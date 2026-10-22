package wish

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// ReviewResult 仅描述当前送审轮次，不把审核通过当成业务上架。
type ReviewResult struct {
	Decision           string  `json:"decision"`
	ContentVersion     int64   `json:"content_version"`
	Reason             *string `json:"reason,omitempty"`
	RevisionSuggestion *string `json:"revision_suggestion,omitempty"`
}

// Result 按当前权限选择互斥的本人或公开视图；从不回退到旧版本正文。
type Result struct {
	ID                    string         `json:"id"`
	Version               int64          `json:"version"`
	ContentVersion        int64          `json:"content_version"`
	State                 State          `json:"current_state"`
	AllowedActions        []string       `json:"allowed_actions"`
	ExpiresAt             *time.Time     `json:"expires_at"`
	PrivacyReviewRequired bool           `json:"privacy_review_required,omitempty"`
	OwnContent            *ContentInput  `json:"own_content,omitempty"`
	Public                *PublicContent `json:"public,omitempty"`
	ReviewResult          *ReviewResult  `json:"review_result,omitempty"`
}

// PublicAuthor 仅含公开账号标识、昵称和当前允许的已审标签。
type PublicAuthor struct {
	ID           string       `json:"id"`
	Nickname     string       `json:"nickname"`
	IdentityTags IdentityTags `json:"identity_tags"`
}

// PublicContent 只组合当前已审投影和公开元数据，不包含作者私有身份或原始密文。
type PublicContent struct {
	ID             string       `json:"id"`
	Body           string       `json:"body"`
	ContentVersion int64        `json:"content_version"`
	SchoolRelated  bool         `json:"school_related"`
	NoticeCodes    []string     `json:"notice_codes"`
	NoticeText     string       `json:"notice_text"`
	Author         PublicAuthor `json:"author"`
	Category       string       `json:"category"`
	Campus         string       `json:"campus"`
	RewardMethod   string       `json:"reward_method"`
	ExpiresAt      *time.Time   `json:"expires_at"`
}

// Query 约束公开筛选或本人状态筛选；游标绑定路由、主体及筛选，不能跨视图复用。
type Query struct {
	Category string
	Campus   string
	State    State
	Cursor   string
	Limit    int
}

// PublicPage 使用上架时间及随机ID的降序稳定游标。
type PublicPage struct {
	Items      []PublicContent `json:"items"`
	NextCursor *string         `json:"next_cursor"`
}

// OwnPage 使用本人随机ID降序游标，结果始终复核当前会话及隐私屏障。
type OwnPage struct {
	Items      []Result `json:"items"`
	NextCursor *string  `json:"next_cursor"`
}

type pageCursor struct {
	Scope       string    `json:"scope"`
	ID          string    `json:"id"`
	PublishedAt time.Time `json:"published_at"`
}

func (s *Service) encodeCursor(c pageCursor) string {
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.receiptKey)
	mac.Write([]byte("wish.cursor\x00"))
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Service) decodeCursor(value, scope string) (out pageCursor, err error) {
	if value == "" {
		return out, nil
	}
	if len(value) > 2048 {
		return out, command.ErrInvalid
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return out, command.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return out, command.ErrInvalid
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return out, command.ErrInvalid
	}
	mac := hmac.New(sha256.New, s.receiptKey)
	mac.Write([]byte("wish.cursor\x00"))
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(b, &out) != nil || out.Scope != scope || !validWishID(out.ID) {
		return pageCursor{}, command.ErrInvalid
	}
	return out, nil
}
func queryScope(kind, owner string, q Query) string {
	b, _ := json.Marshal([]any{kind, owner, q.Category, q.Campus, q.State})
	return string(b)
}
func validateQuery(q Query, own bool) error {
	if q.Limit < 1 || q.Limit > 100 {
		return command.ErrInvalid
	}
	if own {
		if q.Category != "" || q.Campus != "" || q.State != "" && !slices.Contains([]State{Draft, PendingReview, Rejected, Ready, Published, Matching, Realized, Fulfilled, Withdrawn, Expired}, q.State) {
			return command.ErrInvalid
		}
	} else {
		if q.State != "" || q.Category != "" && !slices.Contains([]string{"help", "team", "items", "dating", "skill_exchange", "other"}, q.Category) || q.Campus != "" && !campus(q.Campus) {
			return command.ErrInvalid
		}
	}
	return nil
}

