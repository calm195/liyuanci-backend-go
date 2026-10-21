package wish

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// TestCursorBinding 追溯 T014/REQ-018：游标防篡改并绑定公开筛选或本人主体，不接受跨域复用。
func TestCursorBinding(t *testing.T) {
	s := &Service{receiptKey: serviceConfig().ReceiptKey}
	scope := queryScope("public", "", Query{Category: "help"})
	c := pageCursor{Scope: scope, ID: "60000000-0000-4000-8000-000000000001", PublishedAt: at()}
	value := s.encodeCursor(c)
	if got, e := s.decodeCursor(value, scope); e != nil || got != c {
		t.Fatal(got, e)
	}
	for _, v := range []string{"invalid", value + "x", strings.Repeat("x", 2049)} {
		if _, e := s.decodeCursor(v, scope); e == nil {
			t.Fatal("接受无效游标")
		}
	}
	if _, e := s.decodeCursor(value, queryScope("public", "", Query{Category: "other"})); e == nil {
		t.Fatal("跨筛选复用")
	}
	if _, e := s.decodeCursor(value, queryScope("own", "owner", Query{})); e == nil {
		t.Fatal("跨主体复用")
	}
	if e := validateQuery(Query{Limit: 0}, false); e == nil {
		t.Fatal("接受零分页")
	}
	if e := validateQuery(Query{Limit: 101}, true); e == nil {
		t.Fatal("接受过大分页")
	}
}

// TestPostgresQueries 追溯 T014/REQ-003/004/005/008/014/018：真实PG当前版本、资格、屏障与本人解密视图。
func TestPostgresQueries(t *testing.T) {
	f := lifecycleSetup(t)
	s := f.service
	ctx := context.Background()
	draft := f.create()
	own, e := s.Detail(ctx, f.token, "own", draft.ID)
	if e != nil || own.OwnContent == nil || own.OwnContent.Body != draftInput().Body || own.Public != nil || len(own.AllowedActions) == 0 {
		t.Fatal(own, e)
	}
	if _, e = s.Detail(ctx, "", "guest", draft.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal("游客读草稿", e)
	}
	if _, e = s.Detail(ctx, "bad", "bad", draft.ID); !errors.Is(e, command.ErrUnauthenticated) {
		t.Fatal(e)
	}
	r := f.ready()
	published, e := s.Publish(ctx, f.token, "query-publish", "publish", r.ID, r.Version)
	if e != nil || published.State != Published {
		t.Fatal(published, e)
	}
	public, e := s.Detail(ctx, "", "public", r.ID)
	if e != nil || public.Public == nil || public.OwnContent != nil || public.Public.Body != "合成已审正文" {
		t.Fatal(public, e)
	}
	// 当前隐藏设置立即生效，已审标签不被复制到匿名以外的私有输出。
	f.exec(`UPDATE public.wish_versions SET identity_tags='{"gender":"female"}' WHERE wish_id=?`, r.ID)
	public, e = s.Detail(ctx, "", "tags", r.ID)
	if e != nil || public.Public.Author.IdentityTags.Gender == nil {
		t.Fatal(public, e)
	}
	f.exec("UPDATE public.accounts SET hide_identity_tags=true WHERE id=?", f.owner)
	public, e = s.Detail(ctx, "", "hidden", r.ID)
	if e != nil || public.Public.Author.IdentityTags.Gender != nil {
		t.Fatal(public, e)
	}
	page, e := s.List(ctx, "list", Query{Limit: 1})
	if e != nil || len(page.Items) == 0 {
		t.Fatal(page, e)
	}
	// 相同上架时间用ID打破平局，签名游标分页不重复；不同筛选禁止复用。
	r2 := f.ready()
	if _, e = s.Publish(ctx, f.token, "query-publish-2", "publish", r2.ID, r2.Version); e != nil {
		t.Fatal(e)
	}
	page, e = s.List(ctx, "list", Query{Limit: 1})
	if e != nil || page.NextCursor == nil {
		t.Fatal(page, e)
	}
	next, e := s.List(ctx, "next", Query{Limit: 1, Cursor: *page.NextCursor})
	if e != nil || len(next.Items) != 1 || page.Items[0].ID == next.Items[0].ID {
		t.Fatal(next, e)
	}
	if _, e = s.List(ctx, "changed", Query{Limit: 1, Category: "help", Cursor: *page.NextCursor}); !errors.Is(e, command.ErrInvalid) {
		t.Fatal(e)
	}
	ownPage, e := s.Own(ctx, f.token, "mine", Query{Limit: 1})
	if e != nil || len(ownPage.Items) != 1 || ownPage.NextCursor == nil {
		t.Fatal(ownPage, e)
	}
	// 屏障同时阻止游客公开和本人旧正文，不会因原审核approved而放行。
	f.exec("INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version) VALUES (?,'wish',?,1,1)", "70000000-0000-4000-8000-000000000001", r.ID)
	if _, e = s.Detail(ctx, "", "held", r.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal(e)
	}
	own, e = s.Detail(ctx, f.token, "held-own", r.ID)
	if e != nil || own.OwnContent != nil || !own.PrivacyReviewRequired || len(own.AllowedActions) != 0 {
		t.Fatal(own, e)
	}
	// 即使屏障已释放，旧审核的hold代次仍不可复用。
	f.exec("UPDATE public.privacy_holds SET status='released',released_at=clock_timestamp() WHERE target_id=?", r.ID)
	if _, e = s.Detail(ctx, "", "old-hold", r.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal(e)
	}
	f.exec("UPDATE public.wishes SET expires_at=clock_timestamp()-interval '1 second' WHERE id=?", r2.ID)
	if _, e = s.Detail(ctx, "", "expired", r2.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal(e)
	}
	own, e = s.Detail(ctx, f.token, "expired-own", r2.ID)
	if e != nil || own.State != Expired {
		t.Fatal(own, e)
	}
	expired, e := s.Own(ctx, f.token, "expired-list", Query{Limit: 20, State: Expired})
	if e != nil || len(expired.Items) != 1 {
		t.Fatal(expired, e)
	}
	// 删除标记在列表、详情和创建回执重放中均不可复活。
	if e = s.Delete(ctx, f.token, "query-delete", "delete", draft.ID, draft.Version); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Detail(ctx, f.token, "deleted", draft.ID); !errors.Is(e, command.ErrNotFound) {
		t.Fatal(e)
	}
	f.exec("UPDATE public.sessions SET revoked_at=clock_timestamp() WHERE account_id=?", f.owner)
	if _, e = s.Own(ctx, f.token, "revoked", Query{Limit: 20}); !errors.Is(e, command.ErrUnauthenticated) {
		t.Fatal(e)
	}
	page, e = s.List(ctx, "barrier-list", Query{Limit: 100})
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range page.Items {
		if item.ID == r.ID || item.ID == r2.ID {
			t.Fatal("屏障/过期内容进入广场")
		}
	}

}
