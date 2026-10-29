package privacy

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// cleanGraph 只在已注销主体锁下发现普通清理边界，不把完整对端账号纳入删除集合。
type cleanGraph struct{ posts, comments, capsules, nicknames, relatedComments, wishes, applications, matches, conversations, messages, contents, tasks, assets, notifications, works, refs []string }

func collect(db *gorm.DB, id string) (g cleanGraph, err error) {
	queries := []struct {
		dst *[]string
		sql string
	}{
		{&g.wishes, `SELECT id FROM public.wishes WHERE owner_id=?`},
		{&g.applications, `SELECT id FROM public.applications WHERE applicant_id=? OR wish_id IN (SELECT id FROM public.wishes WHERE owner_id=?)`},
		{&g.matches, `SELECT id FROM public.matches WHERE requester_id=? OR helper_id=?`},
		{&g.conversations, `SELECT id FROM public.conversations WHERE match_id IN (SELECT id FROM public.matches WHERE requester_id=? OR helper_id=?)`},
		{&g.messages, `SELECT id FROM public.messages WHERE sender_id=? OR conversation_id IN (SELECT c.id FROM public.conversations c JOIN public.matches m ON m.id=c.match_id WHERE m.requester_id=? OR m.helper_id=?)`},
		{&g.assets, `SELECT id FROM public.assets WHERE owner_id=?`},
	}
	for _, q := range queries {
		var args []any
		for _, c := range q.sql {
			if c == '?' {
				args = append(args, id)
			}
		}
		if err = db.Raw(q.sql, args...).Scan(q.dst).Error; err != nil {
			return
		}
	}
	if err = collectWhispers(db, id, &g); err != nil {
		return
	}
	if err = db.Model(&entity.ContentRegistry{}).Where("author_id=? OR entity_id IN ? OR entity_id IN ? OR entity_id IN ?", id, g.wishes, g.applications, g.messages).Pluck("id", &g.contents).Error; err != nil {
		return
	}
	if err = db.Model(&entity.ModerationTask{}).Where("content_id IN ?", g.contents).Pluck("id", &g.tasks).Error; err != nil {
		return
	}
	g.refs = append(g.refs, id)
	for _, ids := range [][]string{g.wishes, g.applications, g.matches, g.conversations, g.messages, g.contents, g.tasks, g.assets, g.posts, g.comments, g.capsules, g.nicknames} {
		g.refs = append(g.refs, ids...)
	}
	for _, q := range []struct {
		model any
		where string
	}{
		{&entity.IdentityReview{}, "account_id=?"}, {&entity.Penalty{}, "account_id=?"}, {&entity.Appeal{}, "account_id=?"}, {&entity.Report{}, "reporter_id=?"},
	} {
		var ids []string
		if err = db.Model(q.model).Where(q.where, id).Pluck("id", &ids).Error; err != nil {
			return
		}
		g.refs = append(g.refs, ids...)
	}
	var reportIDs []string
	if err = db.Model(&entity.Report{}).Where("reporter_id=? OR content_id IN ? OR target_id IN ?", id, g.contents, g.refs).Pluck("id", &reportIDs).Error; err != nil {
		return
	}
	g.refs = append(g.refs, reportIDs...)
	if err = db.Model(&entity.CareCard{}).Where("recipient_id=?", id).Pluck("id", &g.notifications).Error; err != nil {
		return
	}
	g.refs = append(g.refs, g.notifications...)
	if err = db.Model(&entity.Notification{}).Where("recipient_id=? OR target_ref IN ? OR id IN (SELECT notification_id FROM public.whisper_notification_refs WHERE source_id IN ? OR target_id IN ? OR comment_id IN ?)", id, g.refs, g.refs, g.refs, g.refs).Pluck("id", &g.notifications).Error; err != nil {
		return
	}
	g.refs = append(g.refs, g.notifications...)
	if err = db.Model(&entity.WorkItem{}).Where("target_id IN ?", g.refs).Pluck("id", &g.works).Error; err != nil {
		return
	}
	g.refs = append(g.refs, g.works...)
	return
}

