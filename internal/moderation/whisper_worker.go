package moderation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// WhisperReviewRoute 固定树洞工作映射，不与旧业务队列混用。
func WhisperReviewRoute() durable.Route {
	return durable.Route{Kind: "whisper_review", TargetKind: "moderation_task", EventType: "whisper.review.requested"}
}

// WhisperReviewSink 将技术唤醒和审核任务一起暂存，不在业务事务内发送消息。
type WhisperReviewSink struct{ Logger *zap.Logger }

// Append 实现树洞送审边界；稳定操作标识绑定任务而非发送次数。
func (s WhisperReviewSink) Append(ctx context.Context, db *gorm.DB, in whisper.ReviewSubmission) error {
	if !whisperKind(in.Kind) || in.Round < 1 {
		return command.ErrInvalid
	}
	return durable.Append(ctx, db, s.Logger, WhisperReviewRoute(), durable.Draft{WorkID: uuid.NewString(), OperationID: in.TaskID, EventID: uuid.NewString(), AggregateID: in.TaskID, AggregateVersion: in.ContentVersion})
}

// WhisperAssessment 的 Body/Identity 仅用于公开内容；私密胶囊分类不得返回替换正文。
type WhisperAssessment struct {
	Body                                  string
	Identity                              whisper.IdentityInput
	SchoolRelated, OrdinaryHit, CrisisHit bool
	DetectorVersion                       string
}

// WhisperPublicAnalyzer 明确接受树洞种类和显式身份输入，不能借用旧业务标签推断身份。
type WhisperPublicAnalyzer interface {
	AnalyzeWhisper(context.Context, string, string, whisper.IdentityInput) (WhisperAssessment, error)
}

// WhisperPrivateAnalyzer 只分类胶囊原文，没有公开脱敏返回通道。
type WhisperPrivateAnalyzer interface {
	ClassifyWhisperCapsule(context.Context, string) (MessageAssessment, error)
}

// WhisperWorker 使用持久消费收据、事务外分析和锁内资格复核；不拥有连接池生命周期。
type WhisperWorker struct {
	db      *gorm.DB
	logger  *zap.Logger
	codec   *securedata.Codec
	public  WhisperPublicAnalyzer
	private WhisperPrivateAnalyzer
	cfg     WorkerConfig
}

// NewWhisperWorker 要求两个不同的分析边界均显式注入；未配置审核时不能启动真实消费者。
func NewWhisperWorker(db *gorm.DB, logger *zap.Logger, codec *securedata.Codec, public WhisperPublicAnalyzer, private WhisperPrivateAnalyzer, cfg WorkerConfig) (*WhisperWorker, error) {
	if db == nil || logger == nil || codec == nil || public == nil || private == nil || cfg.AnalysisTimeout <= 0 || !validateCare(cfg.Care) {
		return nil, durable.ErrInvalid
	}
	if cfg.Care != nil {
		copy := *cfg.Care
		cfg.Care = &copy
	}
	w := &WhisperWorker{db, logger, codec, public, private, cfg}
	_, e := w.consumer(nil, nil, nil)
	return w, e
}

