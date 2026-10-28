package whisper_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// testWhisperCapsules 验证B06实际角色、加密、私密审核和持久解锁；时间推进用显式SQL夹具。
func testWhisperCapsules(t *testing.T, admin, workerDB *gorm.DB, s *whisper.Service, w *moderation.WhisperWorker, a *reviewAnalyzer, reviews, crises *moderation.Service, owner, token, otherToken, adminToken string) {
	t.Helper()
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	unlock, e := whisper.NewCapsuleUnlockWorker(workerDB, zap.NewNop(), durable.ConsumerConfig{Timeout: 5 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3})
	must(e)
	router := httpapi.New(zap.NewNop(), nil)
	must(httpapi.RegisterWhisperCapsules(router, s))
	samples := []map[string]any{}
	request := func(op, method, path, tok string, body any, status int, key ...string) json.RawMessage {
		t.Helper()
		var b []byte
		if v, ok := body.(string); ok {
			b = []byte(v)
		} else if body != nil {
			b, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(b))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		req.Header.Set("Content-Type", "application/json")
		k := uuid.NewString()
		if len(key) > 0 {
			k = key[0]
		}
		req.Header.Set("Idempotency-Key", k)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s status=%d want=%d: %s", op, rec.Code, status, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Pragma") != "no-cache" {
			t.Fatal("未禁止缓存")
		}
		var response any
		must(json.Unmarshal(rec.Body.Bytes(), &response))
		samples = append(samples, map[string]any{"operation_id": op, "status": status, "response": response})
		var out struct {
			Data json.RawMessage `json:"data"`
		}
		must(json.Unmarshal(rec.Body.Bytes(), &out))
		return out.Data
	}
	now := func() time.Time {
		var at time.Time
		must(admin.Raw("SELECT date_trunc('second',clock_timestamp())").Scan(&at).Error)
		return at.UTC()
	}
	create := func(body string) whisper.Capsule {
		v, e := s.CreateCapsule(ctx, token, uuid.NewString(), "", whisper.CapsuleInput{Body: body, UnlockAt: now().Add(120 * time.Second), SafetyNoticeAccepted: true})
		must(e)
		if v.Body != nil {
			t.Fatal("创建泄漏正文")
		}
		return v
	}
	event := func(id, kind string) durable.Event {
		var ev durable.Event
		q := `SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.work_items w ON w.id=e.work_id WHERE w.kind='whisper_capsule_unlock' AND w.target_id=? AND e.superseded_at IS NULL`
		if kind == "review" {
			q = `SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.entity_id=? AND e.superseded_at IS NULL ORDER BY t.created_at DESC LIMIT 1`
		}
		must(admin.Raw(q, id).Scan(&ev).Error)
		if ev.EventID == "" {
			t.Fatal("未建立工作", id, kind)
		}
		return ev
	}
	scan := func(id string) { _, e := w.Consume(ctx, event(id, "review")); must(e) }
	due := func(id string) {
		must(admin.Exec("UPDATE public.whisper_capsules SET unlock_at=date_trunc('second',clock_timestamp())-interval '1 second' WHERE id=?", id).Error)
		must(admin.Exec("UPDATE public.work_items SET next_at=clock_timestamp()-interval '1 second' WHERE kind='whisper_capsule_unlock' AND target_id=?", id).Error)
		must(admin.Exec("UPDATE public.outbox_events SET next_at=clock_timestamp()-interval '1 second' WHERE work_id IN (SELECT id FROM public.work_items WHERE kind='whisper_capsule_unlock' AND target_id=?)", id).Error)
	}
	current := func(id string) whisper.Capsule { v, e := s.GetCapsule(ctx, token, "", id); must(e); return v }
	countNotices := func(id string) int64 {
		var n int64
		must(admin.Model(&entity.WhisperNotificationRef{}).Where("kind='whisper_capsule_unlocked' AND source_id=?", id).Count(&n).Error)
		return n
	}
	t.Run("五接口及正文只在到期详情读取", func(t *testing.T) {
		input := whisper.CapsuleInput{Body: "合成人名 的私密信件", UnlockAt: now().Add(120 * time.Second), SafetyNoticeAccepted: true}
		key := uuid.NewString()
		raw := request("whisperCapsuleCreate", "POST", "/me/whispers/capsules", token, input, 202, key)
		var c whisper.Capsule
		must(json.Unmarshal(raw, &c))
		if c.Body != nil || c.Status != "scanning" {
			t.Fatal(c)
		}
		if v := current(c.ID); v.Body != nil {
			t.Fatal("扫描中可读")
		}
		early, e := unlock.Consume(ctx, event(c.ID, "unlock"))
		must(e)
		if !early.Deferred {
			t.Fatal(early)
		}
		scan(c.ID)
		locked := current(c.ID)
		if locked.Status != "locked" || locked.Body != nil {
			t.Fatal(locked)
		}
		due(c.ID)
		raw = request("whisperCapsuleGet", "GET", "/me/whispers/capsules/"+c.ID, token, nil, 200)
		var opened whisper.Capsule
		must(json.Unmarshal(raw, &opened))
		if opened.Status != "unlocked" || opened.Body == nil || *opened.Body != input.Body || opened.Version != locked.Version {
			t.Fatal(opened)
		}
		raw = request("whisperCapsulesList", "GET", "/me/whispers/capsules", token, nil, 200)
		var page whisper.Page[whisper.Capsule]
		must(json.Unmarshal(raw, &page))
		for _, v := range page.Items {
			if v.Body != nil {
				t.Fatal("列表泄漏正文")
			}
		}
		raw = request("whisperCapsuleCreate", "POST", "/me/whispers/capsules", token, input, 202, key)
		must(json.Unmarshal(raw, &c))
		if c.Status != "unlocked" || c.Body != nil {
			t.Fatal("创建重放泄漏正文")
		}
		ev := event(c.ID, "unlock")
		r, e := unlock.Consume(ctx, ev)
		must(e)
		if !r.Applied || countNotices(c.ID) != 1 {
			t.Fatal(r)
		}
		r, e = unlock.Consume(ctx, ev)
		must(e)
		if !r.Replayed || countNotices(c.ID) != 1 {
			t.Fatal(r)
		}
		raw = request("whisperCapsuleDelete", "POST", "/me/whispers/capsules/"+c.ID+"/delete", token, map[string]any{"expected_version": c.Version}, 200)
		must(json.Unmarshal(raw, &c))
		if c.Status != "deleted" || c.Body != nil || countNotices(c.ID) != 0 {
			t.Fatal(c)
		}
		r, e = unlock.Consume(ctx, ev)
		must(e)
		if r.Applied || countNotices(c.ID) != 0 {
			t.Fatal("删除后旧工作补发")
		}
		c = create("未来取消信件")
		request("whisperCapsuleCancel", "POST", "/me/whispers/capsules/"+c.ID+"/cancel", token, map[string]any{"expected_version": c.Version}, 200)
		request("whisperCapsuleGet", "GET", "/me/whispers/capsules/"+c.ID, otherToken, nil, 404)
		request("whisperCapsulesList", "GET", "/me/whispers/capsules", "", nil, 401)
		request("whisperCapsuleCreate", "POST", "/me/whispers/capsules", token, `{"body":"正文","unlock_at":"2027-01-01T00:00:00Z","safety_notice_accepted":false}`, 400)
		request("whisperCapsuleCreate", "POST", "/me/whispers/capsules", token, whisper.CapsuleInput{Body: "过早", UnlockAt: now().Add(59 * time.Second), SafetyNoticeAccepted: true}, 400)
	})
	t.Run("审核挂起迟到批准与补偿", func(t *testing.T) {
		c := create("普通命中 的私密原稿")
		scan(c.ID)
		due(c.ID)
		ev := event(c.ID, "unlock")
		r, e := unlock.Consume(ctx, ev)
		must(e)
		if !r.Deferred || current(c.ID).Body != nil {
			t.Fatal(r)
		}
		var work entity.WorkItem
		must(admin.Where("id=?", ev.CorrelationID).Take(&work).Error)
		if work.Status != "waiting" || work.Attempts != 0 {
			t.Fatal(work.Status, work.Attempts)
		}
		r, e = unlock.ProcessNext(ctx)
		must(e)
		if r.Applied {
			t.Fatal("未审工作忙轮询")
		}
		var task entity.ModerationTask
		must(admin.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.entity_id=?", c.ID).Take(&task).Error)
		_, e = reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", task.ID, moderation.WhisperReviewCommand{ExpectedVersion: task.Version, ExpectedContentVersion: 1, Decision: "approved"})
		must(e)
		if v := current(c.ID); v.Status != "unlocked" || v.Body == nil || *v.Body != "普通命中 的私密原稿" {
			t.Fatal(v)
		}
		must(admin.Where("id=?", ev.CorrelationID).Take(&work).Error)
		if work.Status != "pending" {
			t.Fatal("人工批准未唤醒")
		}
		r, e = unlock.ProcessNext(ctx)
		must(e)
		if !r.Applied || countNotices(c.ID) != 1 {
			t.Fatal(r)
		}
		// 模拟批准唤醒丢失，数据库有界补偿仍可处理waiting且已批准的对象。
		second := create("补偿通知")
		scan(second.ID)
		due(second.ID)
		must(admin.Exec("UPDATE public.work_items SET status='waiting' WHERE kind='whisper_capsule_unlock' AND target_id=?", second.ID).Error)
		r, e = unlock.ProcessNext(ctx)
		must(e)
		if !r.Applied || countNotices(second.ID) != 1 {
			t.Fatal(r)
		}
	})
	t.Run("取消竞争及受罚本人读取", func(t *testing.T) {
		c := create("取消竞争")
		reviewEvent := event(c.ID, "review")
		wake := event(c.ID, "unlock")
		a.hook = func() { _, e := s.CloseCapsule(ctx, token, uuid.NewString(), "", c.ID, "cancel", c.Version); must(e) }
		r, e := w.Consume(ctx, reviewEvent)
		a.hook = nil
		must(e)
		if r.Applied {
			t.Fatal("取消后扫描恢复")
		}
		r, e = unlock.Consume(ctx, wake)
		must(e)
		if r.Applied || current(c.ID).Status != "cancelled" {
			t.Fatal(r)
		}
		readable := create("受罚仍可读的本人胶囊")
		scan(readable.ID)
		due(readable.ID)
		future := create("受罚仍可取消")
		starts := now().Add(-time.Second)
		ends := starts.Add(7 * 24 * time.Hour)
		penalty := entity.Penalty{ID: uuid.NewString(), AccountID: owner, Level: "general", ActionKind: "mute", ViolationRef: uuid.NewString(), ContentVersion: 1, StartsAt: starts, EndsAt: &ends, ReasonRef: uuid.NewString()}
		must(admin.Create(&penalty).Error)
		if v := current(readable.ID); v.Body == nil {
			t.Fatal("处罚中断私密读取")
		}
		_, e = s.CloseCapsule(ctx, token, uuid.NewString(), "", future.ID, "cancel", future.Version)
		must(e)
		_, e = s.CreateCapsule(ctx, token, uuid.NewString(), "", whisper.CapsuleInput{Body: "受罚不能新封存", UnlockAt: now().Add(120 * time.Second), SafetyNoticeAccepted: true})
		if !errors.Is(e, command.ErrForbidden) {
			t.Fatal(e)
		}
		must(admin.Where("id=?", penalty.ID).Delete(&entity.Penalty{}).Error)
	})
	t.Run("扫描失败与通知失败不泄漏正文", func(t *testing.T) {
		c := create("扫描重试")
		due(c.ID)
		ev := event(c.ID, "review")
		a.privateFailure = errors.New("合成临时分类失败")
		_, e := w.Consume(ctx, ev)
		a.privateFailure = nil
		if !errors.Is(e, durable.ErrTemporary) {
			t.Fatal(e)
		}
		if v := current(c.ID); v.Body != nil || v.Status == "unlocked" {
			t.Fatal(v)
		}
		_, e = unlock.Consume(ctx, event(c.ID, "unlock"))
		must(e)
		must(admin.Exec("UPDATE public.work_items SET next_at=clock_timestamp()-interval '1 second' WHERE id=?", ev.CorrelationID).Error)
		_, e = w.Consume(ctx, ev)
		must(e)
		if current(c.ID).Body == nil {
			t.Fatal("重试批准未能按时读取")
		}
		must(admin.Exec(`CREATE FUNCTION public.fail_capsule_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='whisper_capsule_unlocked' THEN RAISE EXCEPTION '合成站内通知故障'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_capsule_notice BEFORE INSERT ON public.notifications FOR EACH ROW EXECUTE FUNCTION public.fail_capsule_notice()`).Error)
		_, e = unlock.Consume(ctx, event(c.ID, "unlock"))
		if e == nil {
			t.Fatal("故障夹具未触发")
		}
		if current(c.ID).Body == nil || countNotices(c.ID) != 0 {
			t.Fatal("通知失败影响读取或半提交")
		}
		must(admin.Exec("DROP TRIGGER fail_capsule_notice ON public.notifications; DROP FUNCTION public.fail_capsule_notice()").Error)
		for i := 0; i < 10 && countNotices(c.ID) == 0; i++ {
			_, e = unlock.ProcessNext(ctx)
			must(e)
		}
		if countNotices(c.ID) != 1 {
			t.Fatal("失败后不能补偿通知")
		}
	})
	t.Run("拒绝危机与独立证据", func(t *testing.T) {
		rejected := create("普通命中 需要拒绝")
		scan(rejected.ID)
		var task entity.ModerationTask
		must(admin.Table("public.moderation_tasks AS t").Select("t.*").Joins("JOIN public.content_registry c ON c.id=t.content_id").Where("c.entity_id=?", rejected.ID).Take(&task).Error)
		_, e = reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", task.ID, moderation.WhisperReviewCommand{ExpectedVersion: task.Version, ExpectedContentVersion: 1, Decision: "rejected", Reason: "合成原因", RevisionSuggestion: "请另建新胶囊"})
		must(e)
		if v := current(rejected.ID); v.Status != "rejected" || v.ReviewResult.Reason == nil {
			t.Fatal(v)
		}
		v := current(rejected.ID)
		_, e = s.CloseCapsule(ctx, token, uuid.NewString(), "", v.ID, "delete", v.Version)
		must(e)
		must(admin.Where("id=?", task.ID).Take(&task).Error)
		if task.Reason == nil || *task.Reason != "普通副本已清理" || task.RevisionSuggestion == nil || *task.RevisionSuggestion != "普通副本已清理" {
			t.Fatal("普通终结保留了人工原文引用")
		}
		crisis := create("危机命中 私密信件")
		scan(crisis.ID)
		if v := current(crisis.ID); v.Status != "isolated" || v.Body != nil {
			t.Fatal(v)
		}
		page, e := crises.WhisperCrises(ctx, adminToken, "", "", 50)
		must(e)
		var caseID string
		for _, item := range page.Items {
			if item.Source.ID == crisis.ID {
				caseID = item.ID
			}
		}
		if caseID == "" {
			t.Fatal("危机未立即建案")
		}
		detail, e := crises.WhisperCrisis(ctx, adminToken, "", caseID)
		must(e)
		if detail.StudentNumber != nil || detail.IdentityStatus != "not_verified" {
			t.Fatal(detail)
		}
		v = current(crisis.ID)
		_, e = s.CloseCapsule(ctx, token, uuid.NewString(), "", v.ID, "delete", v.Version)
		must(e)
		detail, e = crises.WhisperCrisis(ctx, adminToken, "", caseID)
		must(e)
		if detail.Body != "危机命中 私密信件" {
			t.Fatal("普通删除破坏独立证据")
		}

		// 合成分类器的危机误报经专岗确认后重新安全分类，普通批准仍不提前读取。
		rescan := create("普通命中 误报复核")
		a.privateCrisis = true
		scan(rescan.ID)
		a.privateCrisis = false
		cases, e := crises.WhisperCrises(ctx, adminToken, "", "", 50)
		must(e)
		var fp moderation.WhisperCrisisSummary
		for _, item := range cases.Items {
			if item.Source.ID == rescan.ID {
				fp = item
			}
		}
		if fp.ID == "" {
			t.Fatal("未建立合成误报案件")
		}
		_, e = crises.ActOnWhisperCrisis(ctx, adminToken, uuid.NewString(), "", fp.ID, moderation.CrisisCommand{ExpectedVersion: fp.Version, Reason: "合成分类误报"}, true)
		must(e)
		if v := current(rescan.ID); v.Status == "unlocked" || v.Body != nil {
			t.Fatal("误报直接解锁")
		}
		scan(rescan.ID)
		var next entity.ModerationTask
		must(admin.Raw("SELECT t.* FROM public.moderation_tasks t JOIN public.whisper_capsules c ON c.review_task_id=t.id WHERE c.id=?", rescan.ID).Scan(&next).Error)
		if next.Status != "human_required" {
			t.Fatal("误报未回普通人工复核", next.Status)
		}
		_, e = reviews.DecideWhisper(ctx, adminToken, uuid.NewString(), "", next.ID, moderation.WhisperReviewCommand{ExpectedVersion: next.Version, ExpectedContentVersion: 1, Decision: "approved"})
		must(e)
		if v := current(rescan.ID); v.Status != "locked" || v.Body != nil {
			t.Fatal("普通批准绕过预约时间")
		}
	})
	t.Run("隐私屏障与二十个活动额度并发", func(t *testing.T) {
		c := create("隐私保护")
		scan(c.ID)
		due(c.ID)
		must(admin.Create(&entity.PrivacyHold{ID: uuid.NewString(), TargetKind: "whisper_capsule", TargetID: c.ID, HoldEpoch: 1, RequiredContentVersion: 1, Status: "active"}).Error)
		if v := current(c.ID); v.Status != "isolated" || v.Body != nil {
			t.Fatal(v)
		}
		r, e := unlock.Consume(ctx, event(c.ID, "unlock"))
		must(e)
		if r.Applied || countNotices(c.ID) != 0 {
			t.Fatal(r)
		}
		fresh, tok := uuid.NewString(), uuid.NewString()
		hash := sha256.Sum256([]byte(tok))
		must(admin.Create(&entity.Account{ID: fresh, Nickname: "合成额度主体"}).Error)
		must(admin.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), fresh, hash[:]).Error)
		var first whisper.Capsule
		input := whisper.CapsuleInput{Body: "额度信件", UnlockAt: now().Add(120 * time.Second), SafetyNoticeAccepted: true}
		for i := 0; i < 19; i++ {
			v, e := s.CreateCapsule(ctx, tok, uuid.NewString(), "", input)
			must(e)
			if i == 0 {
				first = v
			}
		}
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, e := s.CreateCapsule(ctx, tok, uuid.NewString(), "", input); results <- e }()
		}
		wg.Wait()
		close(results)
		success, limited := 0, 0
		for e := range results {
			if e == nil {
				success++
			} else if errors.Is(e, whisper.ErrCapsuleLimit) {
				limited++
			} else {
				t.Fatal(e)
			}
		}
		if success != 1 || limited != 1 {
			t.Fatal(success, limited)
		}
		request("whisperCapsuleCreate", "POST", "/me/whispers/capsules", tok, input, 409)
		_, e = s.CloseCapsule(ctx, tok, uuid.NewString(), "", first.ID, "cancel", first.Version)
		must(e)
		_, e = s.CreateCapsule(ctx, tok, uuid.NewString(), "", input)
		must(e)
		// B07补正：解锁、拒绝不占额度；隔离到期内容仍占额度。
		must(admin.Exec("UPDATE public.whisper_capsules SET review_state='approved',created_at=clock_timestamp()-interval '2 minutes',unlock_at=date_trunc('second',clock_timestamp())-interval '1 second' WHERE owner_id=? AND lifecycle='active'", fresh).Error)
		_, e = s.CreateCapsule(ctx, tok, uuid.NewString(), "", input)
		must(e)
		must(admin.Exec("UPDATE public.whisper_capsules SET review_state='rejected' WHERE owner_id=? AND lifecycle='active'", fresh).Error)
		_, e = s.CreateCapsule(ctx, tok, uuid.NewString(), "", input)
		must(e)
		must(admin.Exec("INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version,status) SELECT gen_random_uuid(),'whisper_capsule',id,1,1,'active' FROM public.whisper_capsules WHERE owner_id=? AND lifecycle='active'", fresh).Error)
		if _, e = s.CreateCapsule(ctx, tok, uuid.NewString(), "", input); !errors.Is(e, whisper.ErrCapsuleLimit) {
			t.Fatal("隔离未占用额度", e)
		}
	})
	if output := os.Getenv("APP_WHISPER_CAPSULE_WIRE_OUTPUT"); output != "" {
		b, e := json.MarshalIndent(samples, "", "  ")
		must(e)
		must(os.WriteFile(output, append(b, '\n'), 0600))
	}
}