func deleteRows(db *gorm.DB, model any, query string, args ...any) error {
	return db.Where(query, args...).Delete(model).Error
}

// cleanAccount 按外键顺序删除普通聊天双方、版本、审计及身份占用。证据域不在此权限边界。
func cleanAccount(db *gorm.DB, id string) error {
	g, e := collect(db, id)
	if e != nil {
		return e
	}
	if e = db.Model(&entity.WhisperComment{}).Where("author_id<>? AND post_id IN ?", id, g.posts).Update("post_id", nil).Error; e != nil {
		return e
	}
	// 保留对端仍有效处罚，但移除已删普通内容引用；已限定证据不依赖该外键。
	if e = db.Model(&entity.Penalty{}).Where("account_id<>? AND content_id IN ?", id, g.contents).Update("content_id", nil).Error; e != nil {
		return e
	}
	steps := []struct {
		model any
		where string
		args  []any
	}{
		{&entity.DeliveryAttempt{}, "notification_id IN ?", []any{g.notifications}},
		{&entity.WhisperNotificationRef{}, "notification_id IN ?", []any{g.notifications}},
		{&entity.Notification{}, "id IN ?", []any{g.notifications}},
		{&entity.CareCard{}, "recipient_id=?", []any{id}},
		{&entity.AccessAudit{}, "subject_ref IN ?", []any{g.refs}},
		{&entity.ResourceFeedback{}, "account_id=?", []any{id}},
		{&entity.CommandReceipt{}, "(actor_kind='student' AND actor_id=?) OR result_ref IN ?", []any{id, g.refs}},
		{&entity.ConsumerReceipt{}, "operation_id IN (SELECT operation_id FROM public.work_items WHERE id IN ?)", []any{g.works}},
		{&entity.OutboxEvent{}, "work_id IN ? OR aggregate_id IN ?", []any{g.works, g.refs}},
		{&entity.WorkItem{}, "id IN ?", []any{g.works}},
		{&entity.Report{}, "reporter_id=? OR content_id IN ? OR target_id IN ?", []any{id, g.contents, g.refs}},
		{&entity.Appeal{}, "account_id=? OR penalty_id IN (SELECT id FROM public.penalties WHERE account_id=?)", []any{id, id}},
		{&entity.Penalty{}, "account_id=?", []any{id}},
		{&entity.ContentRestriction{}, "content_id IN ?", []any{g.contents}},
		{&entity.ModerationScan{}, "task_id IN ?", []any{g.tasks}},
		{&entity.Message{}, "id IN ?", []any{g.messages}},
		{&entity.Conversation{}, "id IN ?", []any{g.conversations}},
		{&entity.Match{}, "id IN ?", []any{g.matches}},
		{&entity.Application{}, "id IN ?", []any{g.applications}},
		{&entity.StoryVersion{}, "wish_id IN ?", []any{g.wishes}},
		{&entity.WishVersion{}, "wish_id IN ?", []any{g.wishes}},
		{&entity.Wish{}, "id IN ?", []any{g.wishes}},
		{&entity.WhisperHug{}, "actor_id=? OR post_id IN ?", []any{id, g.posts}},
		{&entity.WhisperAlias{}, "actor_id=? OR post_id IN ?", []any{id, g.posts}},
		{&entity.WhisperCommentVersion{}, "comment_id IN ?", []any{g.comments}},
		{&entity.WhisperComment{}, "id IN ?", []any{g.comments}},
		{&entity.WhisperPostVersion{}, "post_id IN ?", []any{g.posts}},
		{&entity.WhisperPost{}, "id IN ?", []any{g.posts}},
		{&entity.WhisperCapsule{}, "id IN ?", []any{g.capsules}},
		{&entity.WhisperPreference{}, "account_id=?", []any{id}},
		{&entity.WhisperNicknameVersion{}, "account_id=?", []any{id}},
		{&entity.ModerationTask{}, "id IN ?", []any{g.tasks}},
		{&entity.ContentRegistry{}, "id IN ?", []any{g.contents}},
		{&entity.PrivacyHold{}, "target_id IN ? OR target_id IN ? OR target_id IN ? OR target_id IN ? OR target_id IN ?", []any{g.wishes, g.posts, g.comments, g.capsules, g.nicknames}},
		{&entity.ShareLink{}, "target_id IN ?", []any{g.wishes}},
		{&entity.IdentityReview{}, "account_id=?", []any{id}},
		{&entity.Asset{}, "owner_id=?", []any{id}},
		{&entity.Block{}, "owner_id=? OR target_id=?", []any{id, id}},
		{&entity.Session{}, "account_id=?", []any{id}},
	}
	// 幸存故事先移除所有已删匹配的引用；保留稳定内容屏障，不随普通关系清理。
	if e = db.Model(&entity.StoryVersion{}).Where("source_match_id IN ?", g.matches).Updates(map[string]any{"source_match_id": nil, "peer_epoch": nil}).Error; e != nil {
		return e
	}
	for _, step := range steps {
		if e = deleteRows(db, step.model, step.where, step.args...); e != nil {
			return e
		}
	}
	// 所有该主体的清理工作先清空FK，避免账号物理删除留下回执关联。
	if e = db.Model(&entity.DeletionJob{}).Where("subject_ref=?", id).Update("subject_ref", nil).Error; e != nil {
		return e
	}
	return deleteRows(db, &entity.Account{}, "id=? AND lifecycle='deleting'", id)
}
func cleanEvidence(db *gorm.DB, id string) error {
	for _, step := range []struct {
		model any
		where string
	}{
		{&entity.CrisisDelivery{}, "case_id IN (SELECT id FROM evidence.crisis_cases WHERE evidence_case_id=?)"},
		{&entity.EvidenceItem{}, "case_id=?"}, {&entity.EvidenceAction{}, "case_id=?"}, {&entity.EvidenceAsset{}, "case_id=?"},
		{&entity.EvidenceCrisisCase{}, "evidence_case_id=?"}, {&entity.EvidenceCase{}, "id=?"},
	} {
		if e := deleteRows(db, step.model, step.where, id); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) objects(ctx context.Context, in Intent) (out []Object, err error) {
	if in.Scope == "account" {
		var rows []entity.Asset
		err = s.db.WithContext(ctx).Where("owner_id=?", in.Target).Find(&rows).Error
		for _, a := range rows {
			out = append(out, Object{ID: a.ID, KeyCipher: a.EncryptedObjectKey, Version: a.ObjectVersion})
		}
		if err != nil {
			return
		}
		// 把整张普通删除图的随机引用交给外部清理，覆盖幸存内容缓存、opaque回执和通知定位。
		graph, e := collect(s.db.WithContext(ctx), in.Target)
		if e != nil {
			return nil, e
		}
		seen := map[string]bool{}
		for _, v := range out {
			seen[v.ID] = true
		}
		for _, ref := range graph.refs {
			if !seen[ref] {
				out = append(out, Object{ID: ref, ReferenceOnly: true})
				seen[ref] = true
			}
		}
	} else {
		var rows []entity.EvidenceAsset
		err = s.db.WithContext(ctx).Where("case_id=?", in.Target).Find(&rows).Error
		for _, a := range rows {
			out = append(out, Object{ID: a.ID, KeyCipher: a.EncryptedObjectKey, Version: a.ObjectVersion, Evidence: true})
		}
	}
	return
}

// Reconcile 从独立日志重建丢失的受理事务。after为空开始扫描，返回空游标时该轮结束。
// 宿主循环推进游标，避免未完成的旧意图阻塞超过一百项的新请求；游标不作为权威持久状态。
func (s *Service) Reconcile(ctx context.Context, after string, limit int, scope string) (next string, err error) {
	defer s.done("intent.reconcile", "", time.Now(), &err)
	if scope != "account" && scope != "evidence" {
		return "", command.ErrInvalid
	}
	rows, e := s.journal.Pending(ctx, after, limit)
	if e != nil {
		return "", e
	}
	for _, in := range rows {
		next = in.ID
		if in.Scope != scope {
			continue
		}
		e = s.reconcileOne(ctx, in)
		if e != nil && err == nil {
			err = e
		}
	}
	if len(rows) < limit {
		next = ""
	}
	return
}
func (s *Service) reconcileOne(ctx context.Context, in Intent) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if in.Scope == "account" {
			if e := db.Exec("SELECT pg_advisory_xact_lock(624109174)").Error; e != nil {
				return e
			}
		}
		var exists int64
		model := any(&entity.Account{})
		if in.Scope == "evidence" {
			model = &entity.EvidenceCase{}
		}
		if e := db.Model(model).Where("id=?", in.Target).Count(&exists).Error; e != nil {
			return e
		}
		var plan command.Discovery
		var e error
		if exists > 0 {
			if in.Scope == "account" {
				plan, e = accountPlan(ctx, db, in.Target)
			} else {
				plan, e = evidencePlan(ctx, db, in.Target)
			}
			if e != nil {
				return e
			}
		}
		tx, e := command.LockPlan(ctx, db, plan, s.cfg.Command.LockTimeout)
		if e != nil {
			return e
		}
		if exists > 0 {
			var fresh command.Discovery
			if in.Scope == "account" {
				fresh, e = accountPlan(ctx, db, in.Target)
			} else {
				fresh, e = evidencePlan(ctx, db, in.Target)
			}
			if e != nil {
				return e
			}
			if !equalPlan(plan, fresh) {
				return command.ErrRetry
			}
		}
		var jobs []entity.DeletionJob
		if e = db.Where("job_id=?", in.ID).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&jobs).Error; e != nil {
			return e
		}
		if len(jobs) > 0 && exists > 0 && in.Scope == "account" && jobs[0].Phase != "completed" {
			// 恢复的未完成回执也不能使活动主体或新增对象跳过屏障。
			if e = activateAccount(ctx, tx, in.Target); e != nil {
				return e
			}
		}
		if len(jobs) > 0 {
			if jobs[0].Phase != "completed" || exists == 0 {
				return nil
			}
			// 已确认删除不能因恢复快照中残留completed回执跳过仍存在的目标。
			if in.Scope == "account" {
				e = activateAccount(ctx, tx, in.Target)
			} else {
				e = activateEvidence(ctx, db, in.Target)
			}
			if e != nil {
				return e
			}
			var subject *string
			if in.Scope == "account" {
				subject = &in.Target
			}
			return db.Model(&entity.DeletionJob{}).Where("job_id=?", in.ID).Updates(map[string]any{"phase": "blocked", "target_ref": in.Target, "subject_ref": subject, "next_at": gorm.Expr("clock_timestamp()"), "error_code": nil}).Error
		}
		if exists > 0 {
			if in.Scope == "account" {
				e = activateAccount(ctx, tx, in.Target)
			} else {
				e = activateEvidence(ctx, db, in.Target)
			}
			if e != nil {
				return e
			}
		}
		if in.Scope == "account" && exists == 0 {
			// 恢复较新快照时主体已不存在，仍由适配器核实外部副本；不能因查无账号提前完成。
			return db.Create(&entity.DeletionJob{JobID: in.ID, Scope: in.Scope, TargetRef: &in.Target, Phase: "blocked", ExpiresAt: time.Now().Add(s.cfg.ReceiptTTL)}).Error
		}
		return s.createJob(db, in)
	})
}

