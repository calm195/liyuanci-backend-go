package durable

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// Compensation 统计有界扫描的独立处理结果；部分成功不会因后续失败回滚。
type Compensation struct {
	Scanned   int
	Applied   int
	Replayed  int
	Cancelled int
	Deferred  int
	Missing   int
	Attention int
	Failed    int
}

// Compensate 从到期持久源补偿，不依赖 Broker 是否确认，也不重复创建工作或发送外部请求。
// 调度方显式控制调用频率；每次最多扫描一百项，消费仍走同一套当前状态及回执校验。
func (c *Consumer) Compensate(ctx context.Context, limit int) (out Compensation, err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		c.logger.Info("数据库工作补偿完成", zap.String("result", result), zap.Duration("duration", time.Since(start)), zap.Int("scanned", out.Scanned), zap.Int("applied", out.Applied), zap.Int("replayed", out.Replayed), zap.Int("cancelled", out.Cancelled), zap.Int("deferred", out.Deferred), zap.Int("missing", out.Missing), zap.Int("attention", out.Attention), zap.Int("failed", out.Failed))
	}()
	if limit < 1 || limit > 100 {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	// 候选不是授权或领取快照；并行扫描可能选到相同工作，Consume 的根事务负责串行化。
	var events []Event
	if failure := c.db.WithContext(ctx).Raw(`SELECT w.id AS correlation_id,e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.created_at
 FROM public.work_items w LEFT JOIN LATERAL (
 SELECT event_id,event_type,aggregate_id,aggregate_version,created_at FROM public.outbox_events
 WHERE work_id=w.id AND event_type=? AND aggregate_id=w.target_id AND superseded_at IS NULL
 ORDER BY created_at,event_id LIMIT 1) e ON true
 WHERE w.kind=? AND w.target_kind=? AND w.status IN ('pending','retry_wait','leased')
 AND w.next_at<=clock_timestamp() AND (w.status<>'leased' OR w.lease_until<=clock_timestamp())
 ORDER BY w.next_at,w.id LIMIT ?`, c.definition.Route.EventType, c.definition.Route.Kind, c.definition.Route.TargetKind, limit).Scan(&events).Error; failure != nil {
		return out, ErrUnavailable
	}
	for _, event := range events {
		out.Scanned++
		if event.EventID == "" {
			// 目标版本保存在事件关联，缺失时不能从旧消息猜测；只将仍到期且无关联的工作转待办。
			result := c.db.WithContext(ctx).Exec(`UPDATE public.work_items w SET status='attention',version=version+1,lease_until=NULL
 WHERE w.id=? AND w.kind=? AND w.target_kind=? AND w.status IN ('pending','retry_wait','leased') AND w.next_at<=clock_timestamp()
 AND (w.status<>'leased' OR w.lease_until<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM public.outbox_events e WHERE e.work_id=w.id AND e.event_type=? AND e.aggregate_id=w.target_id AND e.superseded_at IS NULL)`, event.CorrelationID, c.definition.Route.Kind, c.definition.Route.TargetKind, c.definition.Route.EventType)
			if result.Error != nil {
				out.Failed++
				if err == nil {
					err = ErrUnavailable
				}
			} else {
				out.Attention += int(result.RowsAffected)
			}
			continue
		}
		got, failure := c.Consume(ctx, event)
		if failure != nil {
			out.Failed++
			if err == nil {
				err = failure
			}
			continue
		}
		if got.Applied {
			out.Applied++
		}
		if got.Replayed {
			out.Replayed++
		}
		if got.Cancelled {
			out.Cancelled++
		}
		if got.Deferred {
			out.Deferred++
		}
		if got.Missing {
			out.Missing++
		}
	}
	return out, err
}

// RedriveDecision 是受控重驱动前重新复核当前业务资格的结果，不执行业务效果。
type RedriveDecision string

const (
	// Resume 允许以同一操作标识重新调度当前工作。
	Resume RedriveDecision = "resume"
	// Invalidate 表示工作已失效，记录无操作终结而非恢复执行。
	Invalidate RedriveDecision = "invalidate"
)

// RedrivePolicy 必须由服务端固定角色和当前状态复核回调，不能由客户端提交。
type RedrivePolicy struct {
	AllowedRoles []string
	Recheck      func(context.Context, *command.Tx, Work) (RedriveDecision, error)
}

// RedriveRequest 绑定明确工作及工作自身版本，不能用目标内容版本代替。
type RedriveRequest struct {
	WorkID          string
	ExpectedVersion int64
}

// Redriven 仅为调用方事务中的暂存结果，最终提交和幂等回执归 command.Engine 所有。
type Redriven struct {
	Resumed   bool
	Cancelled bool
}