func (s *Service) readTransaction(ctx context.Context, run func(*gorm.DB, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	// 可重复读保证会话、当前版本、审核绑定和屏障来自同一数据库快照，不拼接不同时刻的权限。
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		return run(db, now)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
}
func currentReader(ctx context.Context, db *gorm.DB, token string, now time.Time) (string, error) {
	if token == "" {
		return "", command.ErrUnauthenticated
	}
	h := sha256.Sum256([]byte(token))
	var rows []struct{ ID string }
	e := db.WithContext(ctx).Model(&entity.Session{}).Table("public.sessions AS s").Select("a.id").Joins("JOIN public.accounts a ON a.id=s.account_id").Where("s.subject_kind='student' AND s.token_hash=? AND s.auth_epoch=a.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>? AND a.lifecycle='active'", h[:], now).Find(&rows).Error
	if e != nil {
		return "", e
	}
	if len(rows) != 1 {
		return "", command.ErrUnauthenticated
	}
	return rows[0].ID, nil
}
func readError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{command.ErrInvalid, command.ErrUnauthenticated, command.ErrNotFound, command.ErrForbidden, command.ErrInternal} {
		if errors.Is(err, known) {
			return known
		}
	}
	return command.ErrUnavailable
}
func (s *Service) queryDone(operation, rid string, start time.Time, err *error) {
	*err = readError(*err)
	s.policy.finish("心愿查询操作完成", operation, rid, start, *err)
}

func (s *Service) publicContent(rid string, now time.Time, a aggregate) (out PublicContent, err error) {
	p, e := s.policy.Public(rid, now, a.owner, a.snapshot)
	if e != nil {
		return out, e
	}
	// 已审标签仍须属于批准枚举；年级由审核发布时校验，不在游客读取时调用外部字典。
	if p.IdentityTags.Grade != nil && strings.TrimSpace(*p.IdentityTags.Grade) == "" {
		return out, command.ErrNotFound
	}
	if p.IdentityTags.Campus != nil && !campus(*p.IdentityTags.Campus) || p.IdentityTags.Gender != nil && !slices.Contains([]string{"male", "female", "undisclosed"}, *p.IdentityTags.Gender) {
		return out, command.ErrNotFound
	}
	return PublicContent{ID: a.row.ID, Body: p.Body, ContentVersion: a.row.CurrentContentVersion, SchoolRelated: *a.snapshot.Review.SchoolRelated, NoticeCodes: p.NoticeCodes, NoticeText: p.NoticeText, Author: PublicAuthor{ID: a.row.OwnerID, Nickname: a.account.Nickname, IdentityTags: p.IdentityTags}, Category: a.row.Category, Campus: a.row.Campus, RewardMethod: a.content.RewardMethod, ExpiresAt: a.row.ExpiresAt}, nil
}
func effectiveState(a aggregate, now time.Time) State {
	if a.snapshot.State == Published && a.row.ExpiresAt != nil && !now.Before(*a.row.ExpiresAt) {
		return Expired
	}
	return a.snapshot.State
}
func (s *Service) ownResult(ctx context.Context, db *gorm.DB, rid string, now time.Time, a aggregate) (out Result, err error) {
	out = Result{ID: a.row.ID, Version: a.row.Version, ContentVersion: a.row.CurrentContentVersion, State: effectiveState(a, now), AllowedActions: []string{}, ExpiresAt: a.row.ExpiresAt, PrivacyReviewRequired: a.snapshot.PrivacyHeld}
	if a.task != nil {
		decision := "pending"
		if a.task.Status == "approved" || a.task.Status == "rejected" {
			decision = a.task.Status
		}
		out.ReviewResult = &ReviewResult{Decision: decision, ContentVersion: a.content.ContentVersion}
		if decision == "rejected" {
			out.ReviewResult.Reason = a.task.Reason
			out.ReviewResult.RevisionSuggestion = a.task.RevisionSuggestion
		}
	}
	if !a.snapshot.PrivacyHeld {
		var envelope securedata.Envelope
		if json.Unmarshal(a.content.BodyCipher, &envelope) != nil {
			return Result{}, command.ErrUnavailable
		}
		plain, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "wish_content", ObjectID: a.row.ID, Version: uint64(a.content.ContentVersion)}, envelope)
		if e != nil {
			return Result{}, command.ErrUnavailable
		}
		defer clear(plain)
		var c Content
		if json.Unmarshal(plain, &c) != nil {
			return Result{}, command.ErrUnavailable
		}
		out.OwnContent = &ContentInput{Category: c.Category, Body: c.Body, Campus: c.Campus, IdentityTags: &c.IdentityTags, RewardMethod: &c.RewardMethod, DurationDays: &c.DurationDays}
	}
	if ordinary(a.owner, a.snapshot) {
		for _, candidate := range []struct {
			action Action
			code   string
		}{{Edit, "putWishesIdContent"}, {DeleteDraft, "deleteWishesIdDraft"}, {SubmitReview, "postWishesIdSubmitReview"}, {Publish, "postWishesIdPublish"}, {Withdraw, "postWishesIdWithdraw"}} {
			_, e := s.policy.Plan(rid, now, a.owner, a.snapshot, 0, Change{Action: candidate.action, ExpectedVersion: a.row.Version, ExpectedContentVersion: a.row.CurrentContentVersion, DurationDays: int(a.row.DurationDays)})
			if e == nil {
				out.AllowedActions = append(out.AllowedActions, candidate.code)
			}
		}
	}
	return out, nil
}

