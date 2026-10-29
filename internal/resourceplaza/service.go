package resourceplaza

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"
)

// 分类特定冲突供HTTP边界明确提示，不能降级为空页。
var (
	ErrCategoryInUse = errors.New("分类仍有资料引用")
	ErrCategoryName  = errors.New("分类名称已存在")
)

// Config 显式指定事务期限和独立游标密钥，不含默认生产凭据。
type Config struct {
	Command command.Config
	Key     []byte
}

// Service 使用受限业务数据库和注入加密器；不负责迁移或外链请求。
type Service struct {
	db    *gorm.DB
	codec *securedata.Codec
	log   *zap.Logger
	cfg   Config
}

// DeriveKey 将根密钥隔离到资料游标用途。
func DeriveKey(root []byte) []byte {
	m := hmac.New(sha256.New, root)
	_, _ = m.Write([]byte("resource-plaza.cursor.v1"))
	return m.Sum(nil)
}

// New 验证依赖但不执行DDL或建立业务数据。
func New(db *gorm.DB, log *zap.Logger, codec *securedata.Codec, cfg Config) (*Service, error) {
	if db == nil || log == nil || codec == nil || len(cfg.Key) != 32 || cfg.Command.Timeout <= 0 || cfg.Command.LockTimeout < time.Millisecond {
		return nil, command.ErrInvalid
	}
	cfg.Key = append([]byte(nil), cfg.Key...)
	return &Service{db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), codec, log, cfg}, nil
}

type actor struct {
	ID           string
	Admin, Muted bool
	Now          time.Time
}

func (s *Service) done(op, rid string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	s.log.Info("资料请求完成", zap.String("operation", op), zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
func normalizeError(e error) error {
	if e == nil {
		return nil
	}
	for _, v := range []error{command.ErrRetry, command.ErrInvalid, command.ErrUnauthenticated, command.ErrForbidden, command.ErrVerificationRequired, command.ErrNotFound, command.ErrVersionConflict, command.ErrIdempotencyConflict, command.ErrConflict, command.ErrUnavailable, ErrCategoryInUse, ErrCategoryName} {
		if errors.Is(e, v) {
			return v
		}
	}

	var state interface{ SQLState() string }
	if errors.As(e, &state) && slices.Contains([]string{"40001", "40P01", "55P03"}, state.SQLState()) {
		return command.ErrRetry
	}
	return command.ErrUnavailable
}

// transaction 先锁主体重验当前会话，再执行领域锁，避免对象存在性泄露。
func (s *Service) transaction(ctx context.Context, token string, admin, write bool, run func(*gorm.DB, actor) error) error {
	if token == "" {
		return command.ErrUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	isolation := sql.LevelReadCommitted
	if !write {
		isolation = sql.LevelRepeatableRead
	}
	var e error
	for attempt := 0; attempt < 3; attempt++ {
		e = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			h := sha256.Sum256([]byte(token))
			var session entity.Session
			e := db.Where("token_hash=? AND revoked_at IS NULL AND expires_at>clock_timestamp()", h[:]).Take(&session).Error
			if errors.Is(e, gorm.ErrRecordNotFound) {
				var other bool
				fn := "is_live_admin_session"
				if admin {
					fn = "is_live_student_session"
				}
				if e = db.Raw("SELECT public."+fn+"(decode(?,'hex'))", hex.EncodeToString(h[:])).Scan(&other).Error; e != nil {
					return e
				}
				if other {
					return command.ErrForbidden
				}
				return command.ErrUnauthenticated
			}
			if e != nil {
				return e
			}
			if (session.SubjectKind == "admin") != admin {
				return command.ErrForbidden
			}
			p := command.Discovery{}
			if admin {
				p.Admins = []string{session.SubjectID}
			} else {
				p.Accounts = []string{session.SubjectID}
			}
			guard, e := command.LockPlan(ctx, db, p, s.cfg.Command.LockTimeout)
			if e != nil {
				return e
			}
			now := guard.Now()
			var epoch int64
			if admin {
				var v entity.AdminAccount
				if e = db.Select("id,status,auth_epoch").Where("id=? AND status='active'", session.SubjectID).Take(&v).Error; e != nil {
					return command.ErrUnauthenticated
				}
				epoch = v.AuthEpoch
			} else {
				g, ok := guard.Guard(session.SubjectID)
				if !ok || g.Lifecycle != "active" {
					return command.ErrUnauthenticated
				}
				epoch = g.AuthEpoch
			}
			if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND revoked_at IS NULL AND expires_at>? AND auth_epoch=?", session.ID, now, epoch).Take(&session).Error; e != nil {
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return command.ErrUnauthenticated
				}
				return e
			}
			a := actor{ID: session.SubjectID, Admin: admin, Now: now}
			if admin {
				var roles []string
				if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", a.ID).Pluck("role", &roles).Error; e != nil {
					return e
				}
				if !slices.Contains(roles, "review_operator") && !slices.Contains(roles, "super_admin") {
					return command.ErrForbidden
				}
			} else {
				var kinds []string
				if e = db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('ban','mute') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", a.ID, now, now).Pluck("action_kind", &kinds).Error; e != nil {
					return e
				}
				if slices.Contains(kinds, "ban") {
					return command.ErrForbidden
				}
				g, _ := guard.Guard(a.ID)
				if g.Verification != "verified" {
					return command.ErrVerificationRequired
				}
				a.Muted = slices.Contains(kinds, "mute")
				if write && a.Muted {
					return command.ErrForbidden
				}
			}
			if e = db.Exec("SELECT set_config('app.resource_actor',?,true)", a.ID).Error; e != nil {
				return e
			}
			return run(db, a)
		}, &sql.TxOptions{Isolation: isolation})
		e = normalizeError(e)
		if !errors.Is(e, command.ErrRetry) {
			return e
		}
	}
	return e
}

