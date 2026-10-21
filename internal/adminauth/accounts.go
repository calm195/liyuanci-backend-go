package adminauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// RolesInput 以目标账号版本约束完整角色替换，理由只进入受限操作留迹。
type RolesInput struct {
	Roles           []string `json:"roles"`
	Reason          string   `json:"reason"`
	ExpectedVersion int64    `json:"expected_version"`
}

// AccountPage 是仅超管可见的本地账号页；空游标表示已读完。
type AccountPage struct {
	Items      []Account `json:"items"`
	NextCursor *string   `json:"next_cursor"`
}

type accountCursor struct {
	Actor string `json:"actor"`
	After string `json:"after"`
}

func (s *Service) encodeCursor(actor, after string) string {
	body, _ := json.Marshal(accountCursor{actor, after})
	tag := keyedDigest(s.config.ReceiptKey, "admin_accounts_cursor\x00"+string(body))
	return base64.RawURLEncoding.EncodeToString(append(body, tag...))
}

func (s *Service) decodeCursor(raw, actor string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > 1024 {
		return "", ErrInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) <= sha256.Size {
		return "", ErrInvalid
	}
	body, tag := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
	if !hmac.Equal(tag, keyedDigest(s.config.ReceiptKey, "admin_accounts_cursor\x00"+string(body))) {
		return "", ErrInvalid
	}
	var cursor accountCursor
	if json.Unmarshal(body, &cursor) != nil || cursor.Actor != actor || !canonicalID(cursor.After) {
		return "", ErrInvalid
	}
	return cursor.After, nil
}

func canonicalID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

// ListAccounts 每页重新授权；签名游标绑定当前超管和固定列表，不能跨主体复用。
func (s *Service) ListAccounts(ctx context.Context, token, cursor, requestID string, limit int) (out AccountPage, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		if err != nil {
			out = AccountPage{}
		}
		s.complete("accounts_list", requestID, start, err)
	}()
	if limit < 1 || limit > 50 {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	// 当前会话和角色与列表读取处于同一事务；角色写也取得账号guard，避免撤权后继续读取。
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := s.lockTimeout(ctx, db); e != nil {
			return e
		}
		actor, _, e := s.authorized(ctx, db, token)
		if e != nil {
			return e
		}
		account, e := projection(ctx, db, actor.ID)
		if e != nil {
			return e
		}
		if !slices.Contains(account.Roles, "super_admin") {
			return ErrForbidden
		}
		after, e := s.decodeCursor(cursor, actor.ID)
		if e != nil {
			return e
		}
		items, e := accountRows(ctx, db, after, limit+1)
		if e != nil {
			return e
		}
		out.Items = items
		if len(items) > limit {
			out.Items = items[:limit]
			next := s.encodeCursor(actor.ID, out.Items[limit-1].ID)
			out.NextCursor = &next
		}
		return nil
	})
	return out, err
}

// ChangeRoles 使用命令根事务锁定操作者和目标；成功重放仍复核当前超管权限。
func (s *Service) ChangeRoles(ctx context.Context, token, key, requestID, target string, input RolesInput) (out Account, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		if err != nil {
			out = Account{}
		}
		s.complete("roles_change", requestID, start, err)
	}()
	if !canonicalID(target) || !validKey(key) || input.Roles == nil || strings.TrimSpace(input.Reason) == "" || input.ExpectedVersion < 1 {
		return out, ErrInvalid
	}
	input.Roles = slices.Clone(input.Roles)
	slices.Sort(input.Roles)
	input.Roles = slices.Compact(input.Roles)
	for _, role := range input.Roles {
		if !slices.Contains([]string{"super_admin", "review_operator", "crisis_operator"}, role) {
			return out, ErrInvalid
		}
	}
	definition := command.Definition{
		Kind: "admin.account.roles", Method: "PUT", ResourcePath: "/api/v1/admin/accounts/" + target + "/roles", ActorKind: "admin", AllowedRoles: []string{"super_admin"},
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{Admins: []string{target}}, nil
		},
		Authorize: func(context.Context, *command.Tx) error { return nil },
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			current, e := projection(ctx, tx.DB(), target)
			if e != nil {
				return "", e
			}
			if current.Version != input.ExpectedVersion {
				return "", command.ErrVersionConflict
			}
			// 不改变会话代次：每次请求都按当前角色授权，改密/禁用的撤销语义保持独立。
			if e = replaceRoles(ctx, tx.DB(), target, input.Roles); e != nil {
				return "", e
			}
			return target, audit(ctx, tx.DB(), tx.Actor().ID, target, "roles_change", input.Reason)
		},
		Resolve: func(ctx context.Context, tx *command.Tx, id string) error {
			var e error
			out, e = projection(ctx, tx.DB(), id)
			return e
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: requestID, Request: input})
	return out, err
}
