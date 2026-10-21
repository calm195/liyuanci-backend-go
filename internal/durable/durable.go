// Package durable 提供同事务持久工作、最小唤醒转发与幂等消费，领域规则由注入处理器负责。
package durable

import (
	"context"
	"errors"
	"regexp"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

var (
	// ErrInvalid 表示参数或事务所有权不符合契约。
	ErrInvalid = errors.New("持久工作参数无效")
	// ErrUnavailable 表示存储操作失败，原始错误不越过边界。
	ErrUnavailable = errors.New("持久工作存储不可用")
	// ErrTemporary 表示处理依赖临时失败，可按持久状态和配置重试。
	ErrTemporary = errors.New("持久工作暂时失败")
	// ErrConfiguration 表示配置或回调不能安全继续，需要异常待办处理。
	ErrConfiguration = errors.New("持久工作处理失败")
	// ErrLeaseLost 表示租约过期或已被其他转发者取得。
	ErrLeaseLost = errors.New("转发租约已失效")
)

// Route 是服务端固定的工作、目标与事件类型映射，不接受任意请求载荷。
type Route struct {
	Kind       string
	TargetKind string
	EventType  string
}

// Draft 只保存随机技术标识及目标版本，不包含正文或外部身份。
type Draft struct {
	WorkID           string
	OperationID      string
	EventID          string
	AggregateID      string
	AggregateVersion int64
}

// Event 是允许发送的最小唤醒载荷；CorrelationID 对应持久工作引用。
type Event struct {
	EventID          string    `json:"event_id"`
	EventType        string    `json:"event_type"`
	AggregateID      string    `json:"aggregate_id"`
	AggregateVersion int64     `json:"aggregate_version"`
	CorrelationID    string    `json:"correlation_id"`
	CreatedAt        time.Time `json:"created_at"`
}

func validID(s string) bool {
	ok, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, s)
	return ok
}
func validRoute(r Route) bool {
	for _, value := range []string{r.Kind, r.TargetKind, r.EventType} {
		ok, _ := regexp.MatchString(`^[a-z][a-z0-9_.]{0,63}$`, value)
		if !ok {
			return false
		}
	}
	return true
}

// Append 仅在调用方事务内追加工作和事件，最终提交/回滚及完成日志由命令入口拥有。
// 返回成功只代表 SQL 已暂存；不得自行提交、在此发送 MQ 或把暂存日志当持久化成功。
func Append(ctx context.Context, tx *gorm.DB, logger *zap.Logger, route Route, draft Draft) (err error) {
	if logger == nil {
		return ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "pending_commit"
		if err != nil {
			result = "failure"
		}
		logger.Info("持久工作暂存完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if tx == nil || tx.Statement == nil || !validRoute(route) || !validID(draft.WorkID) || !validID(draft.OperationID) || !validID(draft.EventID) || !validID(draft.AggregateID) || draft.AggregateVersion <= 0 {
		return ErrInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrInvalid
	}
	// 单条语句让调用方即使错误地忽略返回错误，也不会只落一个没有唤醒的工作。
	result := tx.WithContext(ctx).Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}).Exec(`WITH work AS (
 INSERT INTO public.work_items(id,kind,target_kind,target_id,operation_id) VALUES (?,?,?,?,?) RETURNING id)
 INSERT INTO public.outbox_events(event_id,work_id,aggregate_id,aggregate_version,event_type)
 SELECT ?,id,?,?,? FROM work`, draft.WorkID, route.Kind, route.TargetKind, draft.AggregateID, draft.OperationID, draft.EventID, draft.AggregateID, draft.AggregateVersion, route.EventType)
	if result.Error != nil {
		return ErrUnavailable
	}
	return nil
}

// Sender 只发送技术唤醒；实现须遵守 context，未知失败按配置故障处理，不记录供应商原文。
type Sender interface {
	Send(context.Context, Event) error
}

