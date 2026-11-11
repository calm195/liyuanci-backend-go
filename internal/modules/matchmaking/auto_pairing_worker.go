package matchmaking

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// AutoWorker 复用统一持久工作；计算在锁外进行，最终提交重新验证完整输入指纹。
type AutoWorker struct {
	service *Service
	config  durable.ConsumerConfig
}

// NewAutoWorker 仅构造消费者，不启动协程；生命周期由content工作宿主管理。
func NewAutoWorker(db *gorm.DB, log *zap.Logger, codec *securedata.Codec, cfg durable.ConsumerConfig) (*AutoWorker, error) {
	if db == nil || log == nil || codec == nil || cfg.Timeout <= 0 {
		return nil, command.ErrInvalid
	}
	return &AutoWorker{service: &Service{db: db, log: log, codec: codec}, config: cfg}, nil
}

func autoSolution(ctx context.Context, snapshot pairingSnapshot, seed int64) (AutoResult, error) {
	out := emptyAutoResult()
	left, right := []string{}, []string{}
	people := map[string]pairingPerson{}
	for _, p := range snapshot.People {
		person := AutoPerson{EnrollmentID: p.Row.ID, Name: p.Profile.Name}
		if p.Reason != "" {
			out.Excluded = append(out.Excluded, AutoUnmatched{AutoPerson: person, Reason: p.Reason})
			continue
		}
		people[p.Row.ID] = p
		if p.Profile.Gender == "male" {
			left = append(left, p.Row.ID)
		} else {
			right = append(right, p.Row.ID)
		}
	}
	edges := make([]autoPairingEdge, 0, len(left)*len(right))
	degree := map[string]int{}
	for _, a := range left {
		for _, b := range right {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			first, second := people[a], people[b]
			if snapshot.Blocks[blockedKey(first.Row.AccountID, second.Row.AccountID)] {
				continue
			}
			score, _ := Score(first.Profile, second.Profile)
			edges = append(edges, autoPairingEdge{Left: a, Right: b, Score: score})
			degree[a]++
			degree[b]++
		}
	}
	matching, err := solveAutoPairing(ctx, left, right, edges, seed)
	if err != nil {
		return out, err
	}
	used := map[string]bool{}
	for _, edge := range matching {
		first, second := people[edge.Left], people[edge.Right]
		score, details := Score(first.Profile, second.Profile)
		out.Pairs = append(out.Pairs, AutoPair{First: AutoPerson{EnrollmentID: first.Row.ID, Name: first.Profile.Name}, Second: AutoPerson{EnrollmentID: second.Row.ID, Name: second.Profile.Name}, Score: score, ScoreDetails: details})
		used[edge.Left], used[edge.Right] = true, true
	}
	for _, p := range snapshot.People {
		if p.Reason != "" || used[p.Row.ID] {
			continue
		}
		reason := "competition"
		if degree[p.Row.ID] == 0 {
			reason = "no_candidate"
		}
		out.Unmatched = append(out.Unmatched, AutoUnmatched{AutoPerson: AutoPerson{EnrollmentID: p.Row.ID, Name: p.Profile.Name}, Reason: reason})
	}
	return out, nil
}

