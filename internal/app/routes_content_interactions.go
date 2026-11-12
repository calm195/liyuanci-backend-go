package app

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/modules/contentinteraction"
	"github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

// registerContentInteractions 通过领域当前读取投影装配跨内容入口，资料资格仍由资料模块执行。
func registerContentInteractions(router *gin.Engine, public *PublicContentOptions, s *serviceSet, d ServiceDependencies, cfg ServiceConfig) error {
	resolve := func(ctx context.Context, token, rid, kind, id string) (out contentinteraction.Target, err error) {
		switch kind {
		case "wish":
			row, e := s.wishes.PublicInteractionContent(ctx, token, rid, id)
			if e != nil {
				return out, e
			}
			out.Title = row.Title
			out.Excerpt = excerpt(row.Body)
		case "whisper_post":
			if public.whispers == nil {
				return out, command.ErrUnavailable
			}
			raw, e := public.whispers.GetPost(ctx, token, rid, id, false)
			if e != nil {
				return out, e
			}
			row, ok := raw.(whisper.PublicPost)
			if !ok {
				return out, command.ErrUnavailable
			}
			out.Title = row.Title
			out.Excerpt = excerpt(row.Body)
		case "resource":
			if s.studentResources == nil {
				return out, command.ErrUnavailable
			}
			row, e := s.studentResources.Read(ctx, token, rid, "detail", id, resourceplaza.Query{}, false)
			if e != nil {
				return out, e
			}
			fields, ok := row.(map[string]any)
			if !ok {
				return out, command.ErrUnavailable
			}
			out.Title, _ = fields["title"].(string)
			body, _ := fields["description"].(string)
			out.Excerpt = excerpt(body)
		default:
			return out, command.ErrInvalid
		}
		return
	}
	resolveTx := func(ctx context.Context, db *gorm.DB, now time.Time, actor, kind, id string) (out contentinteraction.Target, err error) {
		switch kind {
		case "wish":
			row, e := s.wishes.ReadInteractionContent(ctx, db, now, id)
			if e != nil {
				return out, e
			}
			out.Title = row.Title
			out.Excerpt = excerpt(row.Body)
		case "whisper_post":
			if public.whispers == nil {
				return out, command.ErrUnavailable
			}
			row, e := public.whispers.ReadPublicationPost(ctx, db, now, id)
			if e != nil {
				return out, e
			}
			out.Title = row.Title
			out.Excerpt = excerpt(row.Body)
		case "resource":
			if s.studentResources == nil {
				return out, command.ErrUnavailable
			}
			row, e := s.studentResources.ReadInteractionContent(ctx, db, now, actor, id)
			if e != nil {
				return out, e
			}
			out.Title = row.Title
			out.Excerpt = excerpt(row.Description)
		default:
			return out, command.ErrInvalid
		}
		return
	}
	service, e := contentinteraction.New(d.WishDB, d.Logger, cfg.Wish.Command, cfg.Wish.ReceiptKey, resolve, resolveTx)
	if e != nil {
		return e
	}
	httpapi.RegisterContentInteractions(router, service)
	return nil
}
func excerpt(body string) string { r := []rune(body); return string(r[:min(len(r), 120)]) }