type cursor struct {
	Binding, ID string
	At, Expires time.Time
	Sort        int
}

func (s *Service) aead() (cipher.AEAD, error) {
	b, e := aes.NewCipher(s.cfg.Key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}
func binding(a actor, op string, q Query) string {
	q.Cursor = ""
	b, _ := json.Marshal([]any{a.ID, a.Admin, op, q})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Service) after(a actor, op string, q Query) (c cursor, err error) {
	if q.Cursor == "" {
		return
	}
	g, e := s.aead()
	if e != nil {
		return c, e
	}
	b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
	if e != nil || len(b) < g.NonceSize()+g.Overhead() || len(q.Cursor) > 2048 {
		return c, command.ErrInvalid
	}
	raw, e := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte("resource-plaza/1"))
	if e != nil {
		return c, command.ErrInvalid
	}
	defer clear(raw)
	if json.Unmarshal(raw, &c) != nil || c.Binding != binding(a, op, q) || !c.Expires.After(a.Now) || !uuidPattern.MatchString(c.ID) {
		return cursor{}, command.ErrInvalid
	}
	return
}
func (s *Service) next(a actor, op string, q Query, c cursor) (*string, error) {
	c.Binding = binding(a, op, q)
	c.Expires = a.Now.Add(30 * time.Minute)
	raw, e := json.Marshal(c)
	if e != nil {
		return nil, e
	}
	defer clear(raw)
	g, e := s.aead()
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	v := base64.RawURLEncoding.EncodeToString(g.Seal(nonce, nonce, raw, []byte("resource-plaza/1")))
	return &v, nil
}

type download struct {
	URL  string  `json:"download_url"`
	Code *string `json:"extraction_code"`
}

