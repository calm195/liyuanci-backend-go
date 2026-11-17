package httpapi

import (
	"io"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// RegisterAdminWishContent 注册心愿筛选、详情和公众号草稿导出。
func RegisterAdminWishContent(r *gin.Engine, review *moderation.Service, wishes *wish.Service) error {
	s, err := moderation.NewAdminWishContent(review, wishes)
	if err != nil {
		return err
	}
	return registerAdminContent(r, "/api/v1/admin/content/wishes", s)
}

// RegisterAdminWhisperContent 注册树洞筛选、评论查询和公众号草稿导出。
func RegisterAdminWhisperContent(r *gin.Engine, review *moderation.Service, whispers *whisper.Service) error {
	s, err := moderation.NewAdminWhisperContent(review, whispers)
	if err != nil {
		return err
	}
	return registerAdminContent(r, "/api/v1/admin/content/whispers", s)
}

func registerAdminContent(r *gin.Engine, path string, service *moderation.AdminContent) error {
	if r == nil {
		return command.ErrInvalid
	}
	group := r.Group(path)
	group.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	})
	group.GET("", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, err := parseAdminContentQuery(c.Request.URL.RawQuery)
		if err != nil {
			adminError(c, err)
			return
		}
		out, err := service.List(c.Request.Context(), token, c.GetString("request_id"), q)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, 200, out)
	})
	group.GET("/:id", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, err := url.ParseQuery(c.Request.URL.RawQuery)
		page := 0
		includeStats := false
		if err == nil {
			for key, values := range q {
				if len(values) != 1 {
					err = command.ErrInvalid
					break
				}
				switch key {
				case "comments_page":
					page, err = strconv.Atoi(values[0])
				case "include":
					includeStats = values[0] == "stats"
					if !includeStats {
						err = command.ErrInvalid
					}
				default:
					err = command.ErrInvalid
				}
				if err != nil {
					break
				}
			}
		}
		if err != nil {
			adminError(c, command.ErrInvalid)
			return
		}
		out, err := service.DetailWithStats(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"), page, includeStats)
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, 200, out)
	})
	group.POST("/:id/export", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1))
		if c.Request.URL.RawQuery != "" || err != nil || len(body) != 0 {
			adminError(c, command.ErrInvalid)
			return
		}
		out, err := service.Export(c.Request.Context(), token, c.GetString("request_id"), c.Param("id"))
		if err != nil {
			adminError(c, err)
			return
		}
		adminResult(c, 200, gin.H{"markdown": out})
	})
	return nil
}

func parseAdminContentQuery(raw string) (out moderation.AdminContentQuery, err error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return out, command.ErrInvalid
	}
	for key, values := range q {
		if len(values) != 1 {
			return out, command.ErrInvalid
		}
		value := values[0]
		switch key {
		case "include":
			if value != "stats" {
				return out, command.ErrInvalid
			}
			out.IncludeStats = true
		case "keyword":
			out.Keyword = value
		case "state":
			out.State = value
		case "requester":
			out.Requester = value
		case "helper":
			out.Helper = value
		case "cursor":
			out.Cursor = value
		case "from":
			out.From, err = time.Parse(time.RFC3339, value)
		case "until":
			out.Until, err = time.Parse(time.RFC3339, value)
		default:
			return out, command.ErrInvalid
		}
		if err != nil {
			return out, command.ErrInvalid
		}
	}
	return out, nil
}