// Config 显式约束数据库操作、发送、租约、重试间隔和异常待办门槛。
type Config struct {
	DatabaseTimeout time.Duration
	SendTimeout     time.Duration
	LeaseDuration   time.Duration
	RetryDelay      time.Duration
	MaxAttempts     int
}

// Relay 不拥有连接池或 Sender 生命周期；每次调用最多发送一条，调度循环由装配方负责。
type Relay struct {
	db     *gorm.DB
	logger *zap.Logger
	sender Sender
	config Config
	route  Route
}

// NewRelay 拒绝外部事务，外部发送只能发生在领取事务提交之后。
func NewRelay(db *gorm.DB, logger *zap.Logger, sender Sender, route Route, config Config) (*Relay, error) {
	if db == nil || db.Statement == nil || logger == nil || sender == nil || !validRoute(route) || config.DatabaseTimeout <= 0 || config.SendTimeout <= 0 || config.LeaseDuration < time.Millisecond || config.LeaseDuration <= config.SendTimeout || config.RetryDelay < time.Millisecond || config.MaxAttempts < 1 || config.MaxAttempts > 1000 {
		return nil, ErrInvalid
	}
	if _, ok := db.Statement.ConnPool.(gorm.TxCommitter); ok {
		return nil, ErrInvalid
	}
	return &Relay{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), logger: logger, sender: sender, config: config, route: route}, nil
}

type lease struct {
	Event
	Attempts int
}

