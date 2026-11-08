package moderation

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Export 为超级管理员生成已审公众号草稿，并在成功返回前写入内容访问审计。
func (s *AdminContent) Export(ctx context.Context, token, rid, id string) (out string, err error) {
	defer s.review.done("admin_content."+s.kind()+".export", rid, time.Now(), &err)
	if !validID(id) {
		return "", command.ErrInvalid
	}
	err = s.review.transaction(ctx, func(db *gorm.DB) error {
		ctx := db.Statement.Context
		actor, e := s.review.authorizeRoles(ctx, db, token, command.Discovery{}, []string{"super_admin"})
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		row, e := s.summary(db, id, now)
		if e != nil {
			return e
		}
		p, e := s.publication(ctx, db, now, rid, id)
		if errors.Is(e, command.ErrNotFound) || e == nil && !s.exportable(p) {
			return command.ErrConflict
		}
		if e != nil {
			return e
		}
		var markdown strings.Builder
		markdown.WriteString("# " + markdownLine(p.title) + "\n\n")
		if s.wishes != nil {
			if row.PublishedAt != nil {
				beijing := time.FixedZone("北京时间", 8*60*60)
				markdown.WriteString("上架日期 " + row.PublishedAt.In(beijing).Format(time.DateOnly) + "\n\n")
			}
			writePublicationSection(&markdown, "许愿", *p.original)
			markdown.WriteString("## 应愿与实现\n\n")
			if row.MatchedAt != nil {
				markdown.WriteString("心愿已获得应愿并完成。\n\n")
			} else {
				markdown.WriteString("心愿已完成。\n\n")
			}
			writePublicationSection(&markdown, "还愿故事", *p.story)
		} else {
			writePublicationSection(&markdown, "悄悄话", *p.original)
			const exportCommentLimit = 250
			comments, more, e := s.comments(ctx, db, now, id, 0, exportCommentLimit)
			if e != nil {
				return e
			}
			writePublicationComments(&markdown, comments, more)
		}
		if e = audit(db, actor, id, "admin_content."+s.kind()+".export"); e != nil {
			return e
		}
		out = markdown.String()
		return nil
	})
	if err != nil {
		return "", err
	}
	return
}

func writePublicationComments(out *strings.Builder, comments []AdminPublicationText, more bool) {
	if len(comments) > 0 {
		out.WriteString("## 暖评\n\n")
	}
	for _, item := range comments {
		writePublicationText(out, item)
	}
	if more {
		out.WriteString("其余公开暖评请在后台查看。\n")
	}
}

func writePublicationSection(out *strings.Builder, title string, item AdminPublicationText) {
	out.WriteString("## " + title + "\n\n")
	writePublicationText(out, item)
}

func writePublicationText(out *strings.Builder, item AdminPublicationText) {
	if item.Author != "" {
		out.WriteString(markdownLine(item.Author) + "\n\n")
	}
	if item.Notice != "" {
		out.WriteString(markdownLine(item.Notice) + "\n\n")
	}
	for _, line := range strings.Split(item.Body, "\n") {
		out.WriteString("> " + markdownLine(line) + "\n")
	}
	out.WriteString("\n")
}

var publicationEscapes = strings.NewReplacer(
	"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "{", "\\{", "}", "\\}",
	"[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;", "#", "\\#", "|", "\\|",
	"!", "\\!", "~", "\\~", "&", "&amp;", "\r", "", "\n", " ",
)

func markdownLine(value string) string {
	return publicationEscapes.Replace(value)
}
