package whisper

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func (s *Service) ownerPost(ctx context.Context, db *gorm.DB, now time.Time, id, owner string) (out OwnerPost, err error) {
	a, e := loadPost(ctx, db, id)
	if e != nil {
		return out, e
	}
	if a.post.OwnerID != owner {
		return out, command.ErrNotFound
	}
	state := postState(a)
	out = OwnerPost{ID: id, Version: a.post.Version, ContentVersion: a.post.CurrentContentVersion, Status: state, CreatedAt: timestamp(a.post.CreatedAt), UpdatedAt: timestamp(a.post.UpdatedAt), AllowedActions: []string{}, CommentsOpen: a.post.CommentsOpen, CommentsEpoch: a.post.CommentsEpoch}
	if a.post.PublishedAt != nil {
		v := timestamp(*a.post.PublishedAt)
		out.PublishedAt = &v
	}
	if !slices.Contains([]string{"privacy_hold", "isolated", "restricted", "published"}, state) {
		var body string
		var identity IdentityInput
		if e = s.decrypt(ctx, "whisper_post_body", id, a.post.CurrentContentVersion, a.version.BodyCipher, &body); e != nil {
			return OwnerPost{}, e
		}
		if e = s.decrypt(ctx, "whisper_post_identity", id, a.post.CurrentContentVersion, a.version.IdentityCipher, &identity); e != nil {
			return OwnerPost{}, e
		}
		out.Body = &body
		out.IdentityInput = &identity
	}
	if a.version.ReviewTaskID != nil {
		var task entity.ModerationTask
		e = reviewTask(ctx, db, *a.version.ReviewTaskID, a.version.ContentRegistryID, a.post.CurrentContentVersion, &task)
		if e != nil {
			return OwnerPost{}, e
		}
		decision := "pending"
		if slices.Contains([]string{"approved", "rejected", "cancelled"}, task.Status) {
			decision = task.Status
		}
		out.ReviewResult = &ReviewResult{Decision: decision, ContentVersion: a.post.CurrentContentVersion}
		if decision == "rejected" {
			if task.Reason == nil || task.RevisionSuggestion == nil {
				return OwnerPost{}, command.ErrUnavailable
			}
			out.ReviewResult.Reason = task.Reason
			out.ReviewResult.RevisionSuggestion = task.RevisionSuggestion
		}
	}
	out.AllowedActions = append(out.AllowedActions, "whisperPostWithdraw", "whisperCommentsSetting")
	limited, e := restrictedAccount(ctx, db, owner, now)
	if e != nil {
		return OwnerPost{}, e
	}
	if !limited && a.owner.Verification == "verified" && !slices.Contains([]string{"privacy_hold", "isolated", "restricted"}, state) {
		out.AllowedActions = append(out.AllowedActions, "whisperPostUpdate")
		if s.review != nil && slices.Contains([]string{"draft", "rejected", "withdrawn", "cancelled"}, state) {
			out.AllowedActions = append(out.AllowedActions, "whisperPostSubmit")
		}
	}
	return out, nil
}

func (s *Service) publicPostBase(ctx context.Context, db *gorm.DB, now time.Time, id string, viewer audience, c CatalogSnapshot) (out PublicPost, err error) {
	a, e := loadPost(ctx, db, id)
	if e != nil {
		return out, e
	}
	if postState(a) != "published" || a.owner.Lifecycle != "active" || a.owner.Verification != "verified" || a.version.ReviewState != "approved" || a.version.PublicBody == nil || a.version.SchoolRelated == nil || a.post.PublishedAt == nil {
		return out, command.ErrNotFound
	}
	limited, e := restrictedAccount(ctx, db, a.owner.ID, now)
	if e != nil {
		return out, e
	}
	if limited {
		return out, command.ErrNotFound
	}
	var identity PublicIdentity
	if json.Unmarshal(a.version.PublicIdentity, &identity) != nil || !slices.Contains([]string{"anonymous", "semi", "named"}, identity.Mode) || identity.Mode != a.version.IdentityMode {
		return out, command.ErrNotFound
	}
	identity.IsAuthor = true
	switch identity.Mode {
	case "anonymous":
		identity.Label = a.alias
		identity.Tags = nil
	case "semi":
		if a.owner.HideIdentityTags || !tagsValid(identity.Tags, c.Grades) {
			identity = PublicIdentity{Mode: "anonymous", Label: a.alias, IsAuthor: true}
		} else {
			identity.Label = a.alias
		}
	case "named":
		if a.version.NicknameVersion == nil || !validText(identity.Label, 2, 20) {
			return out, command.ErrNotFound
		}
		identity.Tags = nil
	}
	codes := []string{}
	var notice *string
	if *a.version.SchoolRelated {
		codes = append(codes, "UNVERIFIED_SCHOOL")
		v := "未经证实"
		notice = &v
	}
	out = PublicPost{ID: id, Version: a.post.Version, ContentVersion: a.post.CurrentContentVersion, Body: *a.version.PublicBody, Identity: identity, PublishedAt: timestamp(*a.post.PublishedAt), SchoolRelated: *a.version.SchoolRelated, NoticeCodes: codes, NoticeText: notice, ViewerIsAuthor: viewer.ID == a.post.OwnerID, AllowedActions: []string{}, CommentsOpen: a.post.CommentsOpen, CommentsEpoch: a.post.CommentsEpoch}
	return out, nil
}