func (c *Consumer) source(ctx context.Context, db *gorm.DB, id string) (Work, error) {
	var rows []Work
	if failure := db.WithContext(ctx).Raw(`SELECT w.*,coalesce(e.aggregate_version,0) AS target_version FROM public.work_items w
 LEFT JOIN LATERAL (SELECT aggregate_version FROM public.outbox_events WHERE work_id=w.id AND event_type=? AND aggregate_id=w.target_id AND superseded_at IS NULL ORDER BY created_at,event_id LIMIT 1) e ON true
 WHERE w.id=? AND w.kind=? AND w.target_kind=?`, c.definition.Route.EventType, id, c.definition.Route.Kind, c.definition.Route.TargetKind).Scan(&rows).Error; failure != nil {
		return Work{}, command.ErrUnavailable
	}
	if len(rows) != 1 {
		return Work{}, command.ErrNotFound
	}
	return rows[0], nil
}

// RedrivePlan 供命令 Discover 回调发现工作当前依赖，不能将工作状态当作重放授权条件。
func (c *Consumer) RedrivePlan(ctx context.Context, db *gorm.DB, id string) (command.Discovery, error) {
	if db == nil || !validID(id) {
		return command.Discovery{}, command.ErrInvalid
	}
	work, err := c.source(ctx, db, id)
	if err != nil {
		return command.Discovery{}, err
	}
	plan, err := c.definition.Discover(ctx, db, work)
	if err != nil {
		return command.Discovery{}, command.ErrUnavailable
	}
	return command.NormalizePlan(plan)
}

