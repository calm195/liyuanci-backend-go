package whisper_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

type privacyAuthUnused struct{}

func (privacyAuthUnused) Exchange(context.Context, string) ([]byte, error) {
	panic("本用例不登录")
}
func (privacyAuthUnused) ResolveOpenID(context.Context, *gorm.DB, []byte) (string, error) {
	panic("本用例不解析身份")
}
func (privacyAuthUnused) Allow(context.Context, string) (bool, error) { panic("本用例不登录") }

type privacyRedactor struct {
	fail bool
	hook func()
}

func (r *privacyRedactor) Redact(ctx context.Context, kind, body string) (string, error) {
	if r.hook != nil {
		r.hook()
	}
	if r.fail {
		return "", command.ErrUnavailable
	}
	return "经过受控脱敏的自有内容", ctx.Err()
}

type privacyEraser struct {
	calls int
	refs  map[string]bool
}

func (e *privacyEraser) Erase(ctx context.Context, _ privacy.Intent, objects []privacy.Object) error {
	e.refs = map[string]bool{}
	for _, v := range objects {
		e.refs[v.ID] = true
	}
	e.calls++
	return ctx.Err()
}

// testWhisperPrivacy 覆盖B07的匿名切换、真实权限注销、幸存候选与独立日志恢复重删。
// 分类、KMS、脱敏及外部副本清理均为显式合成适配器，不冒充生产全副本验收。
func testWhisperPrivacy(t *testing.T, admin, api, maintenance *gorm.DB, s *whisper.Service, w *moderation.WhisperWorker, codec *securedata.Codec, journal privacy.Journal, cfg whisper.Config, catalog whisper.Catalog, admission whisper.Admission, analyzer *reviewAnalyzer) {
	ctx, log := context.Background(), zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	actor, peer := uuid.NewString(), uuid.NewString()
	token, peerToken := uuid.NewString(), uuid.NewString()
	for _, row := range [][2]string{{actor, token}, {peer, peerToken}} {
		must(admin.Exec("INSERT INTO public.accounts(id,nickname,verification) VALUES (?,'隐私合成主体','verified')", row[0]).Error)
		hash := sha256.Sum256([]byte(row[1]))
		must(admin.Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,1,clock_timestamp()+interval '1 hour')", uuid.NewString(), row[0], hash[:]).Error)
	}
	event := func(id string) durable.Event {
		var ev durable.Event
		must(admin.Raw(`SELECT e.event_id,e.event_type,e.aggregate_id,e.aggregate_version,e.work_id AS correlation_id,e.created_at FROM public.outbox_events e JOIN public.moderation_tasks t ON t.id=e.aggregate_id JOIN public.content_registry c ON c.id=t.content_id WHERE c.entity_id=? ORDER BY t.created_at DESC,t.id DESC LIMIT 1`, id).Scan(&ev).Error)
		return ev
	}
	consume := func(id string) { _, e := w.Consume(ctx, event(id)); must(e) }
	post := func(tok string) whisper.PublicPost {
		p, e := s.CreatePost(ctx, tok, uuid.NewString(), "", whisper.ContentInput{Body: "隐私测试父帖", Identity: whisper.IdentityInput{Mode: "anonymous"}})
		must(e)
		_, e = s.ChangePost(ctx, tok, uuid.NewString(), "", p.ID, "submit", whisper.PostInput{ExpectedVersion: p.Version})
		must(e)
		consume(p.ID)
		raw, e := s.GetPost(ctx, tok, "", p.ID, false)
		must(e)
		return raw.(whisper.PublicPost)
	}
	name, e := s.GetNickname(ctx, token, "")
	must(e)
	_, e = s.SetNickname(ctx, token, uuid.NewString(), "", whisper.NicknameInput{ExpectedVersion: name.Version, Label: "隐私昵称"})
	must(e)
	var nameRef string
	must(admin.Model(&entity.WhisperNicknameVersion{}).Where("account_id=?", actor).Pluck("content_registry_id", &nameRef).Error)
	consume(nameRef)
	parent := post(token)
	otherParent := post(peerToken)
	comment := func(tok string, p whisper.PublicPost) whisper.OwnerComment {
		v, e := s.SaveComment(ctx, tok, uuid.NewString(), "", p.ID, "", whisper.CommentInput{ContentInput: whisper.ContentInput{Body: "保留作者自己的暖评和原帖主信息", Identity: whisper.IdentityInput{Mode: "anonymous"}}, ExpectedPostVersion: p.Version, ExpectedCommentsEpoch: p.CommentsEpoch})
		must(e)
		return v
	}
	survivor := comment(peerToken, parent)
	consume(survivor.ID)
	ownComment := comment(token, otherParent)
	consume(ownComment.ID)
	late := comment(peerToken, parent)
	lateEvent := event(late.ID)
	capsule, e := s.CreateCapsule(ctx, token, uuid.NewString(), "", whisper.CapsuleInput{Body: "注销前尚未审核的未来信", UnlockAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), SafetyNoticeAccepted: true})
	must(e)
	must(admin.Exec("UPDATE public.work_items SET status='waiting' WHERE kind='whisper_capsule_unlock' AND target_id=?", capsule.ID).Error)
	must(admin.Model(&entity.Account{}).Where("id=?", actor).Update("verification", "unverified").Error)
	evidenceCapsule, e := s.CreateCapsule(ctx, token, uuid.NewString(), "", whisper.CapsuleInput{Body: "独立危机证据私密信", UnlockAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), SafetyNoticeAccepted: true})
	must(e)
	analyzer.privateCrisis = true
	consume(evidenceCapsule.ID)
	analyzer.privateCrisis = false
	must(admin.Model(&entity.Account{}).Where("id=?", actor).Update("verification", "verified").Error)
	var evidenceID string
	must(admin.Model(&entity.EvidenceCrisisCase{}).Where("source_entity_id=?", evidenceCapsule.ID).Pluck("evidence_case_id", &evidenceID).Error)
	if evidenceID == "" {
		t.Fatal("未建立独立证据")
	}
	governance, e := safety.New(api, log, codec, safety.Config{Command: cfg.Command, Key: bytes.Repeat([]byte{15}, 32), MaxTextBytes: 2000})
	must(e)
	// 切换前历史关系真实写入并保留相同block_id。
	_, e = governance.Execute(ctx, peerToken, uuid.NewString(), "", "block", "", safety.Input{TargetUserID: actor})
	must(e)
	var historical entity.Block
	must(admin.Where("owner_id=? AND target_id=?", peer, actor).Take(&historical).Error)
	must(blockpolicy.Enable(ctx, admin, log))
	must(migration.ApplyPermissions(ctx, admin, log))
	if e = admin.Exec("UPDATE public.interaction_block_policy SET opaque_enabled=false,switched_at=NULL").Error; e == nil {
		t.Fatal("切换允许回退")
	}
	if e = migration.Run(ctx, admin, log, "down"); e == nil {
		t.Fatal("切换后允许破坏性回退")
	}
	offCfg := cfg
	offCfg.Enabled = false
	off, e := whisper.NewService(api, log, codec, catalog, admission, nil, offCfg)
	must(e)
	r := httpapi.New(log, nil)
	must(httpapi.RegisterSafety(r, governance, governance))
	must(httpapi.RegisterInteractionBlocks(r, s))
	samples := []map[string]any{}
	request := func(op, method, path, tok, body string, status int) json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rr.Code, rr.Body.String())
		}
		if rr.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("响应可缓存")
		}
		if op != "" {
			samples = append(samples, map[string]any{"operation": op, "status": rr.Code, "body": func() any {
				if status == 204 {
					return nil
				}
				return json.RawMessage(rr.Body.Bytes())
			}()})
		}
		return rr.Body.Bytes()
	}
	t.Run("旧协议与新Me切换且模块关闭不降级", func(t *testing.T) {
		for _, tc := range []struct{ method, path string }{{"GET", "/me/blocks?limit=bogus"}, {"POST", "/me/blocks"}, {"DELETE", "/me/blocks/not-a-uuid"}} {
			raw := request("", tc.method, tc.path, token, "{}", 409)
			if !bytes.Contains(raw, []byte("CLIENT_UPGRADE_REQUIRED")) {
				t.Fatal(string(raw))
			}
			request("", tc.method, tc.path, "invalid", "{}", 401)
		}
		page, e := off.ListBlocks(ctx, peerToken, "", whisper.Query{})
		must(e)
		if len(page.Items) != 1 || page.Items[0].BlockID != historical.BlockID {
			t.Fatal(page)
		}
		must(off.DeleteBlock(ctx, peerToken, uuid.NewString(), "", historical.BlockID, historical.Version))
		_, e = off.CreateBlock(ctx, peerToken, uuid.NewString(), "", whisper.BlockInput{Source: whisper.BlockSource{Kind: "public_profile", ID: actor}})
		must(e)
		auth, e := studentauth.New(api, log, privacyAuthUnused{}, privacyAuthUnused{}, privacyAuthUnused{}, studentauth.Config{Command: cfg.Command, Safety: true, SessionKey: bytes.Repeat([]byte{18}, 32), SessionTTL: time.Hour, ExchangeTTL: time.Minute})
		must(e)
		me, e := auth.Current(ctx, token, "")
		must(e)
		raw, _ := json.Marshal(me)
		if bytes.Contains(raw, []byte("postMeBlocks")) || bytes.Contains(raw, []byte("deleteMeBlocks")) || !bytes.Contains(raw, []byte("interactionBlockCreate")) {
			t.Fatal(string(raw))
		}
	})
	t.Run("三个opaque操作与精确可见来源", func(t *testing.T) {
		page, e := s.ListBlocks(ctx, peerToken, "", whisper.Query{})
		must(e)
		request("interactionBlocksList", "GET", "/me/interaction-blocks", peerToken, "", 200)
		id := page.Items[0].BlockID
		request("interactionBlockDelete", "DELETE", "/me/interaction-blocks/"+id, peerToken, `{"expected_version":1}`, 204)
		body := `{"source":{"kind":"whisper_post","id":"` + parent.ID + `","content_version":1}}`
		raw := request("interactionBlockCreate", "POST", "/me/interaction-blocks", peerToken, body, 200)
		if bytes.Contains(raw, []byte(actor)) || bytes.Contains(raw, []byte("block_id")) {
			t.Fatal(string(raw))
		}
		page, e = s.ListBlocks(ctx, peerToken, "", whisper.Query{})
		must(e)
		if e = s.DeleteBlock(ctx, token, uuid.NewString(), "", page.Items[0].BlockID, page.Items[0].Version); !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		if e = s.DeleteBlock(ctx, peerToken, uuid.NewString(), "", page.Items[0].BlockID, page.Items[0].Version+1); !errors.Is(e, command.ErrVersionConflict) {
			t.Fatal(e)
		}
		must(s.DeleteBlock(ctx, peerToken, uuid.NewString(), "", page.Items[0].BlockID, page.Items[0].Version))
		bad := int64(99)
		_, e = s.CreateBlock(ctx, peerToken, uuid.NewString(), "", whisper.BlockInput{Source: whisper.BlockSource{Kind: "whisper_post", ID: parent.ID, ContentVersion: &bad}})
		if !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		one := int64(1)
		_, e = s.CreateBlock(ctx, peerToken, uuid.NewString(), "", whisper.BlockInput{Source: whisper.BlockSource{Kind: "whisper_comment", ID: ownComment.ID, ContentVersion: &one}})
		must(e)
		request("", "POST", "/me/interaction-blocks", peerToken, `{"source":{"kind":"public_profile","id":"`+actor+`","content_version":1}}`, 400)
	})
	t.Run("普通API无直接原稿治理清理权限", func(t *testing.T) {
		digest := sha256.Sum256([]byte(peerToken))
		for _, id := range []string{ownComment.ID, survivor.ID} {
			if e := api.Exec("SELECT public.clear_held_whisper_comment(decode(?,'hex'),?,?::jsonb)", hex.EncodeToString(digest[:]), id, "[]").Error; e == nil {
				t.Fatal("清理函数越过本人屏障")
			}
		}
		if e := api.Model(&entity.WhisperCommentVersion{}).Where("comment_id=?", survivor.ID).Update("body_cipher", json.RawMessage(`{}`)).Error; e == nil {
			t.Fatal("普通API可直接改写原稿")
		}
		if e := api.Exec("DELETE FROM public.moderation_scans WHERE false").Error; e == nil {
			t.Fatal("普通API可直接删除扫描")
		}
	})
	var peerNotice string
	must(admin.Model(&entity.WhisperNotificationRef{}).Where("source_id=?", ownComment.ID).Pluck("notification_id", &peerNotice).Error)
	if peerNotice == "" {
		t.Fatal("缺少评论通知")
	}
	// 把真实旧业务快照保存为JSON，仅位于隔离测试进程内；完成后重新插入以验证独立日志覆盖completed回执。
	tables := []string{"accounts", "sessions", "content_registry", "moderation_tasks", "whisper_preferences", "whisper_nickname_versions", "whisper_posts", "whisper_post_versions", "whisper_comments", "whisper_comment_versions", "whisper_capsules", "whisper_aliases", "whisper_hugs", "work_items", "outbox_events"}
	snapshots := map[string]string{}
	columns := map[string]string{}
	for _, table := range tables {
		var cols string
		must(admin.Raw("SELECT string_agg(quote_ident(attname),',' ORDER BY attnum) FROM pg_attribute WHERE attrelid=?::regclass AND attnum>0 AND NOT attisdropped AND attgenerated=''", "public."+table).Scan(&cols).Error)
		columns[table] = cols
		var raw string
		must(admin.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]'::jsonb)::text FROM public." + table + " t").Scan(&raw).Error)
		snapshots[table] = raw
	}
	redactor := &privacyRedactor{fail: true}
	eraser := &privacyEraser{}
	pcfg := privacy.Config{Command: cfg.Command, Key: bytes.Repeat([]byte{17}, 32), ReceiptTTL: 24 * time.Hour, RetryDelay: time.Millisecond, Redactor: redactor}
	deletion, e := privacy.New(maintenance, journal, codec, nil, log, pcfg)
	must(e)
	purger, e := privacy.New(maintenance, journal, codec, eraser, log, pcfg)
	must(e)
	var accepted privacy.Accepted
	t.Run("同步屏障暂停脱敏与外部清理", func(t *testing.T) {
		var a entity.Account
		must(admin.Where("id=?", actor).Take(&a).Error)
		accepted, e = deletion.Request(ctx, token, uuid.NewString(), "", privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: a.Version})
		must(e)
		if _, e = s.GetPost(ctx, "", "", parent.ID, false); !errors.Is(e, command.ErrNotFound) {
			t.Fatal(e)
		}
		if _, e = s.GetNickname(ctx, token, ""); !errors.Is(e, command.ErrUnauthenticated) {
			t.Fatal(e)
		}
		c, e := s.GetComment(ctx, peerToken, "", survivor.ID)
		must(e)
		if c.Body != nil || c.RecoveryBody != nil || c.PostID != nil || c.Status != "privacy_hold" {
			t.Fatal(c)
		}
		notices, e := s.Notifications(ctx, peerToken, "", whisper.Query{})
		must(e)
		for _, v := range notices.Items {
			if v.ID == peerNotice {
				t.Fatal("暂停清理仍能读旧通知")
			}
		}
		if _, e = s.ReadNotification(ctx, peerToken, uuid.NewString(), "", peerNotice, 1); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("抑制通知可重放", e)
		}
		processed, e := purger.ProcessNext(ctx, "account")
		if !processed || !errors.Is(e, command.ErrUnavailable) || eraser.calls != 0 {
			t.Fatal(processed, e, eraser.calls)
		}
		var pending int64
		must(admin.Model(&entity.WorkItem{}).Where("target_id=? AND status<>'cancelled'", capsule.ID).Count(&pending).Error)
		if pending != 0 {
			t.Fatal("解锁工作未取消")
		}
		_, e = w.Consume(ctx, lateEvent)
		must(e)
		c, e = s.GetComment(ctx, peerToken, "", late.ID)
		must(e)
		if c.Body != nil || c.Status != "privacy_hold" {
			t.Fatal(c)
		}
		redactor.fail = false
		noExternal, e := privacy.New(maintenance, journal, codec, nil, log, pcfg)
		must(e)
		must(admin.Exec("UPDATE public.deletion_jobs SET next_at=clock_timestamp() WHERE scope='account'").Error)
		_, e = noExternal.ProcessNext(ctx, "account")
		if !errors.Is(e, command.ErrUnavailable) {
			t.Fatal(e)
		}
		status, e := deletion.Status(ctx, "", accepted.Receipt)
		must(e)
		if status.Status != "processing" {
			t.Fatal(status)
		}
	})
	process := func() {
		must(admin.Exec("UPDATE public.deletion_jobs SET next_at=clock_timestamp() WHERE scope='account'").Error)
		_, e := purger.ProcessNext(ctx, "account")
		must(e)
	}
	t.Run("脱敏期间版本变化拒绝旧结果", func(t *testing.T) {
		redactor.hook = func() {
			redactor.hook = nil
			must(admin.Model(&entity.WhisperComment{}).Where("id=?", survivor.ID).Update("version", gorm.Expr("version+1")).Error)
		}
		must(admin.Exec("UPDATE public.deletion_jobs SET next_at=clock_timestamp() WHERE scope='account'").Error)
		_, e := purger.ProcessNext(ctx, "account")
		if !errors.Is(e, command.ErrRetry) {
			t.Fatal(e)
		}
		c, e := s.GetComment(ctx, peerToken, "", survivor.ID)
		must(e)
		if c.Body != nil || c.RecoveryBody != nil {
			t.Fatal("旧脱敏结果穿透", c)
		}
	})
	t.Run("普通删除图幸存候选与新稿新ID", func(t *testing.T) {
		process()
		status, e := deletion.Status(ctx, "", accepted.Receipt)
		must(e)
		if status.Status != "completed" {
			t.Fatal(status)
		}
		for _, ref := range []string{parent.ID, survivor.ID, capsule.ID, peerNotice} {
			if !eraser.refs[ref] {
				t.Fatal("外部清理遗漏引用", ref)
			}
		}
		var retained int64
		must(admin.Model(&entity.EvidenceCase{}).Where("id=?", evidenceID).Count(&retained).Error)
		if retained != 1 {
			t.Fatal("普通注销删除了限定证据")
		}
		if maintenance.Exec("SELECT * FROM evidence.evidence_items LIMIT 0").Error == nil {
			t.Fatal("普通隐私身份能读限定证据")
		}
		var count int64
		for _, tc := range []struct {
			model any
			query string
		}{{&entity.Account{}, "id=?"}, {&entity.ContentRegistry{}, "author_id=?"}, {&entity.WhisperPost{}, "owner_id=?"}, {&entity.WhisperCapsule{}, "owner_id=?"}, {&entity.WhisperNicknameVersion{}, "account_id=?"}, {&entity.WhisperComment{}, "author_id=?"}, {&entity.WhisperAlias{}, "actor_id=?"}, {&entity.WhisperHug{}, "actor_id=?"}} {
			must(admin.Model(tc.model).Where(tc.query, actor).Count(&count).Error)
			if count != 0 {
				t.Fatalf("%T 残留 %d", tc.model, count)
			}
		}
		c, e := s.GetComment(ctx, peerToken, "", survivor.ID)
		must(e)
		if c.Body != nil || c.PostID != nil || c.RecoveryBody == nil || *c.RecoveryBody != "经过受控脱敏的自有内容" {
			t.Fatal(c)
		}
		if _, e = s.GetPost(ctx, peerToken, "", otherParent.ID, false); e != nil {
			t.Fatal("评论者注销误删他人父帖", e)
		}
		// 选择其他可见帖子重新填写，新的审核、别名和ID与旧串完全独立。
		raw, e := s.GetPost(ctx, peerToken, "", otherParent.ID, false)
		must(e)
		fresh := comment(peerToken, raw.(whisper.PublicPost))
		if fresh.ID == survivor.ID {
			t.Fatal("重用了旧ID")
		}
	})
	t.Run("已确认独立日志重建屏障并再次清理", func(t *testing.T) {
		// 先恢复账号、父帖和旧原稿；deferred FK让这个模拟快照恢复与真实事务一样原子。
		must(admin.Transaction(func(db *gorm.DB) error {
			for _, table := range tables {
				sql := "INSERT INTO public." + table + "(" + columns[table] + ") SELECT " + columns[table] + " FROM jsonb_populate_recordset(NULL::public." + table + ",?::jsonb) ON CONFLICT DO NOTHING"
				if e := db.Exec(sql, snapshots[table]).Error; e != nil {
					return e
				}
			}
			// 恢复幸存评论原始父关联和公开候选，以证明日志会再次隐藏而非依赖上次hold。
			if e := db.Exec("DELETE FROM public.privacy_holds WHERE target_kind LIKE 'whisper_%' AND target_id IN ?", []string{parent.ID, survivor.ID, late.ID, capsule.ID}).Error; e != nil {
				return e
			}
			if e := db.Exec("UPDATE public.whisper_comments c SET post_id=x.post_id,privacy_epoch=x.privacy_epoch,version=x.version,review_state=x.review_state,recovery_cipher=x.recovery_cipher FROM jsonb_populate_recordset(NULL::public.whisper_comments,?::jsonb) x WHERE c.id=x.id", snapshots["whisper_comments"]).Error; e != nil {
				return e
			}
			return nil
		}))
		_, e = purger.Recover(ctx, "", 100, "account")
		must(e)
		if _, e = s.GetPost(ctx, "", "", parent.ID, false); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("恢复先于重删开放", e)
		}
		process()
		var n int64
		must(admin.Model(&entity.Account{}).Where("id=?", actor).Count(&n).Error)
		if n != 0 {
			t.Fatal("恢复未重删")
		}
		c, e := s.GetComment(ctx, peerToken, "", survivor.ID)
		must(e)
		if c.PostID != nil || c.Body != nil || c.RecoveryBody == nil {
			t.Fatal(c)
		}
	})
	t.Run("幸存作者撤回清除普通恢复副本", func(t *testing.T) {
		c, e := s.GetComment(ctx, peerToken, "", survivor.ID)
		must(e)

		c, e = s.WithdrawComment(ctx, peerToken, uuid.NewString(), "", c.ID, c.Version)
		must(e)
		if c.Body != nil || c.RecoveryBody != nil || c.PostID != nil {
			t.Fatal(c)
		}
	})
	if path := os.Getenv("APP_WHISPER_PRIVACY_WIRE_OUTPUT"); path != "" {
		raw, _ := json.MarshalIndent(samples, "", "  ")
		must(os.WriteFile(path, append(raw, '\n'), 0600))
	}
}