// Consume 先验证持久事件，再在事务外调用分析器；重复、取消及迟到事件不重建内容。
func (w *WhisperWorker) Consume(ctx context.Context, event durable.Event) (out durable.Consumption, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = durable.ErrConfiguration
		}
		w.logger.Info("树洞审核消费完成", zap.Bool("applied", out.Applied), zap.Bool("replayed", out.Replayed), zap.Bool("success", err == nil), zap.Duration("duration", time.Since(start)))
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*w.cfg.Consumer.Timeout+w.cfg.AnalysisTimeout)
	defer cancel()
	var r whisperSource
	var rules rules
	var body string
	var identity whisper.IdentityInput
	valid, input := false, false
	prep := w.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var n int64
		e := db.Raw(`SELECT count(*) FROM public.outbox_events e JOIN public.work_items w ON w.id=e.work_id WHERE e.event_id=? AND e.event_type=? AND e.aggregate_id=? AND e.aggregate_version=? AND e.work_id=? AND e.created_at=? AND e.superseded_at IS NULL AND w.kind=? AND w.target_id=e.aggregate_id`, event.EventID, event.EventType, event.AggregateID, event.AggregateVersion, event.CorrelationID, event.CreatedAt, WhisperReviewRoute().Kind).Scan(&n).Error
		if e != nil || n != 1 || event.EventType != WhisperReviewRoute().EventType {
			return e
		}
		valid = true
		var work entity.WorkItem
		if e = db.Where("id=?", event.CorrelationID).Take(&work).Error; e != nil {
			return e
		}
		var now time.Time
		if e = db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		if work.Status == "completed" || work.Status == "cancelled" || work.Status == "attention" || work.NextAt.After(now) || work.Status == "leased" && work.LeaseUntil != nil && work.LeaseUntil.After(now) {
			return nil
		}
		r, e = loadWhisper(db, event.AggregateID)
		if errors.Is(e, command.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		ok, e := whisperEligible(db, r)
		if e != nil || !ok || r.task.Status != "pending" {
			return e
		}
		rules, e = readRules(db)
		if e != nil {
			return e
		}
		body, identity, e = whisperDecrypt(ctx, db, w.codec, r)
		input = e == nil
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if !valid {
		if prep != nil {
			return out, durable.ErrUnavailable
		}
		return durable.Consumption{Missing: true}, nil
	}
	var a *WhisperAssessment
	var scan *entity.ModerationScan
	if prep == nil && input {
		analysis, stop := context.WithTimeout(ctx, w.cfg.AnalysisTimeout)
		result, e := func() (v WhisperAssessment, e error) {
			defer func() {
				if recover() != nil {
					e = durable.ErrConfiguration
				}
			}()
			if r.registry.Kind == "whisper_capsule" {
				p, e := w.private.ClassifyWhisperCapsule(analysis, body)
				return WhisperAssessment{OrdinaryHit: p.OrdinaryHit, CrisisHit: p.CrisisHit, DetectorVersion: p.DetectorVersion}, e
			}
			return w.public.AnalyzeWhisper(analysis, r.registry.Kind, body, identity)
		}()
		if e == nil {
			e = analysis.Err()
		}
		stop()
		if e != nil {
			prep = durable.ErrTemporary
		} else if !text(result.DetectorVersion, 80) || r.registry.Kind != "whisper_capsule" && (!text(result.Body, 500) || !whisperIdentitySubset(result.Identity, identity) || r.registry.Kind == "whisper_nickname" && (!text(result.Body, 20) || utf8.RuneCountInString(result.Body) < 2)) {
			prep = durable.ErrConfiguration
		} else {
			result.CrisisHit = result.CrisisHit || hit(body, rules.crisisEntries)
			result.OrdinaryHit = result.OrdinaryHit || r.task.PrivacyRecheck || r.task.CrisisRecheck || hit(body, rules.ordinaryEntries)
			result.SchoolRelated = r.registry.Kind != "whisper_capsule" && (result.SchoolRelated || hit(body, rules.schoolEntries))
			raw, _, e := encryptEvidence(ctx, w.codec, "whisper_moderation_projection", r.task.ID, result)
			if e != nil {
				prep = durable.ErrTemporary
			} else {
				a = &result
				scan = &entity.ModerationScan{TaskID: r.task.ID, CrisisVersion: rules.crisis.ID, SchoolVersion: rules.school.ID, OrdinaryVersion: rules.ordinary.ID, DetectorVersion: result.DetectorVersion, SchoolRelated: result.SchoolRelated, OrdinaryHit: result.OrdinaryHit, CrisisHit: result.CrisisHit, ProjectionCipher: raw}
			}
		}
	}
	c, e := w.consumer(scan, a, prep)
	if e != nil {
		return out, e
	}
	return c.Consume(ctx, event)
}
func whisperIdentitySubset(candidate, input whisper.IdentityInput) bool {
	if candidate.Mode != input.Mode {
		return false
	}
	// 公开投影只能保留用户输入或将标签全部去除，不能增加或变换标签。
	if candidate.Tags != nil && (input.Tags == nil || *candidate.Tags != *input.Tags) {
		return false
	}
	if (candidate.NicknameVersion == nil) != (input.NicknameVersion == nil) {
		return false
	}
	return candidate.NicknameVersion == nil || *candidate.NicknameVersion == *input.NicknameVersion
}
func (w *WhisperWorker) consumer(scan *entity.ModerationScan, a *WhisperAssessment, prepare error) (*durable.Consumer, error) {
	return durable.NewConsumer(w.db, w.logger, durable.ConsumerDefinition{ConsumerKind: "whisper_review.scan", Route: WhisperReviewRoute(), Discover: func(ctx context.Context, db *gorm.DB, work durable.Work) (command.Discovery, error) {
		return whisperPlan(ctx, db, work.TargetID)
	}, Apply: func(ctx context.Context, tx *command.Tx, work durable.Work) (durable.Decision, error) {
		db := tx.DB().WithContext(ctx)
		r, e := loadWhisper(db, work.TargetID)
		if errors.Is(e, command.ErrNotFound) {
			return durable.Obsolete, nil
		}
		if e != nil {
			return "", e
		}
		ok, e := whisperEligible(db, r)
		if e != nil {
			return "", e
		}
		if !ok || r.task.Status != "pending" || r.ContentVersion != work.TargetVersion {
			if r.task.Status == "pending" {
				if e = db.Model(&entity.ModerationTask{}).Where("id=?", r.task.ID).Updates(map[string]any{"status": "obsolete", "version": gorm.Expr("version+1")}).Error; e != nil {
					return "", e
				}
			}
			return durable.Obsolete, nil
		}
		if prepare != nil {
			return "", prepare
		}
		if scan == nil || a == nil {
			return "", durable.ErrConfiguration
		}
		if e = db.Create(scan).Error; e != nil {
			return "", e
		}
		if e = db.Model(&entity.ContentRegistry{}).Where("id=?", r.registry.ID).Updates(map[string]any{"school_related": a.SchoolRelated, "school_detection_state": "completed", "school_detection_version": a.DetectorVersion + "/" + scan.SchoolVersion}).Error; e != nil {
			return "", e
		}
		state := "pending"
		taskState := "human_required"
		var decision any
		if a.CrisisHit {
			if _, e = captureWhisperCrisis(ctx, db, w.codec, r, tx.Now()); e != nil {
				return "", e
			}
		} else if !a.SchoolRelated && !a.OrdinaryHit {
			state = "approved"
			taskState = "approved"
			decision = "approved"
		}
		if e = applyWhisperReview(ctx, db, w.codec, r, *a, state, tx.Now()); e != nil {
			return "", e
		}
		e = db.Model(&entity.ModerationTask{}).Where("id=?", r.task.ID).Updates(map[string]any{"status": taskState, "decision": decision, "version": gorm.Expr("version+1")}).Error
		return durable.Complete, e
	}}, w.cfg.Consumer)
}