// Redrive 仅可作为已认证后台 command.Apply 内的操作；必须先通过 RedrivePlan 锁定完整依赖。
// 不建立独立事务，不发 MQ，不调用领域 Apply；新事件替代旧事件，永不重置旧租约代次。
func (c *Consumer) Redrive(ctx context.Context, tx *command.Tx, policy RedrivePolicy, input RedriveRequest) (out Redriven, err error) {
	start := time.Now()
	defer func() {
		result := "pending_commit"
		if err != nil {
			result = "failure"
		}
		c.logger.Info("持久工作重驱动暂存完成", zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	if tx == nil || !validID(input.WorkID) || input.ExpectedVersion < 1 || policy.Recheck == nil || len(policy.AllowedRoles) == 0 {
		return out, command.ErrInvalid
	}
	actor := tx.Actor()
	if actor.Kind != "admin" {
		return out, command.ErrForbidden
	}
	allowed := false
	for _, role := range policy.AllowedRoles {
		if !slices.Contains([]string{"super_admin", "review_operator", "crisis_operator"}, role) {
			return out, command.ErrInvalid
		}
		if slices.Contains(actor.Roles, role) {
			allowed = true
		}
	}
	if !allowed {
		return out, command.ErrForbidden
	}
	work, err := c.source(ctx, tx.DB(), input.WorkID)
	if err != nil {
		return out, err
	}
	plan, err := c.RedrivePlan(ctx, tx.DB(), work.ID)
	if err != nil {
		return out, err
	}
	if err = holdsPlan(tx, plan); err != nil {
		return out, err
	}
	var locked []Work
	if failure := tx.DB().WithContext(ctx).Raw("SELECT * FROM public.work_items WHERE id=? FOR UPDATE", work.ID).Scan(&locked).Error; failure != nil {
		return out, command.ErrUnavailable
	}
	if len(locked) != 1 {
		return out, command.ErrNotFound
	}
	current, err := c.source(ctx, tx.DB(), work.ID)
	if err != nil {
		return out, err
	}
	if current.Version != input.ExpectedVersion {
		return out, command.ErrVersionConflict
	}
	if current.Status != "attention" {
		return out, command.ErrConflict
	}
	if current.OperationID != work.OperationID || current.TargetID != work.TargetID {
		return out, command.ErrRetry
	}
	fresh, err := c.RedrivePlan(ctx, tx.DB(), work.ID)
	if err != nil {
		return out, err
	}
	if !samePlan(plan, fresh) {
		return out, command.ErrRetry
	}
	if err = holdsPlan(tx, fresh); err != nil {
		return out, err
	}
	var receipts int64
	if failure := tx.DB().WithContext(ctx).Raw("SELECT count(*) FROM public.consumer_receipts WHERE consumer_kind=? AND operation_id=?", c.definition.ConsumerKind, current.OperationID).Scan(&receipts).Error; failure != nil {
		return out, command.ErrUnavailable
	}
	if receipts != 0 {
		return out, command.ErrConflict
	}
	decision, err := policy.Recheck(ctx, tx, current)
	if err != nil {
		return out, err
	}
	if decision != Resume && decision != Invalidate {
		return out, command.ErrInvalid
	}
	if decision == Resume && current.TargetVersion < 1 {
		return out, command.ErrConflict
	}
	// 所有唤醒按固定事件顺序锁定；旧转发者之后只能看到失效租约，不能确认或把新周期写成 attention。
	var eventIDs []string
	if failure := tx.DB().WithContext(ctx).Raw("SELECT event_id FROM public.outbox_events WHERE work_id=? ORDER BY event_id FOR UPDATE", work.ID).Scan(&eventIDs).Error; failure != nil {
		return out, command.ErrUnavailable
	}
	if failure := tx.DB().WithContext(ctx).Exec("UPDATE public.outbox_events SET superseded_at=clock_timestamp(),lease_until=NULL WHERE work_id=? AND superseded_at IS NULL", work.ID).Error; failure != nil {
		return out, command.ErrUnavailable
	}
	if decision == Invalidate {
		if failure := tx.DB().WithContext(ctx).Exec("UPDATE public.work_items SET status='cancelled',lease_until=NULL,version=version+1 WHERE id=?", work.ID).Error; failure != nil {
			return out, command.ErrUnavailable
		}
		if failure := tx.DB().WithContext(ctx).Exec("INSERT INTO public.consumer_receipts(consumer_kind,operation_id) VALUES (?,?)", c.definition.ConsumerKind, current.OperationID).Error; failure != nil {
			return out, command.ErrUnavailable
		}
		out.Cancelled = true
		return out, nil
	}
	id, err := eventUUID()
	if err != nil {
		return out, command.ErrInternal
	}
	if failure := tx.DB().WithContext(ctx).Exec("UPDATE public.work_items SET status='pending',lease_until=NULL,attempts=0,next_at=clock_timestamp(),version=version+1 WHERE id=?", work.ID).Error; failure != nil {
		return out, command.ErrUnavailable
	}
	if failure := tx.DB().WithContext(ctx).Exec("INSERT INTO public.outbox_events(event_id,work_id,aggregate_id,aggregate_version,event_type) VALUES (?,?,?,?,?)", id, work.ID, current.TargetID, current.TargetVersion, c.definition.Route.EventType).Error; failure != nil {
		return out, command.ErrUnavailable
	}
	out.Resumed = true
	return out, nil
}

func holdsPlan(tx *command.Tx, plan command.Discovery) error {
	for _, id := range plan.Accounts {
		if _, ok := tx.Guard(id); !ok {
			return command.ErrRetry
		}
	}
	for _, ref := range plan.Resources {
		if _, ok := tx.Resource(ref); !ok {
			return command.ErrRetry
		}
	}
	for _, id := range plan.Admins {
		if id != tx.Actor().ID {
			return command.ErrInvalid
		}
	}
	return nil
}

func eventUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 15) | 64
	raw[8] = (raw[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

// RedriveCommand 构造固定后台命令，使首次执行和幂等重放都复核当前角色及对象访问。
// 调用方必须将同一个 input 作为 command.Input.Request，令请求摘要覆盖工作期望版本。
func (c *Consumer) RedriveCommand(policy RedrivePolicy, input RedriveRequest) (command.Definition, error) {
	if !validID(input.WorkID) || input.ExpectedVersion < 1 || policy.Recheck == nil || len(policy.AllowedRoles) == 0 {
		return command.Definition{}, command.ErrInvalid
	}
	roles := append([]string(nil), policy.AllowedRoles...)
	for _, role := range roles {
		if !slices.Contains([]string{"super_admin", "review_operator", "crisis_operator"}, role) {
			return command.Definition{}, command.ErrInvalid
		}
	}
	policy.AllowedRoles = roles
	return command.Definition{
		Kind: "work.redrive", Method: "POST", ResourcePath: "/admin/works/" + input.WorkID + "/redrive", ActorKind: "admin", AllowedRoles: roles,
		Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
			return c.RedrivePlan(ctx, db, input.WorkID)
		},
		Authorize: func(ctx context.Context, tx *command.Tx) error {
			work, err := c.source(ctx, tx.DB(), input.WorkID)
			if err != nil {
				return err
			}
			decision, err := policy.Recheck(ctx, tx, work)
			if err != nil {
				return err
			}
			if decision != Resume && decision != Invalidate {
				return command.ErrInvalid
			}
			return nil
		},
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			if _, err := c.Redrive(ctx, tx, policy, input); err != nil {
				return "", err
			}
			return input.WorkID, nil
		},
		Resolve: func(ctx context.Context, tx *command.Tx, id string) error {
			_, err := c.source(ctx, tx.DB(), id)
			return err
		},
	}, nil
}

// RunCompensator 显式运行串行数据库补偿循环，不由普通 API/探针启动，也不拥有连接池。
// 取消后停止接收新批次并等待当前调用退出；单批失败保持持久状态，下个时钟周期重试。
func (c *Consumer) RunCompensator(ctx context.Context, interval time.Duration, limit int) (err error) {
	start := time.Now()
	batches, failures := 0, 0
	defer func() {
		if recover() != nil {
			err = ErrConfiguration
		}
		result := "cancelled"
		if err != nil {
			result = "failure"
		}
		c.logger.Info("数据库补偿调度完成", zap.String("result", result), zap.Duration("duration", time.Since(start)), zap.Int("batches", batches), zap.Int("failures", failures))
	}()
	if interval < time.Millisecond || limit < 1 || limit > 100 {
		return ErrInvalid
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		batches++
		if _, failure := c.Compensate(ctx, limit); failure != nil {
			failures++
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