// Detail 对无token请求只给公开视图；显式坏token返回401，不降级游客绕过权限。
func (s *Service) Detail(ctx context.Context, token, rid, id string) (out Result, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		s.queryDone("detail", rid, start, &err)
		if err != nil {
			out = Result{}
		}
	}()
	if !validWishID(id) {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor := ""
		if token != "" {
			var e error
			actor, e = currentReader(ctx, db, token, now)
			if e != nil {
				return e
			}
		}
		a, e := readAggregate(ctx, db, id, false)
		if e != nil {
			return e
		}
		if actor == a.row.OwnerID {
			out, e = s.ownResult(ctx, db, rid, now, a)
			return e
		}
		p, e := s.publicContent(rid, now, a)
		if e != nil {
			return command.ErrNotFound
		}
		out = Result{ID: id, Version: a.row.Version, ContentVersion: a.row.CurrentContentVersion, State: Published, AllowedActions: []string{}, ExpiresAt: a.row.ExpiresAt, Public: &p}
		return nil
	})
	return out, err
}

// Own 列出当前本人状态，过期判断不等待补偿工作；隐私屏障生效时不解密旧正文。
func (s *Service) Own(ctx context.Context, token, rid string, q Query) (out OwnPage, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		s.queryDone("own_list", rid, start, &err)
		if err != nil {
			out = OwnPage{}
		}
	}()
	if e := validateQuery(q, true); e != nil {
		return out, e
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		owner, e := currentReader(ctx, db, token, now)
		if e != nil {
			return e
		}
		scope := queryScope("own", owner, q)
		cursor, e := s.decodeCursor(q.Cursor, scope)
		if e != nil {
			return e
		}
		rows := []entity.Wish{}
		query := db.Model(&entity.Wish{}).Where("owner_id=? AND draft_deleted_at IS NULL", owner)
		if q.State == Expired {
			query = query.Where("state='expired' OR (state='published' AND expires_at<=?)", now)
		} else if q.State == Published {
			query = query.Where("state='published' AND expires_at>?", now)
		} else if q.State != "" {
			query = query.Where("state=?", q.State)
		}
		if cursor.ID != "" {
			query = query.Where("id<?", cursor.ID)
		}
		if e = query.Order("id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		out.Items = []Result{}
		if len(rows) > q.Limit {
			next := s.encodeCursor(pageCursor{Scope: scope, ID: rows[q.Limit-1].ID})
			out.NextCursor = &next
			rows = rows[:q.Limit]
		}
		for _, r := range rows {
			a, e := readAggregate(ctx, db, r.ID, false)
			if e != nil {
				return e
			}
			result, e := s.ownResult(ctx, db, rid, now, a)
			if e != nil {
				return e
			}
			out.Items = append(out.Items, result)
		}
		return nil
	})
	return out, err
}

// List 游客广场只输出当前已审且未过期内容；过滤和游标都在可重复读快照内计算。
func (s *Service) List(ctx context.Context, rid string, q Query) (out PublicPage, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		s.queryDone("public_list", rid, start, &err)
		if err != nil {
			out = PublicPage{}
		}
	}()
	if e := validateQuery(q, false); e != nil {
		return out, e
	}
	scope := queryScope("public", "", q)
	cursor, e := s.decodeCursor(q.Cursor, scope)
	if e != nil {
		return out, e
	}
	if cursor.ID != "" && cursor.PublishedAt.IsZero() {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		out.Items = []PublicContent{}
		positions := []entity.Wish{}
		for len(out.Items) <= q.Limit {
			query := db.Model(&entity.Wish{}).Where("draft_deleted_at IS NULL AND state='published' AND expires_at>?", now)
			if q.Category != "" {
				query = query.Where("category=?", q.Category)
			}
			if q.Campus != "" {
				query = query.Where("campus=?", q.Campus)
			}
			if cursor.ID != "" {
				query = query.Where("published_at<? OR (published_at=? AND id<?)", cursor.PublishedAt, cursor.PublishedAt, cursor.ID)
			}
			var rows []entity.Wish
			if e := query.Order("published_at DESC,id DESC").Limit(100).Find(&rows).Error; e != nil {
				return e
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				cursor = pageCursor{Scope: scope, ID: row.ID, PublishedAt: *row.PublishedAt}
				a, e := readAggregate(ctx, db, row.ID, false)
				if e != nil {
					return e
				}
				item, e := s.publicContent(rid, now, a)
				if errors.Is(e, command.ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				out.Items = append(out.Items, item)
				positions = append(positions, row)
				if len(out.Items) > q.Limit {
					break
				}
			}
			if len(rows) < 100 {
				break
			}
		}
		if len(out.Items) > q.Limit {
			row := positions[q.Limit-1]
			next := s.encodeCursor(pageCursor{Scope: scope, ID: row.ID, PublishedAt: *row.PublishedAt})
			out.NextCursor = &next
			out.Items = out.Items[:q.Limit]
		}
		return nil
	})
	return out, err
}
