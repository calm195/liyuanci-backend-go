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

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/contentreview"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// Assessment 是安全处理器审查组合信息后产生的脱敏候选，未复核前不可公开。
type Assessment struct {
	Title                                 string
	RewardText                            string
	Body                                  string
	Tags                                  wish.IdentityTags
	SchoolRelated, OrdinaryHit, CrisisHit bool
	DetectorVersion                       string
	wordlistEvaluation                    *WordlistEvaluation
	wordlistEvidence                      *WordlistEvidence
}

// Analyzer 对词库命中或需重审内容生成原文人工复核候选，允许重复调用且不能产生业务写入。
// 本模块没有默认放行实现；缺少复核策略时构造失败，处理故障保持待审。
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
	// Care 是站内关怀资料；没有外部危机通知服务。
	Care            *CareResources
	MessageAnalyzer MessageAnalyzer
	Consumer        durable.ConsumerConfig
	AnalysisTimeout time.Duration
}

// Worker 不拥有数据库、密钥或安全分析供应商；按持久事件和数据库任务执行。
type Worker struct {
	// whispersOnly 将共享危机投递按来源隔离，避免不同进程抢占彼此的待办。
	whispersOnly bool
	db           *gorm.DB
	codec        *securedata.Codec
	analyzer     Analyzer
	logger       *zap.Logger
	cfg          WorkerConfig
	wordlists    wordlistCache
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
	w := &Worker{
		db:       db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}),
		logger:   log,
		codec:    codec,
		analyzer: analyzer,
		cfg:      cfg,
	}
	if _, e := w.consumer(nil, nil, nil); e != nil {
		return nil, e
	}
	return w, nil
}

type rules struct {
	customRules                                   map[string][]WordlistRule
	crisis, school, ordinary, builtin             entity.WordlistVersion
	builtinEntries                                []builtinRule
	matcher                                       *wordlistMatchers
	crisisEntries, schoolEntries, ordinaryEntries []string
}

func readRules(db *gorm.DB) (rules, error) {
	return readCachedRules(db, nil)
}