func (s *Service) encrypt(ctx context.Context, purpose, id string, version int64, value any) ([]byte, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	defer clear(b)
	env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, b)
	if e != nil {
		return nil, e
	}
	return json.Marshal(env)
}
func (s *Service) download(ctx context.Context, r entity.ResourceMaterial) (d download, err error) {
	var env securedata.Envelope
	if json.Unmarshal(r.DownloadCipher, &env) != nil {
		return d, command.ErrUnavailable
	}
	b, e := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "resource_download", ObjectID: r.ID, Version: uint64(r.ContentVersion)}, env)
	if e != nil {
		return d, e
	}
	defer clear(b)
	err = json.Unmarshal(b, &d)
	return
}
func catProjection(c entity.ResourceCategory, admin bool) map[string]any {
	p := map[string]any{"id": c.ID, "name": c.Name}
	if admin {
		p["sort_order"] = c.SortOrder
		p["enabled"] = c.Enabled
		p["version"] = c.Version
		p["created_at"] = c.CreatedAt.UTC()
		p["updated_at"] = c.UpdatedAt.UTC()
	}
	return p
}
func feedbackProjection(f entity.ResourceFeedback) map[string]any {
	return map[string]any{"resource_id": f.ResourceID, "content_version": f.ContentVersion, "kind": f.Kind, "version": f.Version, "updated_at": f.UpdatedAt.UTC()}
}
func utc(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	t := v.UTC()
	return &t
}
func category(db *gorm.DB, id string) (c entity.ResourceCategory, err error) {
	err = db.Where("id=?", id).Take(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}
func material(db *gorm.DB, id string, a actor) (r entity.ResourceMaterial, err error) {
	q := db.Where("id=? AND NOT privacy_blocked", id)
	if !a.Admin {
		q = q.Where("status='published' AND NOT content_blocked")
	}
	err = q.Take(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return
}
func (s *Service) project(db *gorm.DB, a actor, r entity.ResourceMaterial, detail bool, stats map[string]map[string]any, categories map[string]entity.ResourceCategory) (map[string]any, error) {
	c, ok := categories[r.CategoryID]
	var e error
	if !ok && categories == nil {
		c, e = category(db, r.CategoryID)
	}
	if e != nil || !c.Enabled {
		return nil, command.ErrUnavailable
	}
	p := map[string]any{"id": r.ID, "title": r.Title, "category": catProjection(c, false), "version": r.Version, "created_at": r.CreatedAt.UTC(), "updated_at": r.UpdatedAt.UTC(), "published_at": utc(r.PublishedAt)}
	if detail {
		d, e := s.download(db.Statement.Context, r)
		if e != nil {
			return nil, e
		}
		p["description"] = r.Description
		p["download_url"] = d.URL
		p["extraction_code"] = d.Code
		p["content_version"] = r.ContentVersion
	} else {
		p["description_excerpt"] = string([]rune(r.Description)[:min(120, len([]rune(r.Description)))])
	}
	if a.Admin {
		p["status"] = r.Status
		p["content_version"] = r.ContentVersion
		p["visibility_blocked"] = r.ContentBlocked
		actions := []string{"update", "publish"}
		if r.ContentBlocked {
			actions = []string{"update"}
		}
		if r.Status == "published" {
			actions = []string{"offline"}
		}
		p["allowed_actions"] = actions
		p["feedback_summary"] = stats[r.ID]
	} else if detail {
		p["my_feedback"] = nil
		var fs []entity.ResourceFeedback
		if e = db.Where("resource_id=? AND account_id=? AND content_version=?", r.ID, a.ID, r.ContentVersion).Find(&fs).Error; e != nil {
			return nil, e
		}
		if len(fs) == 1 {
			p["my_feedback"] = feedbackProjection(fs[0])
		}
		actions := []string{"copy_download", "submit_feedback"}
		if a.Muted {
			actions = actions[:1]
		}
		p["allowed_actions"] = actions
	}
	return p, nil
}
func summaries(db *gorm.DB, rows []entity.ResourceMaterial) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ID)
		out[r.ID] = map[string]any{"content_version": r.ContentVersion, "valid": int64(0), "link_invalid": int64(0), "outdated": int64(0), "incomplete": int64(0), "total": int64(0)}
	}
	if len(ids) == 0 {
		return out, nil
	}
	var counts []struct {
		ResourceID, Kind string
		Count            int64
	}
	e := db.Table("public.resource_feedback f").Select("f.resource_id,f.kind,count(*) AS count").Joins("JOIN public.accounts a ON a.id=f.account_id AND a.lifecycle='active'").Joins("JOIN public.resource_materials r ON r.id=f.resource_id AND r.content_version=f.content_version").Where("f.resource_id IN ?", ids).Group("f.resource_id,f.kind").Scan(&counts).Error
	for _, c := range counts {
		out[c.ResourceID][c.Kind] = c.Count
		out[c.ResourceID]["total"] = out[c.ResourceID]["total"].(int64) + c.Count
	}
	return out, e
}