// Once 领取、提交、发送、确认分离；发送后崩溃可重复，不承诺外部恰好一次。
func (r *Relay) Once(ctx context.Context) (claimed bool, err error) {
	start := time.Now()
	result := "idle"
	defer func() {
		if recover() != nil {
			err = ErrConfiguration
			result = "failure"
		}
		if err != nil {
			result = "failure"
		}
		r.logger.Info("发件箱转发完成", zap.String("result", result), zap.Bool("claimed", claimed), zap.Duration("duration", time.Since(start)))
	}()
	var item lease
	err = r.transaction(ctx, func(tx *gorm.DB) error {
		// 先锁工作再锁发件箱，SKIP LOCKED 仅用于队列领取，不作为普通业务一致性读。
		var candidates []struct{ WorkID, EventID string }
		if failure := tx.Raw(`SELECT w.id AS work_id,e.event_id FROM public.work_items w JOIN public.outbox_events e ON e.work_id=w.id
 WHERE w.kind=? AND w.target_kind=? AND e.event_type=? AND w.status IN ('pending','retry_wait','leased')
 AND e.superseded_at IS NULL AND e.sent_at IS NULL AND e.next_at<=clock_timestamp() AND (e.lease_until IS NULL OR e.lease_until<=clock_timestamp())
 ORDER BY e.next_at,e.event_id LIMIT 1 FOR UPDATE OF w SKIP LOCKED`, r.route.Kind, r.route.TargetKind, r.route.EventType).Scan(&candidates).Error; failure != nil {
			return ErrUnavailable
		}
		if len(candidates) == 0 {
			return nil
		}
		// 原子 UPDATE RETURNING 同时取得新租约代次与发送快照，避免读改写之间被重领。
		var rows []lease
		if failure := tx.Raw(`UPDATE public.outbox_events SET lease_until=clock_timestamp()+(? * interval '1 millisecond'),attempts=attempts+1
 WHERE event_id=? AND superseded_at IS NULL AND sent_at IS NULL AND (lease_until IS NULL OR lease_until<=clock_timestamp())
 RETURNING event_id,event_type,aggregate_id,aggregate_version,work_id AS correlation_id,created_at,attempts`, r.config.LeaseDuration.Milliseconds(), candidates[0].EventID).Scan(&rows).Error; failure != nil {
			return ErrUnavailable
		}
		if len(rows) != 1 {
			return ErrLeaseLost
		}
		// 上次发送在确认前中断也占一次尝试；重领时先耗尽检查，防止持续崩溃无限发送。
		if rows[0].Attempts > r.config.MaxAttempts {
			if failure := tx.Model(&entity.WorkItem{}).Where("id=? AND status NOT IN ('cancelled','completed','attention')", rows[0].CorrelationID).Updates(map[string]any{"status": "attention", "version": gorm.Expr("version+1")}).Error; failure != nil {
				return ErrUnavailable
			}
			if failure := tx.Model(&entity.OutboxEvent{}).Where("event_id=?", rows[0].EventID).Updates(map[string]any{"lease_until": nil}).Error; failure != nil {
				return ErrUnavailable
			}
			result = "attention"
			return nil
		}
		item = rows[0]
		return nil
	})
	if err != nil || item.EventID == "" {
		return false, err
	}
	claimed = true
	// 发送时没有数据库事务；适配器 panic 视为配置故障，落入持久异常待办。
	sendCtx, cancel := context.WithTimeout(ctx, r.config.SendTimeout)
	sendErr := func() (failure error) {
		defer cancel()
		defer func() {
			if recover() != nil {
				failure = ErrConfiguration
			}
		}()
		failure = r.sender.Send(sendCtx, item.Event)
		if failure == nil && sendCtx.Err() != nil {
			failure = sendCtx.Err()
		}
		return failure
	}()
	temporary := errors.Is(sendErr, ErrTemporary) || errors.Is(sendErr, context.Canceled) || errors.Is(sendErr, context.DeadlineExceeded)
	attention := sendErr != nil && (!temporary || item.Attempts >= r.config.MaxAttempts)
	err = r.transaction(ctx, func(tx *gorm.DB) error {
		// 确认前仍先锁工作、后锁事件；Find 保持空结果不报错，由租约校验决定是否失效。
		var works []entity.WorkItem
		if failure := tx.Select("id").Where("id = ?", item.CorrelationID).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&works).Error; failure != nil {
			return ErrUnavailable
		}
		var current []struct{ EventID string }
		if failure := tx.Model(&entity.OutboxEvent{}).Select("event_id").
			Where("event_id = ? AND superseded_at IS NULL AND attempts = ? AND sent_at IS NULL AND lease_until > clock_timestamp()", item.EventID, item.Attempts).
			Clauses(clause.Locking{Strength: "UPDATE"}).Find(&current).Error; failure != nil {
			return ErrUnavailable
		}
		if len(current) != 1 {
			return ErrLeaseLost
		}
		if sendErr == nil {
			if failure := tx.Model(&entity.OutboxEvent{}).Where("event_id=?", item.EventID).Updates(map[string]any{"sent_at": gorm.Expr("clock_timestamp()"), "lease_until": nil}).Error; failure != nil {
				return ErrUnavailable
			}
		} else {
			if failure := tx.Model(&entity.OutboxEvent{}).Where("event_id=?", item.EventID).Updates(map[string]any{"lease_until": nil, "next_at": gorm.Expr("clock_timestamp()+(? * interval '1 millisecond')", r.config.RetryDelay.Milliseconds())}).Error; failure != nil {
				return ErrUnavailable
			}
			// 异常待办不得把 cancelled/completed 工作恢复；解除待办须由后续领域命令重新授权。
			if attention {
				if failure := tx.Model(&entity.WorkItem{}).Where("id=? AND status NOT IN ('cancelled','completed','attention')", item.CorrelationID).Updates(map[string]any{"status": "attention", "version": gorm.Expr("version+1")}).Error; failure != nil {
					return ErrUnavailable
				}
			}
		}
		return nil
	})
	if err != nil {
		return true, err
	}
	if sendErr != nil {
		if attention {
			return true, ErrConfiguration
		}
		return true, ErrTemporary
	}
	result = "success"
	return true, nil
}

func (r *Relay) transaction(ctx context.Context, fn func(*gorm.DB) error) error {
	bounded, cancel := context.WithTimeout(ctx, r.config.DatabaseTimeout)
	defer cancel()
	if failure := r.db.WithContext(bounded).Transaction(fn); failure != nil {
		for _, known := range []error{ErrLeaseLost, ErrUnavailable} {
			if errors.Is(failure, known) {
				return known
			}
		}
		return ErrUnavailable
	}
	return nil
}