// ProcessNext 每次处理一个到期持久工作，外部清理先于普通行物理删除，失败保留所有重试定位。
// 并发执行依赖适配器按Intent.ID幂等；最终事务再取主体/资源锁和工作锁，不接受旧状态复活。
func (s *Service) ProcessNext(ctx context.Context, scope string) (processed bool, err error) {
	defer s.done("cleanup.next", "", time.Now(), &err)
	ctx, stop := context.WithTimeout(ctx, 3*s.cfg.Command.Timeout)
	defer stop()
	if scope != "account" && scope != "evidence" {
		return false, command.ErrInvalid
	}
	var job entity.DeletionJob
	bounded, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	e := s.db.WithContext(bounded).Where("scope=? AND phase<>'completed' AND next_at<=clock_timestamp()", scope).Order("next_at,job_id").Take(&job).Error
	cancel()
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if job.TargetRef == nil {
		return true, command.ErrUnavailable
	}
	processed = true
	in := Intent{ID: job.JobID, Scope: scope, Target: *job.TargetRef, SourceModule: job.SourceModule}
	if job.Phase == "verifying" {
		return true, s.finish(ctx, in.ID)
	}
	// 没有已持久意图绝不能执行破坏操作；工作只由受理或独立日志恢复创建。
	redactions, e := s.prepareRedactions(ctx, in)
	if e != nil {
		return true, s.deferJob(ctx, in.ID, "privacy_redaction_unavailable")
	}
	treeRedactions, e := s.prepareWhisperRedactions(ctx, in)
	if e != nil {
		return true, s.deferJob(ctx, in.ID, "privacy_redaction_unavailable")
	}
	objects, e := s.objects(ctx, in)
	if e == nil {
		if s.eraser == nil {
			e = command.ErrUnavailable
		} else {
			call, stop := context.WithTimeout(ctx, s.cfg.Command.Timeout)
			e = eraseSafely(call, s.eraser, in, objects)
			stop()
		}
	}
	if e != nil {
		return true, s.deferJob(ctx, in.ID, "external_cleanup_unavailable")
	}
	bounded, cancel = context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(bounded).Transaction(func(db *gorm.DB) error {
		if scope == "account" {
			if e := db.Exec("SELECT pg_advisory_xact_lock(624109174)").Error; e != nil {
				return e
			}
		}
		var plan command.Discovery
		var e error
		var exists int64
		model := any(&entity.Account{})
		if scope == "evidence" {
			model = &entity.EvidenceCase{}
		}
		if e = db.Model(model).Where("id=?", in.Target).Count(&exists).Error; e != nil {
			return e
		}
		if exists > 0 {
			if scope == "account" {
				plan, e = accountPlan(bounded, db, in.Target)
			} else {
				plan, e = evidencePlan(bounded, db, in.Target)
			}
			if e != nil {
				return e
			}
		}
		_, e = command.LockPlan(bounded, db, plan, s.cfg.Command.LockTimeout)
		if e != nil {
			return e
		}
		if exists > 0 {
			var fresh command.Discovery
			if scope == "account" {
				fresh, e = accountPlan(bounded, db, in.Target)
			} else {
				fresh, e = evidencePlan(bounded, db, in.Target)
			}
			if e != nil {
				return e
			}
			if !equalPlan(plan, fresh) {
				return command.ErrRetry
			}
		}
		var current entity.DeletionJob
		if e = db.Where("job_id=?", in.ID).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current).Error; e != nil {
			return e
		}
		if current.Phase == "completed" {
			return nil
		}
		if current.TargetRef == nil || *current.TargetRef != in.Target {
			return command.ErrConflict
		}
		// 防止恢复程序或错误装配把仍活动主体当作可删目标。
		if exists > 0 {
			if scope == "account" {
				var a entity.Account
				if e = db.Where("id=? AND lifecycle='deleting'", in.Target).Take(&a).Error; e != nil {
					return e
				}
				if e = s.applyRedactions(bounded, db, redactions); e != nil {
					return e
				}
				if e = s.applyWhisperRedactions(bounded, db, treeRedactions); e != nil {
					return e
				}
				e = cleanAccount(db, in.Target)
			} else {
				var ec entity.EvidenceCase
				if e = db.Where("id=? AND status='deletion_pending'", in.Target).Take(&ec).Error; e != nil {
					return e
				}
				e = cleanEvidence(db, in.Target)
			}
			if e != nil {
				return e
			}
		}
		return db.Model(&entity.DeletionJob{}).Where("job_id=?", in.ID).Updates(map[string]any{"phase": "verifying", "subject_ref": nil, "error_code": nil, "remaining_counts": emptyJSON()}).Error
	})
	if err != nil {
		_ = s.deferJob(ctx, in.ID, "database_cleanup_unavailable")
		return true, err
	}
	err = s.finish(ctx, in.ID)
	return
}
func (s *Service) finish(ctx context.Context, id string) error {
	// 先确认恢复日志，再清空业务目标；两者之间崩溃由verifying工作继续，不提前报告完成。
	if e := s.journal.Confirm(ctx, id); e != nil {
		return s.deferJob(ctx, id, "journal_confirmation_unavailable")
	}
	bounded, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(bounded).Model(&entity.DeletionJob{}).Where("job_id=? AND phase='verifying'", id).Updates(map[string]any{"phase": "completed", "target_ref": nil, "error_code": nil}).Error
}