// ProcessNext 扫描持久事件；中断后用同一seed重算，完成回执避免重复提交结果。
func (w *AutoWorker) ProcessNext(ctx context.Context) (out durable.Consumption, err error) {
	started := time.Now()
	defer func() {
		w.service.log.Info("自动分配任务处理完成", zap.Bool("applied", out.Applied), zap.Bool("failed", err != nil), zap.Duration("duration", time.Since(started)))
	}()
	route := autoRoute()
	// 超出持久重试预算的任务显示失败并释放活动任务位，不永久停留在计算中。
	if err = w.service.db.WithContext(ctx).Exec(`UPDATE public.matchmaking_auto_jobs j SET status='failed',version=j.version+1,error='计算依赖失败，请重新生成'
 WHERE j.status IN ('pending','running') AND EXISTS(SELECT 1 FROM public.work_items w WHERE w.target_id=j.id AND w.kind=? AND w.status='attention')`, route.Kind).Error; err != nil {
		return
	}
	var events []durable.Event
	err = w.service.db.WithContext(ctx).Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at
 FROM public.work_items w JOIN public.outbox_events e ON e.work_id=w.id AND e.superseded_at IS NULL
 WHERE w.kind=? AND w.target_kind=? AND e.event_type=? AND w.status IN ('pending','retry_wait','leased')
 AND w.next_at<=clock_timestamp() AND (w.status<>'leased' OR w.lease_until<=clock_timestamp()) ORDER BY w.next_at,w.id LIMIT 1`, route.Kind, route.TargetKind, route.EventType).Scan(&events).Error
	if err != nil {
		return
	}
	if len(events) == 0 {
		return durable.Consumption{Deferred: true}, nil
	}
	event := events[0]
	if err = w.service.db.WithContext(ctx).Model(&entity.MatchmakingAutoJob{}).Where("id=? AND status='pending'", event.AggregateID).Updates(map[string]any{"status": "running", "version": gorm.Expr("version+1")}).Error; err != nil {
		return
	}
	var job entity.MatchmakingAutoJob
	var snapshot pairingSnapshot
	prepare := w.service.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var e error
		job, e = autoJob(db, event.AggregateID)
		if e != nil {
			return e
		}
		if job.Status != "running" {
			return nil
		}
		var now time.Time
		if e = db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		snapshot, e = w.service.pairingSnapshot(ctx, db, job.ActivityID, now)
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	// 准备阶段失败也交给持久消费者，孤儿目标会取消、临时故障按原任务重试。
	result := emptyAutoResult()
	if job.Status == "running" && snapshot.Fingerprint == job.InputHash && time.Now().Before(job.ExpiresAt) {
		result, prepare = autoSolution(ctx, snapshot, job.Seed)
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	consumer, e := durable.NewConsumer(w.service.db, w.service.log, durable.ConsumerDefinition{
		ConsumerKind: "matchmaking.auto_pairing", Route: route,
		Discover: func(ctx context.Context, db *gorm.DB, work durable.Work) (command.Discovery, error) {
			row, e := autoJob(db, work.TargetID)
			if errors.Is(e, command.ErrNotFound) {
				return command.Discovery{}, nil
			}
			if e != nil {
				return command.Discovery{}, e
			}
			return discover(ctx, db, row.ActivityID)
		},
		Apply: func(ctx context.Context, tx *command.Tx, work durable.Work) (durable.Decision, error) {
			row, e := autoJob(tx.DB(), work.TargetID)
			if errors.Is(e, command.ErrNotFound) {
				return durable.Obsolete, nil
			}
			if e != nil {
				return "", e
			}
			if row.Status != "running" && row.Status != "pending" {
				return durable.Obsolete, nil
			}
			current, e := w.service.pairingSnapshot(ctx, tx.DB(), row.ActivityID, tx.Now())
			if e != nil {
				return "", e
			}
			if !tx.Now().Before(row.ExpiresAt) || !autoWindow(current.Activity, tx.Now()) || current.Fingerprint != row.InputHash || snapshot.Fingerprint != row.InputHash {
				e = tx.DB().Model(&row).Updates(map[string]any{"status": "stale", "version": row.Version + 1, "error": "参与资料或资格已变化，请重新生成", "result_cipher": nil}).Error
				return durable.Complete, e
			}
			if prepare != nil {
				e = tx.DB().Model(&row).Updates(map[string]any{"status": "failed", "version": row.Version + 1, "error": "计算失败，请重新生成"}).Error
				return durable.Complete, e
			}
			raw, e := json.Marshal(result)
			if e != nil {
				return "", e
			}
			defer clear(raw)
			envelope, e := w.service.codec.Encrypt(ctx, autoBinding(row.ID), raw)
			if e != nil {
				return "", e
			}
			cipher, e := json.Marshal(envelope)
			if e != nil {
				return "", e
			}
			total := 0
			for _, p := range result.Pairs {
				total += p.Score
			}
			e = tx.DB().Model(&row).Updates(map[string]any{"status": "ready", "version": row.Version + 1, "result_cipher": json.RawMessage(cipher), "paired_count": len(result.Pairs), "unmatched_count": len(result.Unmatched), "excluded_count": len(result.Excluded), "eligible_count": 2*len(result.Pairs) + len(result.Unmatched), "participant_count": len(current.People), "total_score": total}).Error
			return durable.Complete, e
		},
	}, w.config)
	if e != nil {
		return out, e
	}
	return consumer.Consume(ctx, event)
}
