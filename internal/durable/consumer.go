package durable

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// Work 是数据库持久源快照，不从唤醒事件重建，OperationID 是跨投递稳定去重依据。
type Work struct {
	// TargetVersion 来自数据库事件关联的目标版本，与工作自身 Version 分离。
	TargetVersion int64
	ID            string
	Kind          string
	TargetKind    string
	TargetID      string
	Version       int64
	OperationID   string
	Status        string
	LeaseUntil    *time.Time
	Attempts      int
	NextAt        time.Time
}

// Decision 是领域回调已复核当前资格与版本之后给出的终结结果。
type Decision string

const (
	// Complete 表示业务结果已在当前事务内写入。
	Complete Decision = "completed"
	// Obsolete 表示目标已失效，不产生业务结果并取消工作。
	Obsolete Decision = "cancelled"
)

// ConsumerDefinition 由可信服务端装配；Discover 只读发现完整集合，Apply 不执行外部调用。
// Apply 必须使用传入事务复核当前资格、内容版本及隐私屏障，不能信任唤醒中的旧状态。
type ConsumerDefinition struct {
	ConsumerKind string
	Route        Route
	Discover     func(context.Context, *gorm.DB, Work) (command.Discovery, error)
	Apply        func(context.Context, *command.Tx, Work) (Decision, error)
}

// ConsumerConfig 显式约束短事务、锁等待及工作重试预算，不包含业务 SLA。
type ConsumerConfig struct {
	Timeout     time.Duration
	LockTimeout time.Duration
	RetryDelay  time.Duration
	MaxAttempts int
}

// Consumption 描述已提交的处理结果，Missing/Deferred 不写成功回执。
type Consumption struct {
	Applied   bool
	Replayed  bool
	Cancelled bool
	Missing   bool
	Deferred  bool
}

// Consumer 不拥有数据库连接池，调用结束后才允许运输层确认唤醒。
type Consumer struct {
	db         *gorm.DB
	logger     *zap.Logger
	definition ConsumerDefinition
	config     ConsumerConfig
}

// NewConsumer 拒绝外部事务，保证结果、终态及回执由同一个根事务最终提交。
func NewConsumer(db *gorm.DB, logger *zap.Logger, definition ConsumerDefinition, config ConsumerConfig) (*Consumer, error) {
	if db == nil || db.Statement == nil || logger == nil || definition.Discover == nil || definition.Apply == nil || !validRoute(definition.Route) || !validRoute(Route{Kind: definition.ConsumerKind, TargetKind: "work", EventType: "wake"}) || config.Timeout <= 0 || config.LockTimeout < time.Millisecond || config.LockTimeout > config.Timeout || config.RetryDelay < time.Millisecond || config.MaxAttempts < 1 || config.MaxAttempts > 1000 {
		return nil, ErrInvalid
	}
	if _, ok := db.Statement.ConnPool.(gorm.TxCommitter); ok {
		return nil, ErrInvalid
	}
	return &Consumer{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), logger: logger, definition: definition, config: config}, nil
}

func samePlan(a, b command.Discovery) bool {
	return slices.Equal(a.Accounts, b.Accounts) && slices.Equal(a.Admins, b.Admins) && slices.Equal(a.Resources, b.Resources)
}

func (c *Consumer) read(ctx context.Context, db *gorm.DB, event Event) (Work, bool, error) {
	var rows []Work
	failure := db.WithContext(ctx).Raw(`SELECT w.*,e.aggregate_version AS target_version FROM public.work_items w JOIN public.outbox_events e ON e.work_id=w.id
 WHERE w.id=? AND e.event_id=? AND e.aggregate_id=? AND e.aggregate_version=? AND e.event_type=? AND e.created_at=?
 AND e.superseded_at IS NULL AND w.kind=? AND w.target_kind=? AND w.target_id=e.aggregate_id`, event.CorrelationID, event.EventID, event.AggregateID, event.AggregateVersion, event.EventType, event.CreatedAt, c.definition.Route.Kind, c.definition.Route.TargetKind).Scan(&rows).Error
	if failure != nil {
		return Work{}, false, ErrUnavailable
	}
	if len(rows) != 1 {
		return Work{}, false, nil
	}
	return rows[0], true, nil
}