// ProcessNext 从数据库补偿未完成的树洞工作，不依赖消息唤醒持续存在。
func (w *WhisperWorker) ProcessNext(ctx context.Context) (out durable.Consumption, err error) {
	ctx, cancel := context.WithTimeout(ctx, w.cfg.Consumer.Timeout)
	defer cancel()
	var events []durable.Event
	err = w.db.WithContext(ctx).Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.work_items w JOIN public.outbox_events e ON e.work_id=w.id AND e.superseded_at IS NULL WHERE w.kind=? AND w.status IN ('pending','retry_wait','leased') AND w.next_at<=clock_timestamp() AND (w.status<>'leased' OR w.lease_until<=clock_timestamp()) ORDER BY w.next_at,w.id LIMIT 1`, WhisperReviewRoute().Kind).Scan(&events).Error
	if err != nil {
		return out, durable.ErrUnavailable
	}
	if len(events) == 0 {
		return durable.Consumption{Deferred: true}, nil
	}
	return w.Consume(ctx, events[0])
}

// ProcessCrisisDelivery 复用持久租约投递，仍需要显式关怀资料及真实通知适配器。
func (w *WhisperWorker) ProcessCrisisDelivery(ctx context.Context) (bool, error) {
	return (&Worker{db: w.db, logger: w.logger, codec: w.codec, cfg: w.cfg}).ProcessCrisisDelivery(ctx)
}

// applyWhisperReview 仅消费已审核候选；胶囊只改变审核状态，不改写原文或提前解锁。
func applyWhisperReview(ctx context.Context, db *gorm.DB, codec *securedata.Codec, r whisperSource, a WhisperAssessment, state string, now time.Time) error {
	if e := db.Model(&entity.ContentRegistry{}).Where("id=?", r.registry.ID).Update("status", map[string]string{"pending": "human_required", "approved": "approved", "rejected": "rejected"}[state]).Error; e != nil {
		return e
	}
	switch r.registry.Kind {
	case "whisper_nickname":
		u := map[string]any{"review_state": state, "control_version": gorm.Expr("control_version+1")}
		if state == "approved" {
			u["public_label"] = a.Body
			u["revoked_at"] = nil
		} else {
			u["public_label"] = nil
		}
		if e := db.Model(&entity.WhisperNicknameVersion{}).Where("content_registry_id=?", r.ID).Updates(u).Error; e != nil {
			return e
		}
		if state == "approved" {
			return db.Model(&entity.WhisperPreference{}).Where("account_id=? AND candidate_nickname_version=?", r.OwnerID, r.ContentVersion).Updates(map[string]any{"active_nickname_version": r.ContentVersion, "candidate_nickname_version": nil, "version": gorm.Expr("version+1")}).Error
		}
		return nil
	case "whisper_capsule":
		return db.Model(&entity.WhisperCapsule{}).Where("id=?", r.ID).Updates(map[string]any{"review_state": state, "version": gorm.Expr("version+1")}).Error
	}
	identity := whisper.PublicIdentity{Mode: r.IdentityMode, Label: "", Tags: a.Identity.Tags, IsAuthor: r.registry.Kind == "whisper_post"}
	if identity.Mode == "named" {
		var n entity.WhisperNicknameVersion
		if e := db.Select("public_label").Where("account_id=? AND nickname_version=?", r.OwnerID, *r.NicknameVersion).Take(&n).Error; e != nil {
			return e
		}
		if n.PublicLabel == nil {
			return command.ErrConflict
		}
		identity.Label = *n.PublicLabel
	}
	raw, _ := json.Marshal(identity)
	codes := []string{}
	if a.SchoolRelated {
		codes = append(codes, "UNVERIFIED_SCHOOL")
	}
	notice, _ := json.Marshal(codes)
	u := map[string]any{"review_state": state, "school_related": a.SchoolRelated, "public_body": nil, "public_identity": nil, "notice_codes": json.RawMessage(notice)}
	if state == "approved" {
		u["public_body"] = a.Body
		u["public_identity"] = json.RawMessage(raw)
	}
	var model any = &entity.WhisperPostVersion{}
	var resource any = &entity.WhisperPost{}
	if r.registry.Kind == "whisper_comment" {
		model = &entity.WhisperCommentVersion{}
		resource = &entity.WhisperComment{}
	}
	if e := db.Model(model).Where("content_registry_id=?", r.registry.ID).Updates(u).Error; e != nil {
		return e
	}
	updates := map[string]any{"review_state": state, "version": gorm.Expr("version+1"), "updated_at": now}
	if state == "approved" {
		updates["published_at"] = now
		if r.registry.Kind == "whisper_post" {
			updates["published_content_version"] = r.ContentVersion
		}
	}
	return db.Model(resource).Where("id=?", r.ID).Updates(updates).Error
}
