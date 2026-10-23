package moderation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/chat"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Assessment 是安全处理器审查组合信息后产生的脱敏候选，未复核前不可公开。
type Assessment struct {
	Body                                  string
	Tags                                  wish.IdentityTags
	SchoolRelated, OrdinaryHit, CrisisHit bool
	DetectorVersion                       string
}

// Analyzer 必须执行文本安全、个人及组合信息处理，允许重复调用且不能产生业务写入。
// 本模块没有默认放行实现；缺少供应商时构造失败，外部故障保持待审。
type Analyzer interface {
	Analyze(context.Context, wish.Content) (Assessment, error)
}

// MessageAssessment 只允许分类，不允许改写私聊原文或公开身份标签。
type MessageAssessment struct {
	OrdinaryHit, CrisisHit bool
	DetectorVersion        string
}

// MessageAnalyzer 与公开脱敏分析器隔离；缺少实现时私聊失败关闭。
type MessageAnalyzer interface {
	AnalyzeMessage(context.Context, string) (MessageAssessment, error)
}

// WorkerConfig 限制外部分析和最终数据库事务；调度生命周期由宿主负责。
type WorkerConfig struct {
	// Care 与 CrisisNotifier 必须显式提供；缺失时持久待办继续重试。
	Care            *CareResources
	CrisisNotifier  CrisisNotifier
	MessageAnalyzer MessageAnalyzer
	Consumer        durable.ConsumerConfig
	AnalysisTimeout time.Duration
}

// Worker 不拥有数据库、密钥或安全分析供应商；可同时作为MQ处理器及数据库补偿入口。
type Worker struct {
	db       *gorm.DB
	codec    *securedata.Codec
	analyzer Analyzer
	logger   *zap.Logger
	cfg      WorkerConfig
}

// NewWorker 显式构造心愿、应愿留言、私聊及还愿故事扫描消费者，不启动循环、不迁移或自动发布规则。
func NewWorker(db *gorm.DB, log *zap.Logger, codec *securedata.Codec, analyzer Analyzer, cfg WorkerConfig) (*Worker, error) {
	if db == nil || log == nil || codec == nil || analyzer == nil || cfg.AnalysisTimeout <= 0 || !validateCare(cfg.Care) {
		return nil, command.ErrInvalid
	}
	if cfg.Care != nil {
		c := *cfg.Care
		cfg.Care = &c
	}
	w := &Worker{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), logger: log, codec: codec, analyzer: analyzer, cfg: cfg}
	if _, e := w.consumer(nil, nil, nil); e != nil {
		return nil, e
	}
	return w, nil
}

type rules struct {
	crisis, school, ordinary                      entity.WordlistVersion
	crisisEntries, schoolEntries, ordinaryEntries []string
}

func readRules(db *gorm.DB) (out rules, err error) {
	for _, item := range []struct {
		k       string
		row     *entity.WordlistVersion
		entries *[]string
	}{{"crisis", &out.crisis, &out.crisisEntries}, {"school", &out.school, &out.schoolEntries}, {"ordinary_violation", &out.ordinary, &out.ordinaryEntries}} {
		var head entity.WordlistHead
		if err = db.Where("kind=?", item.k).Take(&head).Error; err != nil {
			return
		}
		if head.EffectiveID == nil {
			return out, command.ErrUnavailable
		}
		if err = db.Where("id=? AND type=? AND status='published'", *head.EffectiveID, item.k).Take(item.row).Error; err != nil {
			return
		}
		*item.entries, err = entries(db, item.row.ID)
		if err != nil {
			return
		}
	}
	return
}
func hit(body string, entries []string) bool {
	for _, entry := range entries {
		if strings.Contains(body, entry) {
			return true
		}
	}
	return false
}
func subset(a, b wish.IdentityTags) bool {
	for _, pair := range [][2]*string{{a.Campus, b.Campus}, {a.Grade, b.Grade}, {a.Gender, b.Gender}} {
		if pair[0] != nil && (pair[1] == nil || *pair[0] != *pair[1]) {
			return false
		}
	}
	return true
}
func candidateBody(row current, body string) bool {
	if row.message != nil || row.story != nil {
		return utf8.ValidString(body) && strings.TrimSpace(body) != ""
	}
	if row.application != nil {
		return utf8.ValidString(body) && utf8.RuneCountInString(body) <= 100
	}
	return text(body, 500)
}

