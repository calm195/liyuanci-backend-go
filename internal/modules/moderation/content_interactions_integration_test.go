package moderation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/contentinteraction"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// verifyContentInteractions 使用实际领域解析和受限角色，覆盖预读后下架、收藏失效和浏览事件重放。
func verifyContentInteractions(t *testing.T, root, api *gorm.DB, log *zap.Logger, cfg command.Config, wishes *wish.Service, student func() (string, string), published func(string) wish.CreatedDraft) {
	t.Run("收藏事务复核与失效条目取消及浏览去重", func(t *testing.T) {
		ctx := context.Background()
		must := func(e error) {
			t.Helper()
			if e != nil {
				t.Fatal(e)
			}
		}
		_, token := student()
		w := published(token)
		changeAfterRead := false
		resolve := func(ctx context.Context, token, rid, kind, id string) (contentinteraction.Target, error) {
			v, e := wishes.PublicInteractionContent(ctx, token, rid, id)
			if e == nil && changeAfterRead {
				changeAfterRead = false
				e = root.Model(&entity.Wish{}).Where("id=?", id).Update("state", "withdrawn").Error
			}
			return contentinteraction.Target{Title: v.Title, Excerpt: v.Body}, e
		}
		resolveTx := func(ctx context.Context, db *gorm.DB, now time.Time, actor, kind, id string) (contentinteraction.Target, error) {
			v, e := wishes.ReadInteractionContent(ctx, db, now, id)
			return contentinteraction.Target{Title: v.Title, Excerpt: v.Body}, e
		}
		s, e := contentinteraction.New(api, log, cfg, []byte(strings.Repeat("f", 32)), resolve, resolveTx)
		must(e)
		changeAfterRead = true
		if _, e = s.SetFavorite(ctx, token, uuid.NewString(), "", "wish", w.ID, true); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("收藏仅相信事务外预读", e)
		}
		var total int64
		must(root.Model(&entity.ContentFavorite{}).Where("target_kind='wish' AND target_id=?", w.ID).Count(&total).Error)
		if total != 0 {
			t.Fatal("下架竞争仍写入收藏")
		}
		must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Update("state", "published").Error)
		state, e := s.SetFavorite(ctx, token, uuid.NewString(), "", "wish", w.ID, true)
		must(e)
		if !state.Favorited {
			t.Fatal("本人发布内容不能收藏")
		}
		eventID := uuid.NewString()
		for range 2 {
			stats, e := s.View(ctx, token, "", "wish", w.ID, eventID)
			must(e)
			if stats.ViewCount != 1 || !stats.Favorited {
				t.Fatal("页面事件重放重复计数或丢失本人收藏", stats)
			}
		}
		must(root.Model(&entity.Wish{}).Where("id=?", w.ID).Update("state", "withdrawn").Error)
		page, e := s.Favorites(ctx, token, "", "wish", "", 20)
		must(e)
		if len(page.Items) != 1 || page.Items[0].Available || page.Items[0].Content != nil {
			t.Fatal("收藏失效后回放旧正文", page)
		}
		if _, e = s.View(ctx, token, "", "wish", w.ID, uuid.NewString()); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("下架内容继续接受浏览事件", e)
		}
		state, e = s.SetFavorite(ctx, token, uuid.NewString(), "", "wish", w.ID, false)
		must(e)
		if state.Favorited {
			t.Fatal("失效收藏无法取消")
		}
	})
}