func readCachedRules(db *gorm.DB, cache *wordlistCache) (out rules, err error) {
	for _, item := range []struct {
		kind string
		row  *entity.WordlistVersion
	}{
		{"crisis", &out.crisis},
		{"school", &out.school},
		{"ordinary_violation", &out.ordinary},
		{builtinKind, &out.builtin},
	} {
		var head entity.WordlistHead
		if err = db.Where("kind=?", item.kind).Take(&head).Error; err != nil {
			return
		}
		if head.EffectiveID == nil {
			// 完整服务在启动检查中要求内置基线。旧的独立领域测试可只初始化自定义词库。
			if item.kind == builtinKind {
				continue
			}
			return out, command.ErrUnavailable
		}
		if err = db.Where("id=? AND type=? AND status='published'", *head.EffectiveID, item.kind).
			Take(item.row).Error; err != nil {
			return
		}
	}
	if cache != nil {
		out.matcher = cache.lookup(out)
		if out.matcher != nil {
			return
		}
	}
	for _, item := range []struct {
		id      string
		entries *[]string
	}{
		{out.crisis.ID, &out.crisisEntries},
		{out.school.ID, &out.schoolEntries},
		{out.ordinary.ID, &out.ordinaryEntries},
	} {
		*item.entries, err = enabledEntries(db, item.id)
		if err != nil {
			return
		}
	}
	out.customRules = map[string][]WordlistRule{}
	for _, item := range []struct{ kind, id string }{{"crisis", out.crisis.ID}, {"school", out.school.ID}, {"ordinary_violation", out.ordinary.ID}} {
		out.customRules[item.kind], err = readCustomRules(db, item.id)
		if err != nil {
			return
		}
	}
	if out.builtin.ID != "" {
		out.builtinEntries, err = builtinRules(db, out.builtin.ID)
	}
	return
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
	if row.comment != nil {
		out.Body, err = wish.DecryptWishComment(ctx, codec, row.commentVersion)
		return
	}
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
		data, e := codec.Decrypt(
			ctx,
			securedata.Binding{
				Purpose:  "application_content",
				ObjectID: row.application.ID,
				Version:  uint64(row.application.ContentVersion),
			},
			envelope,
		)
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
	plain, e := codec.Decrypt(
		ctx,
		securedata.Binding{Purpose: "wish_content", ObjectID: row.wish.ID, Version: uint64(row.task.ContentVersion)},
		envelope,
	)
	defer clear(plain)
	if e != nil || json.Unmarshal(plain, &out) != nil {
		return wish.Content{}, command.ErrUnavailable
	}
	if out.Title == "" {
		out.Title = "未命名心愿"
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
		w.logger.Info("心愿相关内容扫描完成",
			zap.String("result", result),
			zap.Duration("duration", time.Since(start)),
			zap.Bool("applied", out.Applied),
			zap.Bool("cancelled", out.Cancelled),
			zap.Bool("replayed", out.Replayed),
		)
	}()
	if !validID(event.EventID) || !validID(event.CorrelationID) || !validID(event.AggregateID) ||
		event.EventType != contentreview.Route().EventType ||
		event.AggregateVersion < 1 || event.CreatedAt.IsZero() {
		return out, durable.ErrInvalid
	}
	// 分析及密钥调用均受总预算约束，直接库调用也不能因无上层截止时间而无限等待。
	overall, stopOverall := context.WithTimeout(ctx, 2*w.cfg.Consumer.Timeout+w.cfg.AnalysisTimeout)
	defer stopOverall()
	ctx = overall
	// 第一阶段只读准备，不在供应商调用期间持有业务行锁。
	prepared, prep := w.prepareScan(ctx, event)
	source := prepared.source
	if !prepared.valid {
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
	// 第二阶段分析和加密；第三阶段由消费者加锁并复核后统一落库。
	var scan *entity.ModerationScan
	var assessment *Assessment
	if prep == nil && prepared.hasInput {
		scan, assessment, prep = w.analyzeScan(ctx, source, prepared.snapshot, prepared.input)
	}
	consumer, e := w.consumer(scan, assessment, prep)
	if e != nil {
		return out, e
	}
	return consumer.Consume(ctx, event)
}

// scanPreparation 保存只读快照及两个独立门槛：事件匹配、内容可供分析。
type scanPreparation struct {
	source   current
	snapshot rules
	input    wish.Content
	valid    bool
	hasInput bool
}

// prepareScan 在只读一致性快照中复核事件和任务，读取词库并解密不可变内容。
// out.valid 只表示事件与持久工作匹配；没有可分析内容时仍交给消费者复核过期或终态任务。
func (w *Worker) prepareScan(ctx context.Context, event durable.Event) (out scanPreparation, err error) {
	var work entity.WorkItem
	bounded, cancel := context.WithTimeout(ctx, w.cfg.Consumer.Timeout)
	defer cancel()
	err = w.db.WithContext(bounded).Transaction(func(db *gorm.DB) error {
		var n int64
		if e := db.Model(&entity.OutboxEvent{}).
			Where("event_id=? AND work_id=? AND aggregate_id=? AND aggregate_version=? AND event_type=? AND created_at=? AND superseded_at IS NULL", event.EventID, event.CorrelationID, event.AggregateID, event.AggregateVersion, event.EventType, event.CreatedAt).
			Count(&n).
			Error; e != nil {
			return e
		}
		if n != 1 {
			return nil
		}
		if e := db.Where(
			"id=? AND kind=? AND target_kind=? AND target_id=?",
			event.CorrelationID,
			contentreview.Route().Kind,
			contentreview.Route().TargetKind,
			event.AggregateID,
		).
			Take(&work).Error; e != nil {
			return e
		}
		out.valid = true
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		// 终态、未到期和租约仍有效的工作不重复调用分析供应商。
		if work.Status == "completed" || work.Status == "cancelled" || work.Status == "attention" ||
			work.NextAt.After(now) ||
			work.Status == "leased" && work.LeaseUntil != nil && work.LeaseUntil.After(now) {
			return nil
		}
		var e error
		out.source, e = load(db, event.AggregateID)
		if errors.Is(e, command.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		ok, e := eligible(db, out.source)
		if e != nil {
			return e
		}
		if !ok || out.source.task.Status != "pending" {
			return nil
		}
		out.snapshot, e = readCachedRules(db, &w.wordlists)
		if e != nil {
			return e
		}
		out.input, e = decryptContent(db.Statement.Context, w.codec, out.source)
		out.hasInput = e == nil
		return e
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return
}

// analyzeScan 在事务外执行安全分析并加密候选投影；异常或非法结果继续失败关闭。
func (w *Worker) analyzeScan(ctx context.Context, source current, snapshot rules, input wish.Content) (*entity.ModerationScan, *Assessment, error) {
	matcher, err := w.wordlists.compile(snapshot)
	if err != nil {
		return nil, nil, durable.ErrTemporary
	}
	snapshot.matcher = matcher
	evidence := snapshot.evidence()
	analysis, stop := context.WithTimeout(ctx, w.cfg.AnalysisTimeout)
	a, e := func() (a Assessment, e error) {
		defer func() {
			if recover() != nil {
				e = durable.ErrConfiguration
			}
		}()
		return w.analyze(analysis, source, snapshot, input)
	}()
	if e == nil {
		e = analysis.Err()
	}
	stop()
	if e != nil {
		return nil, nil, durable.ErrTemporary
	}
	evidence.add("body", a.wordlistEvaluation)
	if source.application == nil && source.message == nil && source.story == nil && source.comment == nil {
		for _, field := range []struct {
			value  string
			name   string
			target *string
			limit  int
		}{{input.Title, "title", &a.Title, 40}, {input.RewardText, "reward_text", &a.RewardText, 100}} {
			if field.value == "" {
				continue
			}
			part := input
			part.Body = field.value
			part.Title = ""
			part.RewardText = ""
			result, failure := w.analyze(ctx, source, snapshot, part)
			if failure != nil {
				return nil, nil, durable.ErrTemporary
			}
			if !text(result.Body, field.limit) {
				return nil, nil, durable.ErrConfiguration
			}
			*field.target = result.Body
			evidence.add(field.name, result.wordlistEvaluation)
			a.CrisisHit = a.CrisisHit || result.CrisisHit
			a.SchoolRelated = a.SchoolRelated || result.SchoolRelated
			a.OrdinaryHit = a.OrdinaryHit || result.OrdinaryHit
		}
	}
	// 候选不能扩大身份标签范围，非法正文或缺少检测版本也不能进入写入阶段。
	if !candidateBody(source, a.Body) || !text(a.DetectorVersion, 80) || !subset(a.Tags, input.IdentityTags) {
		return nil, nil, durable.ErrConfiguration
	}

	candidate, _ := json.Marshal(reviewProjection{Body: a.Body, Tags: a.Tags, Title: a.Title, RewardText: a.RewardText, WordlistEvidence: evidence})
	defer clear(candidate)
	envelope, e := w.codec.Encrypt(ctx, securedata.Binding{
		Purpose:  "moderation_projection",
		ObjectID: source.task.ID,
		Version:  1,
	}, candidate)
	if e != nil {
		return nil, nil, durable.ErrTemporary
	}
	cipher, e := json.Marshal(envelope)
	if e != nil {
		return nil, nil, durable.ErrConfiguration
	}
	a.wordlistEvidence = evidence
	return &entity.ModerationScan{
		TaskID:           source.task.ID,
		CrisisVersion:    snapshot.crisis.ID,
		SchoolVersion:    snapshot.school.ID,
		OrdinaryVersion:  snapshot.ordinary.ID,
		DetectorVersion:  a.DetectorVersion,
		SchoolRelated:    a.SchoolRelated,
		OrdinaryHit:      a.OrdinaryHit,
		CrisisHit:        a.CrisisHit,
		ProjectionCipher: cipher,
	}, &a, nil
}

// consumer 只装配持久消费者；写入阶段统一在消费者提供的事务内执行。
func (w *Worker) consumer(scan *entity.ModerationScan, assessment *Assessment, prepare error) (*durable.Consumer, error) {
	return durable.NewConsumer(w.db, w.logger, durable.ConsumerDefinition{
		ConsumerKind: "wish_review.scan",
		Route:        contentreview.Route(),
		Discover: func(ctx context.Context, db *gorm.DB, work durable.Work) (command.Discovery, error) {
			return discover(ctx, db, work.TargetID)
		},
		Apply: func(ctx context.Context, tx *command.Tx, work durable.Work) (durable.Decision, error) {
			return w.applyScan(ctx, tx, work, scan, assessment, prepare)
		},
	}, w.cfg.Consumer)
}

// applyScan 在统一锁序下重新核对当前版本，再原子写入扫描、业务投影和审核任务。
func (w *Worker) applyScan(ctx context.Context, tx *command.Tx, work durable.Work, scan *entity.ModerationScan, a *Assessment, prepare error) (durable.Decision, error) {
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
		if e := cancelObsoleteScan(ctx, db, row, ok); e != nil {
			return "", e
		}
		return durable.Obsolete, nil
	}
	// 先处理失效目标，再解释事务外准备错误，避免旧任务继续消耗重试。
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
	// 危机优先隔离；仅无危机、涉校和普通命中的候选可自动批准。
	status := "human_required"
	decision := any(nil)
	if e := writeScanDetection(ctx, db, row, scan, a); e != nil {
		return "", e
	}
	if a.CrisisHit {
		if _, e = captureCrisis(ctx, db, w.codec, row, tx.Now(), a.wordlistEvidence); e != nil {
			return "", e
		}
	} else if !a.SchoolRelated && !a.OrdinaryHit {
		status = "approved"
		decision = "approved"
		tags, _ := json.Marshal(a.Tags)
		if e = setReview(db, w.codec, row, "approved", &a.Body, tags, reviewProjection{Title: a.Title, RewardText: a.RewardText}); e != nil {
			return "", e
		}
	}
	if status == "human_required" {
		if e := markScanHumanRequired(ctx, db, row); e != nil {
			return "", e
		}
	}
	if e = db.Model(&entity.ModerationTask{}).
		Where("id=?", row.task.ID).
		Updates(map[string]any{
			"status":   status,
			"decision": decision,
			"version":  gorm.Expr("version+1"),
		}).Error; e != nil {
		return "", e
	}
	if status == "approved" && row.story == nil && row.application == nil && row.message == nil && row.comment == nil {
		if _, e = wish.ApplyApprovedPublication(ctx, db, row.wish.ID, row.task.ContentVersion); e != nil {
			return "", e
		}
	}
	return durable.Complete, nil
}

// ProcessNext 从数据库扫描一条当前有效事件，恢复中断或到期的持久任务，不重建工作。
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
		w.logger.Info("心愿审核数据库补偿完成",
			zap.String("result", result),
			zap.Duration("duration", time.Since(start)),
			zap.Bool("applied", out.Applied),
			zap.Bool("cancelled", out.Cancelled),
		)
	}()

	bounded, cancel := context.WithTimeout(ctx, w.cfg.Consumer.Timeout)
	defer cancel()
	var events []durable.Event
	err = w.db.WithContext(bounded).Raw(`SELECT e.event_id, e.event_type, e.aggregate_id, e.aggregate_version,
       e.work_id AS correlation_id, e.created_at
FROM public.work_items w
JOIN public.outbox_events e ON e.work_id = w.id AND e.superseded_at IS NULL
WHERE w.kind = ? AND w.target_kind = ? AND e.event_type = ?
  AND w.status IN ('pending', 'retry_wait', 'leased')
  AND w.next_at <= clock_timestamp()
  AND (w.status <> 'leased' OR w.lease_until <= clock_timestamp())
  AND NOT EXISTS (
      SELECT 1 FROM public.moderation_tasks t
      JOIN public.content_registry c ON c.id = t.content_id
      WHERE t.id = w.target_id AND t.privacy_recheck AND c.kind = 'story'
        AND EXISTS (
            SELECT 1 FROM public.privacy_holds h
            WHERE h.target_kind = 'wish' AND h.target_id = c.entity_id AND h.status = 'active'
        )
  )
ORDER BY w.next_at, w.id
LIMIT 1`, contentreview.Route().Kind, contentreview.Route().TargetKind, contentreview.Route().EventType).Scan(&events).Error
	cancel()
	if err != nil {
		return durable.Consumption{}, durable.ErrUnavailable
	}
	if len(events) == 0 {
		return durable.Consumption{Deferred: true}, nil
	}
	return w.Consume(ctx, events[0])
}

// cancelObsoleteScan 清理失效目标；故事和私聊仅在资格失效时取消。
// 应愿留言在版本过期或任务终态时也沿用取消语义，不能只按资格判断。
func cancelObsoleteScan(ctx context.Context, db *gorm.DB, row current, isEligible bool) error {
	if row.comment != nil && !isEligible {
		return db.Model(&entity.WishComment{}).Where("id=? AND status IN('pending_scan','pending_review')", row.comment.ID).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error
	}
	if row.story != nil && !isEligible {
		if e := db.Model(&entity.StoryVersion{}).
			Where("wish_id=? AND story_version=? AND moderation_status IN ?", row.wish.ID, row.story.StoryVersion, []string{"pending", "human_required"}).
			Updates(map[string]any{"moderation_status": "cancelled", "public_body": nil}).Error; e != nil {
			return e
		}
		if e := db.Model(&entity.ModerationTask{}).
			Where("id=? AND status='pending'", row.task.ID).
			Updates(map[string]any{"status": "obsolete", "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
	}

	if row.message != nil && !isEligible {
		if e := chat.CancelMessage(ctx, db, *row.message); e != nil {
			return e
		}
	}
	if row.application != nil &&
		(row.application.Status == "pending_scan" || row.application.Status == "pending_review" || row.application.Status == "active") {
		if e := wish.CancelApplication(ctx, db, *row.application, "invalidated", "eligibility_changed"); e != nil {
			return e
		}
	}
	return nil
}

// writeScanDetection 同步内容索引与公开版本的涉校检测标记，并记录实际使用的普通词库版本。
func writeScanDetection(ctx context.Context, db *gorm.DB, row current, scan *entity.ModerationScan, a *Assessment) error {
	db = db.WithContext(ctx)
	detection := a.DetectorVersion + "/" + scan.SchoolVersion
	models := []any{&entity.ContentRegistry{}}
	if row.story != nil {
		models = append(models, &entity.StoryVersion{})
	}
	if row.application == nil && row.message == nil && row.story == nil && row.comment == nil {
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
		if e := q.
			Updates(map[string]any{
				"school_related":           a.SchoolRelated,
				"school_detection_state":   "completed",
				"school_detection_version": detection,
			}).Error; e != nil {
			return e
		}
	}
	if row.story != nil {
		if e := db.Model(&entity.StoryVersion{}).
			Where("wish_id=? AND story_version=?", row.wish.ID, row.task.ContentVersion).
			Update("wordlist_version", scan.OrdinaryVersion).Error; e != nil {
			return e
		}
	}
	if row.application == nil && row.message == nil && row.story == nil && row.comment == nil {
		if e := db.Model(&entity.WishVersion{}).
			Where("wish_id=? AND content_version=?", row.wish.ID, row.task.ContentVersion).
			Update("wordlist_version", scan.OrdinaryVersion).Error; e != nil {
			return e
		}
	}
	return nil
}

// markScanHumanRequired 将不同内容目标投影到各自的人工待审状态。
func markScanHumanRequired(ctx context.Context, db *gorm.DB, row current) error {
	if row.comment != nil {
		return db.WithContext(ctx).Model(&entity.WishComment{}).Where("id=?", row.comment.ID).Updates(map[string]any{"status": "pending_review", "version": gorm.Expr("version+1")}).Error
	}
	db = db.WithContext(ctx)
	if row.story != nil {
		if e := db.Model(&entity.StoryVersion{}).
			Where("wish_id=? AND story_version=?", row.wish.ID, row.task.ContentVersion).
			Update("moderation_status", "human_required").Error; e != nil {
			return e
		}
	}
	if row.message != nil {
		if e := db.Model(&entity.Message{}).
			Where("id=?", row.message.ID).
			Updates(map[string]any{"status": "pending_review", "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
	}
	if row.application != nil {
		if e := db.Model(&entity.Application{}).
			Where("id=?", row.application.ID).
			Updates(map[string]any{"status": "pending_review", "version": gorm.Expr("version+1")}).Error; e != nil {
			return e
		}
	}
	return nil
}