func eraseSafely(ctx context.Context, e Eraser, in Intent, objects []Object) (err error) {
	defer func() {
		if recover() != nil {
			err = command.ErrUnavailable
		}
	}()
	if err = e.Erase(ctx, in, objects); err == nil {
		err = ctx.Err()
	}
	return
}
func (s *Service) deferJob(ctx context.Context, id, code string) error {
	bounded, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	if e := s.db.WithContext(bounded).Model(&entity.DeletionJob{}).Where("job_id=? AND phase<>'completed'", id).Updates(map[string]any{"error_code": code, "attempts": gorm.Expr("attempts+1"), "next_at": time.Now().Add(s.cfg.RetryDelay)}).Error; e != nil {
		return e
	}
	return command.ErrUnavailable
}

// Run 显式启动串行有界清理；取消后等待在途操作返回，宿主随后才能释放连接池。
func (s *Service) Run(ctx context.Context, scope string, interval time.Duration) (err error) {
	defer s.done("worker.run", "", time.Now(), &err)
	if interval < time.Millisecond || (scope != "account" && scope != "evidence") {
		return command.ErrInvalid
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	after := ""
	for {
		if ctx.Err() != nil {
			return nil
		}
		next, e := s.Reconcile(ctx, after, 100, scope)
		if e == nil || next != "" {
			after = next
		}
		_, _ = s.ProcessNext(ctx, scope)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Recover 仅供隔离恢复调度读取全部日志（含已完成删除）并重建屏障/清理工作。
// 返回游标仅表示扫描位置；不自动开放网络、消费者或清除恢复日志，也不代替全副本核验。
func (s *Service) Recover(ctx context.Context, after string, limit int, scope string) (next string, err error) {
	defer s.done("recovery.reapply", "", time.Now(), &err)
	if scope != "account" && scope != "evidence" {
		return "", command.ErrInvalid
	}
	reader, ok := s.journal.(interface {
		AllIntents(context.Context, string, int) ([]Intent, error)
	})
	if !ok {
		return "", command.ErrUnavailable
	}
	rows, e := reader.AllIntents(ctx, after, limit)
	if e != nil {
		return "", e
	}
	for _, in := range rows {
		next = in.ID
		if in.Scope != scope {
			continue
		}
		if e = s.reconcileOne(ctx, in); e != nil && err == nil {
			err = e
		}
	}
	if len(rows) < limit {
		next = ""
	}
	return
}