func (s *Service) publicPost(ctx context.Context, db *gorm.DB, now time.Time, id string, viewer audience, c CatalogSnapshot) (out PublicPost, err error) {
	out, err = s.publicPostBase(ctx, db, now, id, viewer, c)
	var e error
	if err != nil {
		return
	}
	if err = visibleCommentsQuery(ctx, db, id, out.ContentVersion, now).Count(&out.CommentCount).Error; err != nil {
		return PublicPost{}, err
	}
	if e = countHugs(ctx, db, id, &out.HugCount); e != nil {
		return PublicPost{}, e
	}
	if viewer.ID != "" {
		out.MyHug = &HugState{Active: false, Version: 0}
		var hugs []entity.WhisperHug
		if e = viewerHugs(ctx, db, id, viewer.ID, &hugs); e != nil {
			return PublicPost{}, e
		}
		if len(hugs) == 1 {
			out.MyHug = &HugState{Active: hugs[0].Active, Version: hugs[0].Version}
		}
	}

	if viewer.ID != "" && !viewer.Restricted {
		if out.CommentsOpen {
			out.AllowedActions = append(out.AllowedActions, "whisperCommentCreate")
		}
		if !out.ViewerIsAuthor {
			out.AllowedActions = append(out.AllowedActions, "whisperHugSet", "whisperReportCreate")
		}
	}
	return out, nil
}

// GetPost 公开与本人分别投影；非法或已失效会话不能降级成游客。
func (s *Service) GetPost(ctx context.Context, token, rid, id string, own bool) (out any, err error) {
	defer s.completed("post_get", rid, time.Now(), &err)
	if !s.config.Enabled {
		return nil, ErrModuleUnavailable
	}
	if !ValidID(id) {
		return nil, command.ErrInvalid
	}
	c, e := s.resources(ctx)
	if e != nil {
		return nil, e
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		viewer, e := reader(ctx, db, token, now, !own)
		if e != nil {
			return e
		}
		if own {
			out, e = s.ownerPost(ctx, db, now, id, viewer.ID)
		} else {
			out, e = s.publicPost(ctx, db, now, id, viewer, c)
		}
		return e
	})
	if err != nil {
		out = nil
	}
	return
}

// ListPosts 以有界候选扫描推进，不用过滤后的最后一条生成游标。
func (s *Service) ListPosts(ctx context.Context, token, rid string, own bool, q Query) (out any, err error) {
	defer s.completed("posts_list", rid, time.Now(), &err)
	if !s.config.Enabled {
		return nil, ErrModuleUnavailable
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 {
		return nil, command.ErrInvalid
	}
	c, e := s.resources(ctx)
	if e != nil {
		return nil, e
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		viewer, e := reader(ctx, db, token, now, !own)
		if e != nil {
			return e
		}
		kind := "public"
		if own {
			kind = "own"
		}
		scope := s.scope(kind, viewer.ID)
		position, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		candidates, e := postCandidates(ctx, db, own, viewer.ID, position, q.Limit+1)
		if e != nil {
			return e
		}
		more := len(candidates) > q.Limit
		if more {
			candidates = candidates[:q.Limit]
		}
		ownPage := Page[OwnerPost]{Items: []OwnerPost{}}
		publicPage := Page[PublicPost]{Items: []PublicPost{}}
		for _, p := range candidates {
			if own {
				v, e := s.ownerPost(ctx, db, now, p.ID, viewer.ID)
				if e != nil {
					if errors.Is(e, command.ErrNotFound) {
						continue
					}
					return e
				}
				ownPage.Items = append(ownPage.Items, v)
			} else {
				v, e := s.publicPost(ctx, db, now, p.ID, viewer, c)
				if e != nil {
					if errors.Is(e, command.ErrNotFound) {
						continue
					}
					return e
				}
				publicPage.Items = append(publicPage.Items, v)
			}
		}
		if more && len(candidates) > 0 {
			last := candidates[len(candidates)-1]
			at := last.CreatedAt
			if !own {
				at = *last.PublishedAt
			}
			next := s.encodeCursor(cursor{Scope: scope, ID: last.ID, At: at})
			ownPage.NextCursor = &next
			publicPage.NextCursor = &next
		}
		if own {
			out = ownPage
		} else {
			out = publicPage
		}
		return nil
	})
	if err != nil {
		out = nil
	}
	return
}