func decryptContent(ctx context.Context, codec *securedata.Codec, row current) (out wish.Content, err error) {
	if row.story != nil {
		out.Body, err = wish.DecryptStory(ctx, codec, *row.story)
		return
	}
	if row.message != nil {
		out.Body, err = chat.DecryptBody(ctx, codec, *row.message)
		return
	}
	if row.application != nil {
		var envelope securedata.Envelope
		if json.Unmarshal(row.application.MessageCipher, &envelope) != nil {
			return out, command.ErrUnavailable
		}
		data, e := codec.Decrypt(ctx, securedata.Binding{Purpose: "application_content", ObjectID: row.application.ID, Version: uint64(row.application.ContentVersion)}, envelope)
		defer clear(data)
		if e != nil || json.Unmarshal(data, &out.Body) != nil || !candidateBody(row, out.Body) {
			return wish.Content{}, command.ErrUnavailable
		}
		return out, nil
	}
	var envelope securedata.Envelope
	if json.Unmarshal(row.content.BodyCipher, &envelope) != nil {
		return out, command.ErrUnavailable
	}
	plain, e := codec.Decrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: row.wish.ID, Version: uint64(row.task.ContentVersion)}, envelope)
	defer clear(plain)
	if e != nil || json.Unmarshal(plain, &out) != nil {
		return wish.Content{}, command.ErrUnavailable
	}
	return out, nil
}

