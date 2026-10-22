package wish

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// storyReadDone 在查询异常恢复后仍记录失败完成日志，避免把panic记为成功。
func (s *Service) storyReadDone(op, rid string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	s.queryDone(op, rid, start, err)
}

func (s *Service) publicStory(ctx context.Context, db *gorm.DB, id string) (out PublicContent, err error) {
	a, e := readAggregate(ctx, db, id, false)
	if e != nil {
		return out, e
	}
	if a.row.CurrentStoryVersion == 0 || a.row.State != "fulfilled" || a.snapshot.PrivacyHeld {
		return out, command.ErrNotFound
	}
	v, e := storyRow(ctx, db, id, a.row.CurrentStoryVersion)
	if e != nil {
		return out, e
	}
	if v.ReviewTaskID == nil || v.PublicBody == nil || !utf8.ValidString(*v.PublicBody) || strings.TrimSpace(*v.PublicBody) == "" || v.ModerationStatus != "approved" || v.SchoolRelated == nil || v.SchoolDetectionState != "completed" || v.SchoolDetectionVersion == "pending" {
		return out, command.ErrNotFound
	}
	var t entity.ModerationTask
	if e = db.WithContext(ctx).Where("id=?", *v.ReviewTaskID).Take(&t).Error; e != nil {
		return out, e
	}
	if t.Status != "approved" || t.Decision == nil || *t.Decision != "approved" || *v.SchoolRelated && t.ReviewerID == nil {
		return out, command.ErrNotFound
	}
	ok, e := StoryEligible(ctx, db, a.row, v, t)
	if e != nil {
		return out, e
	}
	holds, e := ReadStoryHolds(ctx, db, id)
	if e != nil {
		return out, e
	}
	if !ok || holds.Active || holds.ParentActive {
		return out, command.ErrNotFound
	}
	var c entity.ContentRegistry
	if e = db.WithContext(ctx).Where("id=? AND kind='story' AND entity_id=? AND content_version=?", t.ContentID, id, v.StoryVersion).Take(&c).Error; e != nil {
		return out, e
	}
	if c.Status != "approved" || c.SchoolRelated == nil || *c.SchoolRelated != *v.SchoolRelated || c.SchoolDetectionState != "completed" || c.SchoolDetectionVersion != v.SchoolDetectionVersion {
		return out, command.ErrNotFound
	}
	var tags IdentityTags
	if !a.account.HideIdentityTags {
		if json.Unmarshal(v.IdentityTags, &tags) != nil {
			return out, command.ErrUnavailable
		}
	}
	out = PublicContent{ID: id, Body: *v.PublicBody, ContentVersion: v.StoryVersion, SchoolRelated: *v.SchoolRelated, NoticeCodes: []string{}, Author: PublicAuthor{ID: a.account.ID, Nickname: a.account.Nickname, IdentityTags: tags}, Category: a.row.Category, Campus: a.row.Campus, RewardMethod: a.content.RewardMethod}
	if out.SchoolRelated {
		out.NoticeCodes = []string{"UNVERIFIED_SCHOOL"}
		out.NoticeText = "未经证实"
	}
	return out, nil
}

// Story 只返回当前可公开的故事；作者身份也不能通过公开入口读取待审或被屏障内容。
func (s *Service) Story(ctx context.Context, token, rid, id string) (out PublicContent, err error) {
	defer func() {
		if err != nil {
			out = PublicContent{}
		}
	}()
	defer s.storyReadDone("story.detail", rid, time.Now(), &err)
	if !validWishID(id) {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		if token != "" {
			if _, e := currentReader(ctx, db, token, now); e != nil {
				return e
			}
		}
		var e error
		out, e = s.publicStory(ctx, db, id)
		return e
	})
	return
}

