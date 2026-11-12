// Package durable 提供同事务持久工作、事件关联与数据库幂等消费，领域规则由注入处理器负责。
package durable

import (
	"context"
	"errors"
	"regexp"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"
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

// 固定协议表达式只编译一次，避免每个工作条目及路由字段重复分配。
var (
	identifierPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	routePattern      = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,63}$`)
)

func validID(s string) bool {
	return identifierPattern.MatchString(s)
}

func validRoute(r Route) bool {
	for _, value := range []string{r.Kind, r.TargetKind, r.EventType} {
		if !routePattern.MatchString(value) {
			return false
		}
	}
	return true
}

// Append 仅在调用方事务内追加工作和事件，最终提交/回滚及完成日志由命令入口拥有。
// 返回成功只代表 SQL 已暂存；不得自行提交、在此调用外部供应商 或把暂存日志当持久化成功。
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
	if tx == nil || tx.Statement == nil || !validRoute(route) || !validID(draft.WorkID) || !validID(draft.OperationID) ||
		!validID(draft.EventID) ||
		!validID(draft.AggregateID) ||
		draft.AggregateVersion <= 0 {
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
