// Package contentinteraction 提供本人收藏及轻量浏览入口，目标权限由各内容领域显式注入。
package contentinteraction

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// Target 仅包含当前仍可读取的摘要，不保存下载地址或作者账号映射。
type Target struct {
	Title   string `json:"title"`
	Excerpt string `json:"excerpt"`
}

// Resolver 每次读取重新执行目标领域的当前授权，空目标统一返回不可见错误。
type Resolver func(context.Context, string, string, string, string) (Target, error)

// Service 借用数据库与日志，通过解析器保持各模块的访问边界。
type Service struct {
	db        *gorm.DB
	engine    *command.Engine
	log       *zap.Logger
	resolve   Resolver
	resolveTx TransactionResolver
	key       []byte
	cfg       command.Config
}

// New 构造明确注入的互动服务，不创建表或连接。
func New(db *gorm.DB, log *zap.Logger, cfg command.Config, key []byte, resolver Resolver, resolverTx TransactionResolver) (*Service, error) {
	if resolver == nil || resolverTx == nil || len(key) < 32 {
		return nil, command.ErrInvalid
	}
	engine, err := command.New(db, log, cfg)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, engine: engine, log: log, resolve: resolver, resolveTx: resolverTx, key: append([]byte(nil), key...), cfg: cfg}, nil
}
func validID(v string) bool { id, e := uuid.Parse(v); return e == nil && id.String() == v }
func (s *Service) done(op, rid string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	s.log.Info("内容互动操作完成", zap.String("operation", op), zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
func account(ctx context.Context, db *gorm.DB, token string) (string, error) {
	if token == "" {
		return "", command.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var ids []string
	err := db.WithContext(ctx).Table("public.sessions AS s").Select("a.id").Joins("JOIN public.accounts a ON a.id=s.account_id").Where("s.subject_kind='student' AND s.token_hash=? AND s.auth_epoch=a.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND a.lifecycle='active'", hash[:]).Scan(&ids).Error
	if err != nil {
		return "", err
	}
	if len(ids) != 1 {
		return "", command.ErrUnauthenticated
	}
	return ids[0], nil
}

// FavoriteState 是设置后的本人收藏状态。
type FavoriteState struct {
	Kind      string `json:"kind"`
	TargetID  string `json:"target_id"`
	Favorited bool   `json:"favorited"`
}

// SetFavorite 使用命令回执和账号锁串行化同一人的状态切换；取消不依赖原目标存续。
func (s *Service) SetFavorite(ctx context.Context, token, key, rid, kind, id string, active bool) (out FavoriteState, err error) {
	defer s.done("favorite.set", rid, time.Now(), &err)
	if !contentquery.ValidInteractionKind(kind) || !validID(id) {
		return out, command.ErrInvalid
	}
	if active {
		if _, err = s.resolve(ctx, token, rid, kind, id); err != nil {
			return out, err
		}
	}
	method := "DELETE"
	if active {
		method = "PUT"
	}
	d := command.Definition{Kind: "favorite.set", Method: method, ResourcePath: "/api/v1/me/favorites/" + kind + "/" + id, ActorKind: "student", Access: command.Restricted, RetainReceipt: true}
	d.Discover = func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		if active {
			return discoverTarget(ctx, db, kind, id)
		}
		return command.Discovery{}, nil
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		if !active {
			return nil
		}
		_, e := s.resolveTx(ctx, tx.DB(), tx.Now(), tx.Actor().ID, kind, id)
		return e
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		db := tx.DB().WithContext(ctx)
		var e error
		if active {
			e = db.Exec("INSERT INTO public.content_favorites(account_id,target_kind,target_id,created_at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING", tx.Actor().ID, kind, id, tx.Now()).Error
		} else {
			e = db.Where("account_id=? AND target_kind=? AND target_id=?", tx.Actor().ID, kind, id).Delete(&entity.ContentFavorite{}).Error
		}
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, _ string) error {
		var n int64
		e := tx.DB().WithContext(ctx).Model(&entity.ContentFavorite{}).Where("account_id=? AND target_kind=? AND target_id=?", tx.Actor().ID, kind, id).Count(&n).Error
		out = FavoriteState{Kind: kind, TargetID: id, Favorited: n > 0}
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: active})
	return
}

// FavoriteItem 的失效条目只含技术引用和删除入口，不回放旧摘要。
type FavoriteItem struct {
	Kind      string    `json:"kind"`
	TargetID  string    `json:"target_id"`
	CreatedAt time.Time `json:"created_at"`
	Available bool      `json:"available"`
	Content   *Target   `json:"content"`
}

// FavoritePage 使用绑定账号与筛选的时间游标。
type FavoritePage struct {
	Items      []FavoriteItem `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}
type favoriteCursor struct {
	Actor      string    `json:"a"`
	Kind       string    `json:"k"`
	At         time.Time `json:"t"`
	TargetKind string    `json:"g"`
	ID         string    `json:"i"`
}

func (s *Service) cursor(v favoriteCursor) string {
	b, _ := json.Marshal(v)
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("favorites.cursor.v1\x00"))
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (s *Service) decode(v, actor, kind string) (out favoriteCursor, err error) {
	if v == "" {
		return out, nil
	}
	if len(v) > 2048 {
		return out, command.ErrInvalid
	}
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return out, command.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	sig, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("favorites.cursor.v1\x00"))
	m.Write(b)
	if e != nil || e2 != nil || !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(b, &out) != nil || out.Actor != s.actorBinding(actor) || out.Kind != kind || !validID(out.ID) {
		return favoriteCursor{}, command.ErrInvalid
	}
	return
}

// Favorites 读取本人引用，再逐项复核当前目标权限，不让资料收藏绕过认证。
func (s *Service) Favorites(ctx context.Context, token, rid, kind, cursor string, limit int) (out FavoritePage, err error) {
	defer s.done("favorites.list", rid, time.Now(), &err)
	if limit < 1 || limit > 50 || kind != "" && !contentquery.ValidInteractionKind(kind) {
		return out, command.ErrInvalid
	}
	actor, e := account(ctx, s.db, token)
	if e != nil {
		return out, e
	}
	after, e := s.decode(cursor, actor, kind)
	if e != nil {
		return out, e
	}
	q := s.db.WithContext(ctx).Where("account_id=?", actor)
	if kind != "" {
		q = q.Where("target_kind=?", kind)
	}
	if after.ID != "" {
		q = q.Where("(created_at,target_kind,target_id)<(?,?,?::uuid)", after.At, after.TargetKind, after.ID)
	}
	var rows []entity.ContentFavorite
	if e = q.Order("created_at DESC,target_kind DESC,target_id DESC").Limit(limit + 1).Find(&rows).Error; e != nil {
		return out, e
	}
	out.Items = []FavoriteItem{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next := s.cursor(favoriteCursor{Actor: s.actorBinding(actor), Kind: kind, At: last.CreatedAt, TargetKind: last.TargetKind, ID: last.TargetID})
		out.NextCursor = &next
	}
	for _, row := range rows {
		item := FavoriteItem{Kind: row.TargetKind, TargetID: row.TargetID, CreatedAt: row.CreatedAt}
		target, e := s.resolve(ctx, token, rid, row.TargetKind, row.TargetID)
		if e == nil {
			item.Available = true
			item.Content = &target
		} else if !errors.Is(e, command.ErrNotFound) && !errors.Is(e, command.ErrForbidden) && !errors.Is(e, command.ErrVerificationRequired) && !errors.Is(e, command.ErrUnavailable) {
			return FavoritePage{}, e
		}
		out.Items = append(out.Items, item)
	}
	return
}

// View 先验证目标仍可见，再按页面事件随机键去重累计。
func (s *Service) View(ctx context.Context, token, rid, kind, id, event string) (out contentquery.Stats, err error) {
	defer s.done("view.record", rid, time.Now(), &err)
	if !contentquery.ValidInteractionKind(kind) || !validID(id) || !validID(event) {
		return out, command.ErrInvalid
	}
	err = s.withTargetWrite(ctx, token, kind, id, func(db *gorm.DB, actor string, now time.Time) error {
		if e := contentquery.RecordView(ctx, db, kind, id, event, now); e != nil {
			return e
		}
		stats, e := contentquery.ReadStats(ctx, db, kind, []string{id}, actor)
		out = stats[id]
		return e
	})
	return
}

func (s *Service) actorBinding(actor string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte("favorites.actor.v1\x00" + actor))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