// Consume 先读取不可变内容与同一快照词库，在事务外分析，再按统一锁序原子复核结果与回执。
// 分析允许重复但只做分类；最终效果由稳定operation_id去重，网络调用不持有业务行锁。
func (w *Worker) Consume(ctx context.Context, event durable.Event) (out durable.Consumption, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = durable.ErrConfiguration
		}
		result := "success"
		if err != nil {
			out = durable.Consumption{}
			result = "failure"
		}
		w.logger.Info("心愿相关内容扫描完成", zap.String("result", result), zap.Duration("duration", time.Since(start)), zap.Bool("applied", out.Applied), zap.Bool("cancelled", out.Cancelled), zap.Bool("replayed", out.Replayed))
	}()
	if !validID(event.EventID) || !validID(event.CorrelationID) || !validID(event.AggregateID) || event.EventType != wish.ReviewRoute().EventType || event.AggregateVersion < 1 || event.CreatedAt.IsZero() {
		return out, durable.ErrInvalid
	}
	// 分析及密钥调用均受总预算约束，直接库调用也不能因无上层截止时间而无限等待。
	overall, stopOverall := context.WithTimeout(ctx, 2*w.cfg.Consumer.Timeout+w.cfg.AnalysisTimeout)
	defer stopOverall()
	ctx = overall
	var source current
	var snapshot rules
	var input wish.Content
	var work entity.WorkItem
	var valid, hasInput bool
	bounded, cancel := context.WithTimeout(ctx, w.cfg.Consumer.Timeout)
	defer cancel()
	prep := w.db.WithContext(bounded).Transaction(func(db *gorm.DB) error {
		var n int64
		if e := db.Model(&entity.OutboxEvent{}).Where("event_id=? AND work_id=? AND aggregate_id=? AND aggregate_version=? AND event_type=? AND created_at=? AND superseded_at IS NULL", event.EventID, event.CorrelationID, event.AggregateID, event.AggregateVersion, event.EventType, event.CreatedAt).Count(&n).Error; e != nil {
			return e
		}
		if n != 1 {
			return nil
		}
		if e := db.Where("id=? AND kind=? AND target_kind=? AND target_id=?", event.CorrelationID, wish.ReviewRoute().Kind, wish.ReviewRoute().TargetKind, event.AggregateID).Take(&work).Error; e != nil {
			return e
		}
		valid = true
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		if work.Status == "completed" || work.Status == "cancelled" || work.Status == "attention" || work.NextAt.After(now) || work.Status == "leased" && work.LeaseUntil != nil && work.LeaseUntil.After(now) {
			return nil
		}
		var e error
		source, e = load(db, event.AggregateID)
		if errors.Is(e, command.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		ok, e := eligible(db, source)
		if e != nil {
			return e
		}
		if !ok || source.task.Status != "pending" {
			return nil
		}
		snapshot, e = readRules(db)
		if e != nil {
			return e
		}
		input, e = decryptContent(db.Statement.Context, w.codec, source)
		hasInput = e == nil
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if !valid {
		if prep != nil {
			return out, durable.ErrUnavailable
		}
		out.Missing = true
		return out, nil
	}
	// 父心愿隐私新稿尚未审批时，故事保持持久待处理，不消耗重试或取消新稿。
	if prep == nil && source.story != nil && source.task.PrivacyRecheck {
		holds, e := wish.ReadStoryHolds(ctx, w.db, source.wish.ID)
		if e != nil {
			return out, durable.ErrUnavailable
		}
		if holds.ParentActive {
			return durable.Consumption{Deferred: true}, nil
		}
	}
	var scan *entity.ModerationScan
	var assessment *Assessment
	if prep == nil && hasInput {
		analysis, stop := context.WithTimeout(ctx, w.cfg.AnalysisTimeout)
		a, e := func() (a Assessment, e error) {
			defer func() {
				if recover() != nil {
					e = durable.ErrConfiguration
				}
			}()
			if source.message != nil {
				if w.cfg.MessageAnalyzer == nil {
					return a, command.ErrUnavailable
				}
				result, e := w.cfg.MessageAnalyzer.AnalyzeMessage(analysis, input.Body)
				return Assessment{Body: input.Body, OrdinaryHit: result.OrdinaryHit, CrisisHit: result.CrisisHit, DetectorVersion: result.DetectorVersion}, e
			}
			return w.analyzer.Analyze(analysis, input)
		}()
		if e == nil {
			e = analysis.Err()
		}
		stop()
		if e != nil {
			prep = durable.ErrTemporary
		} else if !candidateBody(source, a.Body) || !text(a.DetectorVersion, 80) || !subset(a.Tags, input.IdentityTags) {
			prep = durable.ErrConfiguration
		} else {
			a.CrisisHit = a.CrisisHit || hit(input.Body, snapshot.crisisEntries)
			if source.message == nil {
				a.SchoolRelated = a.SchoolRelated || hit(input.Body, snapshot.schoolEntries)
			}
			a.OrdinaryHit = a.OrdinaryHit || source.task.PrivacyRecheck || hit(input.Body, snapshot.ordinaryEntries)
			candidate, _ := json.Marshal(struct {
				Body string
				Tags wish.IdentityTags
			}{a.Body, a.Tags})
			defer clear(candidate)
			envelope, e := w.codec.Encrypt(ctx, securedata.Binding{Purpose: "moderation_projection", ObjectID: source.task.ID, Version: 1}, candidate)
			if e != nil {
				prep = durable.ErrTemporary
			} else {
				cipher, e := json.Marshal(envelope)
				if e != nil {
					prep = durable.ErrConfiguration
				} else {
					assessment = &a
					scan = &entity.ModerationScan{TaskID: source.task.ID, CrisisVersion: snapshot.crisis.ID, SchoolVersion: snapshot.school.ID, OrdinaryVersion: snapshot.ordinary.ID, DetectorVersion: a.DetectorVersion, SchoolRelated: a.SchoolRelated, OrdinaryHit: a.OrdinaryHit, CrisisHit: a.CrisisHit, ProjectionCipher: cipher}
				}
			}
		}
	}
	consumer, e := w.consumer(scan, assessment, prep)
	if e != nil {
		return out, e
	}
	return consumer.Consume(ctx, event)
}
func (w *Worker) consumer(scan *entity.ModerationScan, a *Assessment, prepare error) (*durable.Consumer, error) {
	return durable.NewConsumer(w.db, w.logger, durable.ConsumerDefinition{ConsumerKind: "wish_review.scan", Route: wish.ReviewRoute(), Discover: func(ctx context.Context, db *gorm.DB, work durable.Work) (command.Discovery, error) {
		return discover(ctx, db, work.TargetID)
	}, Apply: func(ctx context.Context, tx *command.Tx, work durable.Work) (durable.Decision, error) {
		db := tx.DB().WithContext(ctx)
		row, e := load(db, work.TargetID)
		if errors.Is(e, command.ErrNotFound) {
			return durable.Obsolete, nil
		}
		if e != nil {
			return "", e
		}
		ok, e := eligible(db, row)
		if e != nil {
			return "", e
		}
		if !ok || row.task.ContentVersion != work.TargetVersion || row.task.Status != "pending" {
			if row.story != nil && !ok {
				if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=? AND moderation_status IN ?", row.wish.ID, row.story.StoryVersion, []string{"pending", "human_required"}).Updates(map[string]any{"moderation_status": "cancelled", "public_body": nil}).Error; e != nil {
					return "", e
				}
				if e = db.Model(&entity.ModerationTask{}).Where("id=? AND status='pending'", row.task.ID).Updates(map[string]any{"status": "obsolete", "version": gorm.Expr("version+1")}).Error; e != nil {
					return "", e
				}
			}

			if row.message != nil && !ok {
				if e = chat.CancelMessage(ctx, db, *row.message); e != nil {
					return "", e
				}
			}
			if row.application != nil && (row.application.Status == "pending_scan" || row.application.Status == "pending_review" || row.application.Status == "active") {
				if e = wish.CancelApplication(ctx, db, *row.application, "invalidated", "eligibility_changed"); e != nil {
					return "", e
				}
			}
			return durable.Obsolete, nil
		}
		if prepare != nil {
			if errors.Is(prepare, durable.ErrConfiguration) {
				return "", durable.ErrConfiguration
			}
			return "", durable.ErrTemporary
		}
		if scan == nil || a == nil {
			return "", durable.ErrConfiguration
		}
		if e = db.Create(scan).Error; e != nil {
			return "", e
		}
		status := "human_required"
		decision := any(nil)
		detection := a.DetectorVersion + "/" + scan.SchoolVersion
		models := []any{&entity.ContentRegistry{}}
		if row.story != nil {
			models = append(models, &entity.StoryVersion{})
		}
		if row.application == nil && row.message == nil && row.story == nil {
			models = append(models, &entity.WishVersion{})
		}
		for _, model := range models {
			q := db.Model(model)
			if _, ok := model.(*entity.ContentRegistry); ok {
				q = q.Where("id=?", row.registry.ID)
			} else if row.story != nil {
				q = q.Where("wish_id=? AND story_version=?", row.wish.ID, row.task.ContentVersion)
			} else {
				q = q.Where("wish_id=? AND content_version=?", row.wish.ID, row.task.ContentVersion)
			}
			if e = q.Updates(map[string]any{"school_related": a.SchoolRelated, "school_detection_state": "completed", "school_detection_version": detection}).Error; e != nil {
				return "", e
			}
		}
		if row.story != nil {
			if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", row.wish.ID, row.task.ContentVersion).Update("wordlist_version", scan.OrdinaryVersion).Error; e != nil {
				return "", e
			}
		}
		if row.application == nil && row.message == nil && row.story == nil {
			if e = db.Model(&entity.WishVersion{}).Where("wish_id=? AND content_version=?", row.wish.ID, row.task.ContentVersion).Update("wordlist_version", scan.OrdinaryVersion).Error; e != nil {
				return "", e
			}
		}
		if a.CrisisHit {
			if _, e = captureCrisis(ctx, db, w.codec, row, tx.Now()); e != nil {
				return "", e
			}
		} else if !a.SchoolRelated && !a.OrdinaryHit {
			status = "approved"
			decision = "approved"
			tags, _ := json.Marshal(a.Tags)
			if e = setReview(db, w.codec, row, "approved", &a.Body, tags); e != nil {
				return "", e
			}
		}
		if row.story != nil && status == "human_required" {
			if e = db.Model(&entity.StoryVersion{}).Where("wish_id=? AND story_version=?", row.wish.ID, row.task.ContentVersion).Update("moderation_status", "human_required").Error; e != nil {
				return "", e
			}
		}
		if row.message != nil && status == "human_required" {
			if e = db.Model(&entity.Message{}).Where("id=?", row.message.ID).Updates(map[string]any{"status": "pending_review", "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
		}
		if row.application != nil && status == "human_required" {
			if e = db.Model(&entity.Application{}).Where("id=?", row.application.ID).Updates(map[string]any{"status": "pending_review", "version": gorm.Expr("version+1")}).Error; e != nil {
				return "", e
			}
		}
		if e = db.Model(&entity.ModerationTask{}).Where("id=?", row.task.ID).Updates(map[string]any{"status": status, "decision": decision, "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		return durable.Complete, nil
	}}, w.cfg.Consumer)
}

// ProcessNext 从数据库扫描一条当前有效事件，补偿丢失或已确认的MQ唤醒，不重建工作。
// 等待父隐私审批的故事暂不占候选位，避免其阻塞其他内容扫描。
func (w *Worker) ProcessNext(ctx context.Context) (out durable.Consumption, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = durable.ErrConfiguration
		}
		result := "success"
		if err != nil {
			out = durable.Consumption{}
			result = "failure"
		} else if out.Deferred {
			result = "idle"
		}
		w.logger.Info("心愿审核数据库补偿完成", zap.String("result", result), zap.Duration("duration", time.Since(start)), zap.Bool("applied", out.Applied), zap.Bool("cancelled", out.Cancelled))
	}()

	bounded, cancel := context.WithTimeout(ctx, w.cfg.Consumer.Timeout)
	defer cancel()
	var events []durable.Event
	err = w.db.WithContext(bounded).Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.work_items w JOIN public.outbox_events e ON e.work_id=w.id AND e.superseded_at IS NULL WHERE w.kind=? AND w.target_kind=? AND e.event_type=? AND w.status IN ('pending','retry_wait','leased') AND w.next_at<=clock_timestamp() AND (w.status<>'leased' OR w.lease_until<=clock_timestamp()) AND NOT EXISTS (SELECT 1 FROM public.moderation_tasks t JOIN public.content_registry c ON c.id=t.content_id WHERE t.id=w.target_id AND t.privacy_recheck AND c.kind='story' AND EXISTS(SELECT 1 FROM public.privacy_holds h WHERE h.target_kind='wish' AND h.target_id=c.entity_id AND h.status='active')) ORDER BY w.next_at,w.id LIMIT 1`, wish.ReviewRoute().Kind, wish.ReviewRoute().TargetKind, wish.ReviewRoute().EventType).Scan(&events).Error
	cancel()
	if err != nil {
		return durable.Consumption{}, durable.ErrUnavailable
	}
	if len(events) == 0 {
		return durable.Consumption{Deferred: true}, nil
	}
	return w.Consume(ctx, events[0])
}