func (s *Service) nicknameState(ctx context.Context, db *gorm.DB, now time.Time, owner string) (out NicknameState, err error) {
	p, e := preferences(ctx, db, owner)
	if e != nil {
		return out, e
	}
	out = NicknameState{Version: p.Version, AllowedActions: []string{}}
	limited, e := restrictedAccount(ctx, db, owner, now)
	if e != nil {
		return out, e
	}
	if !limited && s.review != nil {
		out.AllowedActions = append(out.AllowedActions, "whisperNicknameSet")
	}
	if p.ActiveNicknameVersion != nil {
		n, e := nicknameRow(ctx, db, owner, *p.ActiveNicknameVersion)
		if e != nil {
			return out, e
		}
		safe, e := nicknameSafe(ctx, db, n)
		if e != nil {
			return out, e
		}
		if safe {
			out.Active = &NicknameLabel{NicknameVersion: n.NicknameVersion, Label: *n.PublicLabel}
		}
	}
	if p.CandidateNicknameVersion != nil {
		n, e := nicknameRow(ctx, db, owner, *p.CandidateNicknameVersion)
		if e != nil {
			return out, e
		}
		// 被撤销的昵称不回显违规原稿，避免利用本人候选绕过屏障。
		safe, e := nicknameSafe(ctx, db, n)
		if e != nil {
			return out, e
		}
		unblocked, e := nicknameUnblocked(ctx, db, n)
		if e != nil {
			return out, e
		}
		if n.RevokedAt != nil || !unblocked || n.ReviewState == "approved" && !safe {
			return out, nil
		}
		var label string
		if e = s.decrypt(ctx, "whisper_nickname", n.ContentRegistryID, n.NicknameVersion, n.LabelCipher, &label); e != nil {
			return out, e
		}
		state := n.ReviewState
		if state == "pending" {
			state = "review_pending"
		}
		if slices.Contains([]string{"scanning", "review_pending", "approved", "rejected"}, state) {
			candidate := &NicknameCandidate{NicknameLabel: NicknameLabel{NicknameVersion: n.NicknameVersion, Label: label}, Status: state}
			if n.ReviewTaskID != nil {
				var task entity.ModerationTask
				if e = reviewTask(ctx, db, *n.ReviewTaskID, n.ContentRegistryID, n.NicknameVersion, &task); e != nil {
					return out, e
				}
				decision := "pending"
				if slices.Contains([]string{"approved", "rejected", "cancelled"}, task.Status) {
					decision = task.Status
				}
				candidate.ReviewResult = &ReviewResult{Decision: decision, ContentVersion: n.NicknameVersion}
				if decision == "rejected" {
					if task.Reason == nil || task.RevisionSuggestion == nil {
						return out, command.ErrUnavailable
					}
					candidate.ReviewResult.Reason = task.Reason
					candidate.ReviewResult.RevisionSuggestion = task.RevisionSuggestion
				}
			}
			out.Candidate = candidate
		}
	}
	return
}

// GetNickname 在只读快照内返回逻辑初始偏好或当前候选／active。
func (s *Service) GetNickname(ctx context.Context, token, rid string) (out NicknameState, err error) {
	defer s.completed("nickname_get", rid, time.Now(), &err)
	if !s.config.Enabled {
		return out, ErrModuleUnavailable
	}
	err = s.transaction(ctx, func(db *gorm.DB, now time.Time) error {
		a, e := reader(ctx, db, token, now, false)
		if e != nil {
			return e
		}
		out, e = s.nicknameState(ctx, db, now, a.ID)
		return e
	})
	return
}