// Consume 只信任数据库中的工作及操作标识，重复和终态不会执行 Apply；未知唤醒不建工作。
func (c *Consumer) Consume(ctx context.Context, event Event) (out Consumption, err error) {
	start := time.Now()
	recovered := false
	defer func() {
		if recover() != nil {
			recovered = true
			err = ErrConfiguration
		}
		if err != nil {
			out = Consumption{}
		}
		result := "success"
		if err != nil {
			result = "failure"
		}
		c.logger.Info("持久工作消费完成", zap.String("result", result), zap.String("consumer", c.definition.ConsumerKind), zap.Duration("duration", time.Since(start)), zap.Bool("recovered", recovered), zap.Bool("replayed", out.Replayed), zap.Bool("cancelled", out.Cancelled), zap.Bool("missing", out.Missing), zap.Bool("deferred", out.Deferred))
	}()
	if !validID(event.EventID) || !validID(event.CorrelationID) || !validID(event.AggregateID) || event.AggregateVersion <= 0 || event.EventType != c.definition.Route.EventType || event.CreatedAt.IsZero() {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	var initial Work
	var plan command.Discovery
	var found bool
	err = c.db.WithContext(ctx).Transaction(func(reader *gorm.DB) error {
		var failure error
		initial, found, failure = c.read(ctx, reader, event)
		if failure != nil || !found {
			return failure
		}
		// 已终结或待人工的工作不依赖已被注销清理的账号/内容，晚到唤醒仍可安全终结。
		if initial.Status == "completed" || initial.Status == "cancelled" || initial.Status == "attention" {
			return nil
		}
		plan, failure = c.definition.Discover(ctx, reader, initial)
		if failure != nil {
			return failure
		}
		plan, failure = command.NormalizePlan(plan)
		return failure
	}, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelReadCommitted})
	if err != nil {
		return out, safeConsumeError(err)
	}
	if !found {
		out.Missing = true
		return out, nil
	}
	// panic 必须先退出 GORM 事务并回滚，之后才能持久化异常待办，绝不提交半个业务结果。
	err = func() (failure error) {
		defer func() {
			if recover() != nil {
				recovered = true
				failure = ErrConfiguration
			}
		}()
		return c.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			tx, failure := command.LockPlan(ctx, db, plan, c.config.LockTimeout)
			if failure != nil {
				return failure
			}
			var rows []Work
			if failure = db.Raw("SELECT * FROM public.work_items WHERE id=? FOR UPDATE", initial.ID).Scan(&rows).Error; failure != nil {
				return ErrUnavailable
			}
			if len(rows) != 1 {
				out.Missing = true
				return nil
			}
			current, exists, failure := c.read(ctx, db, event)
			if failure != nil {
				return failure
			}
			if !exists {
				out.Missing = true
				return nil
			}
			if current.OperationID != initial.OperationID || current.TargetID != initial.TargetID || current.Kind != initial.Kind || current.TargetKind != initial.TargetKind {
				return ErrConfiguration
			}
			var receipts int64
			if failure = db.Raw("SELECT count(*) FROM public.consumer_receipts WHERE consumer_kind=? AND operation_id=?", c.definition.ConsumerKind, current.OperationID).Scan(&receipts).Error; failure != nil {
				return ErrUnavailable
			}
			if receipts > 0 {
				out.Replayed = true
				return nil
			}
			if current.Status == "attention" {
				out.Deferred = true
				return nil
			}
			if current.Status != "completed" && current.Status != "cancelled" {
				if current.Version != initial.Version {
					return ErrTemporary
				}
				if current.NextAt.After(tx.Now()) || (current.Status == "leased" && current.LeaseUntil != nil && current.LeaseUntil.After(tx.Now())) {
					out.Deferred = true
					return nil
				}
				if current.Attempts >= c.config.MaxAttempts {
					if failure = db.Exec("UPDATE public.work_items SET status='attention',lease_until=NULL,version=version+1 WHERE id=?", current.ID).Error; failure != nil {
						return ErrUnavailable
					}
					out.Deferred = true
					return nil
				}
				discovered, failure := c.definition.Discover(ctx, db, current)
				if failure != nil {
					return failure
				}
				discovered, failure = command.NormalizePlan(discovered)
				if failure != nil {
					return failure
				}
				if !samePlan(plan, discovered) {
					return ErrTemporary
				}
				decision, failure := c.definition.Apply(ctx, tx, current)
				if failure != nil {
					return failure
				}
				if decision != Complete && decision != Obsolete {
					return ErrConfiguration
				}
				if failure = db.Exec("UPDATE public.work_items SET status=?,lease_until=NULL,version=version+1,attempts=attempts+1 WHERE id=?", string(decision), current.ID).Error; failure != nil {
					return ErrUnavailable
				}
				out.Applied = decision == Complete
				out.Cancelled = decision == Obsolete
			} else {
				out.Cancelled = current.Status == "cancelled"
				out.Replayed = current.Status == "completed"
			}
			if failure = db.Exec("INSERT INTO public.consumer_receipts(consumer_kind,operation_id) VALUES (?,?)", c.definition.ConsumerKind, current.OperationID).Error; failure != nil {
				return ErrUnavailable
			}
			return nil
		})
	}()
	if err != nil {
		err = safeConsumeError(err)
		if errors.Is(err, ErrTemporary) || errors.Is(err, ErrConfiguration) {
			if failure := c.schedule(ctx, initial, err); failure != nil {
				return out, failure
			}
		}
	}
	return out, err
}

func safeConsumeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrTemporary) || errors.Is(err, command.ErrRetry) || errors.Is(err, command.ErrVersionConflict) {
		return ErrTemporary
	}
	if errors.Is(err, ErrConfiguration) || errors.Is(err, ErrInvalid) || errors.Is(err, command.ErrInvalid) {
		return ErrConfiguration
	}
	return ErrUnavailable
}

func (c *Consumer) schedule(ctx context.Context, initial Work, cause error) error {
	// 失败重试仅调度工作元数据，不持业务资源锁，也不覆盖其他消费者刚提交的版本/终态。
	failure := c.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var rows []Work
		if failure := db.Raw("SELECT * FROM public.work_items WHERE id=? FOR UPDATE", initial.ID).Scan(&rows).Error; failure != nil {
			return ErrUnavailable
		}
		if len(rows) != 1 {
			return nil
		}
		current := rows[0]
		if current.Version != initial.Version || current.Status == "cancelled" || current.Status == "completed" || current.Status == "attention" {
			return nil
		}
		status := "retry_wait"
		if errors.Is(cause, ErrConfiguration) || current.Attempts+1 >= c.config.MaxAttempts {
			status = "attention"
		}
		if failure := db.Exec("UPDATE public.work_items SET status=?,lease_until=NULL,attempts=attempts+1,version=version+1,next_at=clock_timestamp()+(? * interval '1 millisecond') WHERE id=?", status, c.config.RetryDelay.Milliseconds(), current.ID).Error; failure != nil {
			return ErrUnavailable
		}
		return nil
	})
	return safeConsumeError(failure)
}