// Read 执行八个固定查询操作；每页重新验证数据库主体和游标绑定。
func (s *Service) Read(ctx context.Context, token, rid, op, id string, q Query, admin bool) (out any, err error) {
	defer s.done(op, rid, time.Now(), &err)
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	err = s.transaction(ctx, token, admin, false, func(db *gorm.DB, a actor) error {
		if op == "categories" {
			after, e := s.after(a, op, q)
			if e != nil {
				return e
			}
			v := db.Model(&entity.ResourceCategory{})
			if !admin {
				v = v.Where("enabled")
			} else if q.Enabled != "" {
				v = v.Where("enabled=?", q.Enabled == "true")
			}
			if after.ID != "" {
				v = v.Where("(sort_order,id)>(?,?::uuid)", after.Sort, after.ID)
			}
			var rows []entity.ResourceCategory
			if e = v.Order("sort_order ASC,id ASC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			var next *string
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				next, e = s.next(a, op, q, cursor{ID: last.ID, Sort: last.SortOrder})
				if e != nil {
					return e
				}
			}
			items := []any{}
			for _, c := range rows {
				items = append(items, catProjection(c, admin))
			}
			out = map[string]any{"items": items, "next_cursor": next}
			return nil
		}
		if op == "category_detail" {
			c, e := category(db, id)
			if e != nil {
				return e
			}
			out = catProjection(c, true)
			return nil
		}
		if op == "list" {
			after, e := s.after(a, op, q)
			if e != nil {
				return e
			}
			if q.CategoryID != "" {
				c, e := category(db, q.CategoryID)
				if errors.Is(e, command.ErrNotFound) || e == nil && !c.Enabled {
					return command.ErrInvalid
				}
				if e != nil {
					return e
				}
			}
			v := db.Model(&entity.ResourceMaterial{}).Omit("download_cipher").Where("NOT privacy_blocked")
			if !admin {
				v = v.Where("status='published' AND NOT content_blocked")
			}
			if q.CategoryID != "" {
				v = v.Where("category_id=?", q.CategoryID)
			}
			if q.Status != "" {
				v = v.Where("status=?", q.Status)
			}
			if q.Q != "" {
				v = v.Where("strpos(translate(title,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'),?)>0 OR strpos(translate(description,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'),?)>0", Fold(q.Q), Fold(q.Q))
			}
			if after.ID != "" {
				v = v.Where("(created_at,id)<(?,?::uuid)", after.At, after.ID)
			}
			var rows []entity.ResourceMaterial
			if e = v.Order("created_at DESC,id DESC").Limit(q.Limit + 1).Find(&rows).Error; e != nil {
				return e
			}
			var next *string
			if len(rows) > q.Limit {
				rows = rows[:q.Limit]
				last := rows[len(rows)-1]
				next, e = s.next(a, op, q, cursor{ID: last.ID, At: last.CreatedAt})
				if e != nil {
					return e
				}
			}
			stats := map[string]map[string]any{}
			if admin {
				stats, e = summaries(db, rows)
				if e != nil {
					return e
				}
			}
			categoryIDs := []string{}
			for _, r := range rows {
				categoryIDs = append(categoryIDs, r.CategoryID)
			}
			categories := map[string]entity.ResourceCategory{}
			var categoryRows []entity.ResourceCategory
			if len(categoryIDs) > 0 {
				if e = db.Where("id IN ?", categoryIDs).Find(&categoryRows).Error; e != nil {
					return e
				}
			}
			for _, c := range categoryRows {
				categories[c.ID] = c
			}
			items := []any{}
			for _, r := range rows {
				p, e := s.project(db, a, r, false, stats, categories)
				if e != nil {
					return e
				}
				items = append(items, p)
			}
			out = map[string]any{"items": items, "next_cursor": next}
			return nil
		}
		r, e := material(db, id, a)
		if e != nil {
			return e
		}
		if op == "download" {
			c, e := category(db, r.CategoryID)
			if e != nil || !c.Enabled {
				return command.ErrUnavailable
			}
			if r.Version != q.ExpectedVersion {
				return command.ErrVersionConflict
			}
			d, e := s.download(ctx, r)
			if e != nil {
				return e
			}
			copyText := d.URL
			if d.Code != nil {
				copyText += "\n提取码：" + *d.Code
			}
			out = map[string]any{"resource_id": r.ID, "version": r.Version, "download_url": d.URL, "extraction_code": d.Code, "copy_text": copyText}
			return nil
		}
		stats := map[string]map[string]any{}
		if admin {
			stats, e = summaries(db, []entity.ResourceMaterial{r})
			if e != nil {
				return e
			}
		}
		out, e = s.project(db, a, r, true, stats, nil)
		return e
	})
	return
}
