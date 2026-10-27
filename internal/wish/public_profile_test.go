package wish

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// TestPostgresPublicProfile 追溯REQ-004/005/008/014/018：使用隔离PG和真实运行角色验证混合分页与权限屏障。
// 审核结果由专用测试身份合成；完整故事审核链另由TestStorySharing覆盖。
func TestPostgresPublicProfile(t *testing.T) {
	f := lifecycleSetup(t)
	s, ctx := f.service, context.Background()
	s.config.MaxStoryBytes = 65536
	publish := func() CreatedDraft {
		t.Helper()
		r := f.ready()
		out, e := s.Publish(ctx, f.token, uuid.NewString(), "profile-publish", r.ID, r.Version)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	first, second, story := publish(), publish(), f.ready()
	f.create() // 本人草稿也不得进入主页。
	position := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	f.exec("UPDATE public.wishes SET published_at=? WHERE id IN (?,?)", position, first.ID, second.ID)
	f.exec(`UPDATE public.wish_versions SET identity_tags='{"gender":"female"}' WHERE wish_id IN (?,?)`, first.ID, second.ID)
	f.exec("UPDATE public.wishes SET state='fulfilled',current_story_version=1 WHERE id=?", story.ID)
	registry, task := uuid.NewString(), uuid.NewString()
	no, approved, epoch := false, "approved", int64(1)
	if e := f.root.Create(&entity.ContentRegistry{ID: registry, Kind: "story", EntityID: story.ID, AuthorID: f.owner, ContentVersion: 1, Status: approved, SchoolRelated: &no, SchoolDetectionState: "completed", SchoolDetectionVersion: "synthetic"}).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.root.Create(&entity.ModerationTask{ID: task, ContentID: registry, ContentVersion: 1, SubjectEpoch: 1, HoldEpoch: 1, RestrictionEpoch: &epoch, Status: approved, Decision: &approved}).Error; e != nil {
		t.Fatal(e)
	}
	if e := f.root.Create(&entity.StoryVersion{WishID: story.ID, StoryVersion: 1, AuthorID: f.owner, BodyCipher: json.RawMessage(`{}`), PublicBody: pointer("合成已审故事"), ModerationStatus: approved, SchoolRelated: &no, SchoolDetectionState: "completed", SchoolDetectionVersion: "synthetic", ReviewTaskID: &task, IdentityTags: json.RawMessage(`{"gender":"female"}`), PublishedAt: &position}).Error; e != nil {
		t.Fatal(e)
	}
	f.exec("INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version,status,released_at) VALUES (?,'story',?,1,1,'released',clock_timestamp())", uuid.NewString(), story.ID)
	read := func(token, target, cursor string, limit int) ProfileContents {
		t.Helper()
		out, e := s.PublicProfile(ctx, token, "profile-test", target, cursor, limit)
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	page := read("", f.owner, "", 1)
	if page.Profile.ID != f.owner || page.Profile.Nickname != "合成作者" || len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal(page)
	}
	seen, kinds := map[string]bool{}, map[string]bool{}
	cursor := ""
	previous := ""
	for {
		current := read("", f.owner, cursor, 1)
		if len(current.Items) != 1 {
			t.Fatal("混合分页缺项", current)
		}
		item := current.Items[0]
		if previous != "" && item.Content.ID >= previous {
			t.Fatal("同公开时间没有按ID降序")
		}
		previous = item.Content.ID
		if seen[item.Content.ID] || item.Content.Author.ID != f.owner || item.Content.Author.IdentityTags.Gender == nil {
			t.Fatal("重复或投影不一致", item)
		}
		seen[item.Content.ID], kinds[item.Kind] = true, true
		if current.NextCursor == nil {
			break
		}
		cursor = *current.NextCursor
	}
	if len(seen) != 3 || !kinds["wish"] || !kinds["story"] {
		t.Fatal(seen, kinds)
	}
	for _, tc := range []struct {
		name, token, target, cursor string
		limit                       int
		want                        error
	}{
		{"目标绑定", "", uuid.NewString(), *page.NextCursor, 1, command.ErrInvalid},
		{"读取账号绑定", f.token, f.owner, *page.NextCursor, 1, command.ErrInvalid},
		{"篡改", "", f.owner, *page.NextCursor + "x", 1, command.ErrInvalid},
		{"坏会话不降级", "bad-token", f.owner, "", 1, command.ErrUnauthenticated},
		{"不存在", "", uuid.NewString(), "", 1, command.ErrNotFound},
		{"上限", "", f.owner, "", 51, command.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, e := s.PublicProfile(ctx, tc.token, "deny", tc.target, tc.cursor, tc.limit)
			if !errors.Is(e, tc.want) || out.Profile.ID != "" || out.Items != nil {
				t.Fatal(out, e)
			}
		})
	}
	f.exec("UPDATE public.accounts SET verification='unverified' WHERE id=?", f.owner)
	if page := read("", f.owner, "", 50); len(page.Items) != 0 {
		t.Fatal("资格撤销后仍公开内容", page)
	}
	f.exec("UPDATE public.accounts SET verification='verified' WHERE id=?", f.owner)
	f.exec("UPDATE public.accounts SET hide_identity_tags=true WHERE id=?", f.owner)
	hidden := read(f.token, f.owner, "", 50)
	for _, item := range hidden.Items {
		if item.Content.Author.IdentityTags.Gender != nil {
			t.Fatal("隐藏标签泄漏")
		}
	}
	f.exec("INSERT INTO public.privacy_holds(id,target_kind,target_id,hold_epoch,required_content_version) VALUES (?,'wish',?,1,1)", uuid.NewString(), first.ID)
	f.exec("UPDATE public.wishes SET expires_at=clock_timestamp()-interval '1 second' WHERE id=?", second.ID)
	f.exec("UPDATE public.story_versions SET withdrawn_at=clock_timestamp() WHERE wish_id=?", story.ID)
	empty := read(f.token, f.owner, "", 50)
	if len(empty.Items) != 0 || empty.NextCursor != nil {
		t.Fatal("屏障、过期或撤回内容进入主页", empty)
	}
	f.exec("UPDATE public.accounts SET lifecycle='deleting' WHERE id=?", f.owner)
	if out, e := s.PublicProfile(ctx, "", "deleted", f.owner, "", 20); !errors.Is(e, command.ErrNotFound) || out.Items != nil {
		t.Fatal(out, e)
	}
}