// Stories 按当前稿件时间与标识降序分页，同一快照中复核资格，游标不携带正文。
func (s *Service) Stories(ctx context.Context, rid, cursor string, limit int) (out PublicPage, err error) {
	defer func() {
		if err != nil {
			out = PublicPage{}
		}
	}()
	defer s.storyReadDone("stories.list", rid, time.Now(), &err)
	if limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	after, e := s.decodeCursor(cursor, "stories/public")
	if e != nil {
		return out, e
	}
	if after.ID != "" && after.PublishedAt.IsZero() {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, _ time.Time) error {
		out.Items = []PublicContent{}
		positions := []pageCursor{}
		for len(out.Items) <= limit {
			q := db.Table("public.wishes AS w").Select("w.id,v.created_at AS position").Joins("JOIN public.story_versions v ON v.wish_id=w.id AND v.story_version=w.current_story_version").Where("w.state='fulfilled' AND w.current_story_version>0 AND w.draft_deleted_at IS NULL")
			if after.ID != "" {
				q = q.Where("v.created_at<? OR (v.created_at=? AND w.id<?)", after.PublishedAt, after.PublishedAt, after.ID)
			}
			var rows []struct {
				ID       string
				Position time.Time
			}
			if e := q.Order("v.created_at DESC,w.id DESC").Limit(100).Scan(&rows).Error; e != nil {
				return e
			}
			if len(rows) == 0 {
				break
			}
			for _, w := range rows {
				after = pageCursor{Scope: "stories/public", ID: w.ID, PublishedAt: w.Position}
				item, e := s.publicStory(ctx, db, w.ID)
				if errors.Is(e, command.ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				out.Items = append(out.Items, item)
				positions = append(positions, after)
				if len(out.Items) > limit {
					break
				}
			}
			if len(rows) < 100 {
				break
			}
		}
		if len(out.Items) > limit {
			next := s.encodeCursor(positions[limit-1])
			out.NextCursor = &next
			out.Items = out.Items[:limit]
		}
		return nil
	})
	return
}

// ShareInput 只指定当前公开内容目标，不能提交正文快照或客户端路由。
type ShareInput struct {
	TargetKind string `json:"target_kind"`
	ID         string `json:"id"`
}

// ShareLink 返回独立随机引用与小程序路由；实际微信能力由客户端集成提供。
type ShareLink struct {
	Ref          string `json:"ref"`
	MiniappRoute string `json:"miniapp_route"`
}

// ShareTarget 在打开分享时重新读取权威投影，不复用创建时内容。
type ShareTarget struct {
	TargetKind string        `json:"target_kind"`
	Target     PublicContent `json:"target"`
}

func (s *Service) shareTarget(ctx context.Context, db *gorm.DB, kind, id, rid string, now time.Time) (out PublicContent, err error) {
	if kind == "story" {
		return s.publicStory(ctx, db, id)
	}
	a, e := readAggregate(ctx, db, id, false)
	if e != nil {
		return out, e
	}
	return s.publicContent(rid, now, a)
}

// CreateShare 要求当前普通会话及公开目标，重放仍重新授权，不新建重复引用。
func (s *Service) CreateShare(ctx context.Context, token, key, rid string, in ShareInput) (out ShareLink, err error) {
	defer func() {
		if err != nil {
			out = ShareLink{}
		}
	}()
	defer s.storyDone("share.create", rid, time.Now(), &err)
	if !validWishID(in.ID) || !validCommandKey(key) || (in.TargetKind != "wish" && in.TargetKind != "story") {
		return out, command.ErrInvalid
	}
	id := uuid.NewString()
	d := command.Definition{Kind: "share.create", Method: "POST", ResourcePath: "/api/v1/share-links", ActorKind: "student", Access: command.Ordinary, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		if in.TargetKind == "story" {
			return DiscoverStory(ctx, db, in.ID)
		}
		return discoverWish(ctx, db, in.ID)
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		_, e := s.shareTarget(ctx, tx.DB(), in.TargetKind, in.ID, rid, tx.Now())
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		return id, tx.DB().WithContext(ctx).Create(&entity.ShareLink{ID: id, TargetKind: in.TargetKind, TargetID: in.ID, CreatedAt: tx.Now()}).Error
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var row entity.ShareLink
		if e := tx.DB().WithContext(ctx).Where("id=? AND target_kind=? AND target_id=?", ref, in.TargetKind, in.ID).Take(&row).Error; e != nil {
			return e
		}
		out = ShareLink{Ref: row.ID, MiniappRoute: "/pages/share/index?ref=" + row.ID}
		return nil
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: in})
	return
}

// ResolveShare 重新验证当前公开资格；下线、过期、撤回、注销或屏障后立即不可访问。
func (s *Service) ResolveShare(ctx context.Context, token, rid, ref string) (out ShareTarget, err error) {
	defer func() {
		if err != nil {
			out = ShareTarget{}
		}
	}()
	defer s.storyReadDone("share.resolve", rid, time.Now(), &err)
	if !validWishID(ref) {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		if token != "" {
			if _, e := currentReader(ctx, db, token, now); e != nil {
				return e
			}
		}
		var row entity.ShareLink
		if e := db.Where("id=?", ref).Take(&row).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrNotFound
			}
			return e
		}
		target, e := s.shareTarget(ctx, db, row.TargetKind, row.TargetID, rid, now)
		if e != nil {
			return e
		}
		out = ShareTarget{row.TargetKind, target}
		return nil
	})
	return
}
