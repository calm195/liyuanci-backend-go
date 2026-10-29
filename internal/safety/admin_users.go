package safety

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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// UsersQuery 是后台普通用户查询的完整筛选；Cursor 不参与筛选摘要。
type UsersQuery struct {
	UserID, Nickname, Verification, Restriction string
	Limit                                       int
	Cursor                                      string `json:"-"`
}

// UserSummary 是普通资料白名单，不携带身份密文、匿名昵称或内容归属。
type UserSummary struct {
	ID           string     `json:"id"`
	DisplayName  *string    `json:"display_name"`
	Verification string     `json:"verification"`
	Restriction  string     `json:"restriction"`
	MutedUntil   *time.Time `json:"muted_until"`
	CreatedAt    time.Time  `json:"created_at"`
	AsOf         time.Time  `json:"as_of"`
}

// UserPage 仅返回键集分页结果，不返回过滤前计数。
type UserPage struct {
	Items      []UserSummary `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// ActivePenalty 只说明当前可读限制，不含案件来源。
type ActivePenalty struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	StartsAt time.Time  `json:"starts_at"`
	EndsAt   *time.Time `json:"ends_at"`
}

// UserDetail 提供账号版本与导航提示；提示不赋予写权限。
type UserDetail struct {
	UserSummary
	AccountVersion  int64           `json:"account_version"`
	ActivePenalties []ActivePenalty `json:"active_penalties"`
	AllowedActions  []string        `json:"allowed_actions"`
}

// UserCaseLink 仅允许已授权的普通案件引用。
type UserCaseLink struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// UserPenalty 是处罚历史安全投影，自由输入理由不进入该结构。
type UserPenalty struct {
	ID        string        `json:"id"`
	Level     string        `json:"level"`
	Kind      string        `json:"kind"`
	Status    string        `json:"status"`
	StartsAt  time.Time     `json:"starts_at"`
	EndsAt    *time.Time    `json:"ends_at"`
	RevokedAt *time.Time    `json:"revoked_at"`
	Reason    *string       `json:"reason"`
	CaseLink  *UserCaseLink `json:"case_link"`
	AppealID  *string       `json:"appeal_id"`
}

// UserPenaltyPage 在权限过滤后分页，不暴露隐藏处罚的位置或数量。
type UserPenaltyPage struct {
	Items      []UserPenalty `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

// NormalizeUsersQuery 规范化普通昵称与UUID，校验封闭筛选词表。
func NormalizeUsersQuery(q UsersQuery) (UsersQuery, error) {
	if q.UserID != "" {
		id, e := NormalizeUserID(q.UserID)
		if e != nil {
			return q, e
		}
		q.UserID = id
	}
	q.Nickname = strings.TrimSpace(q.Nickname)
	if !utf8.ValidString(q.Nickname) || utf8.RuneCountInString(q.Nickname) > 50 || q.Limit < 1 || q.Limit > 50 ||
		!slices.Contains([]string{"", "unverified", "pending", "verified", "rejected"}, q.Verification) ||
		!slices.Contains([]string{"", "normal", "muted", "banned"}, q.Restriction) {
		return q, command.ErrInvalid
	}
	q.Nickname = asciiFold(q.Nickname)
	return q, nil
}

// NormalizeUserID 仅接受带连字符的标准UUID并规范化大小写。
func NormalizeUserID(value string) (string, error) {
	id, e := uuid.Parse(value)
	if e != nil || len(value) != 36 || !strings.EqualFold(id.String(), value) {
		return "", command.ErrInvalid
	}
	return id.String(), nil
}

func asciiFold(v string) string {
	b := []byte(v)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

type usersCursor struct {
	Version  int
	Binding  string
	Expires  time.Time
	Position time.Time
	ID       string
}

func (s *Service) usersAEAD() (cipher.AEAD, error) {
	m := hmac.New(sha256.New, s.cfg.Key)
	m.Write([]byte("admin.users.cursor.v1"))
	b, e := aes.NewCipher(m.Sum(nil))
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}

func (s *Service) usersBinding(actor, op, target string, q UsersQuery) string {
	return base64.RawURLEncoding.EncodeToString(s.digest(struct {
		Actor, Purpose, Target, Sort string
		Query                        UsersQuery
	}{actor, op, target, "time_uuid_desc", q}))
}

func (s *Service) usersAfter(binding, value string, now time.Time) (out usersCursor, err error) {
	if value == "" {
		return out, nil
	}
	if len(value) > 4096 {
		return out, command.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(value)
	if e != nil {
		return out, command.ErrInvalid
	}
	a, e := s.usersAEAD()
	if e != nil {
		return out, e
	}
	if len(b) < a.NonceSize()+a.Overhead() {
		return out, command.ErrInvalid
	}
	plain, e := a.Open(nil, b[:a.NonceSize()], b[a.NonceSize():], []byte("admin-users/1"))
	if e != nil {
		return out, command.ErrInvalid
	}
	defer clear(plain)
	if json.Unmarshal(plain, &out) != nil || out.Version != 1 || out.Binding != binding || !now.Before(out.Expires) || out.Expires.After(now.Add(30*time.Minute)) || !validID(out.ID) || out.Position.IsZero() {
		return usersCursor{}, command.ErrInvalid
	}
	return out, nil
}

func (s *Service) usersNext(binding string, at time.Time, id string, now time.Time) (*string, error) {
	a, e := s.usersAEAD()
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(usersCursor{1, binding, now.Add(30 * time.Minute), at, id})
	if e != nil {
		return nil, e
	}
	defer clear(b)
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	value := base64.RawURLEncoding.EncodeToString(a.Seal(nonce, nonce, b, []byte("admin-users/1")))
	return &value, nil
}

// usersRead 在一致快照内重验后台会话，按后台、目标账号锁序保护详情留迹与注销清理。
// 新请求不会越过已提交的注销屏障；并发快照冲突失败关闭，由调用方重新读取。
func (s *Service) usersRead(ctx context.Context, token, target string, run func(*gorm.DB, string, []string, time.Time) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		h := sha256.Sum256([]byte(token))
		var session entity.Session
		e := db.Where("token_hash=? AND revoked_at IS NULL AND expires_at>?", h[:], now).Take(&session).Error
		if e == gorm.ErrRecordNotFound {
			// RLS不向后台连接展示学生会话，只借窄权限布尔函数映射跨域错误。
			var student bool
			if failure := db.Raw("SELECT public.is_live_student_session(decode(?,'hex'))", hex.EncodeToString(h[:])).Scan(&student).Error; failure != nil {
				return failure
			}
			if student {
				return command.ErrForbidden
			}
			return command.ErrUnauthenticated
		}
		if e != nil {
			return e
		}
		if session.SubjectKind != "admin" {
			return command.ErrForbidden
		}
		plan := command.Discovery{Admins: []string{session.SubjectID}}
		if _, e = command.LockPlan(ctx, db, plan, s.cfg.Command.LockTimeout); e != nil {
			return e
		}
		var admin entity.AdminAccount
		if e = db.Select("id,status,auth_epoch").Where("id=? AND status='active' AND auth_epoch=?", session.SubjectID, session.AuthEpoch).Take(&admin).Error; e != nil {
			if e == gorm.ErrRecordNotFound {
				return command.ErrUnauthenticated
			}
			return e
		}
		if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND revoked_at IS NULL AND expires_at>?", session.ID, now).Take(&session).Error; e != nil {
			if e == gorm.ErrRecordNotFound {
				return command.ErrUnauthenticated
			}
			return e
		}
		var roles []string
		if e = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=?", admin.ID).Pluck("role", &roles).Error; e != nil {
			return e
		}
		if !slices.ContainsFunc(roles, func(r string) bool {
			return slices.Contains([]string{"review_operator", "crisis_operator", "super_admin"}, r)
		}) {
			return command.ErrForbidden
		}
		if target != "" {
			if _, e = command.LockPlan(ctx, db, command.Discovery{Accounts: []string{target}}, s.cfg.Command.LockTimeout); e != nil {
				return e
			}
			var n int64
			if e = db.Model(&entity.Account{}).Where("id=? AND lifecycle='active'", target).Count(&n).Error; e != nil {
				return e
			}
			if n != 1 {
				return command.ErrNotFound
			}
		}
		return run(db, admin.ID, roles, now.UTC())
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
}

// UsersList 查询当前普通用户；列表日志只记录筛选类型和数量，不记录查询值或整页标识。
func (s *Service) UsersList(ctx context.Context, token, rid string, q UsersQuery) (out UserPage, err error) {
	defer func() {
		if err != nil {
			out = UserPage{}
		}
	}()
	defer s.done("admin_users_list", rid, time.Now(), &err)
	q, err = NormalizeUsersQuery(q)
	if err != nil {
		return
	}
	out.Items = []UserSummary{}
	var actor string
	err = s.usersRead(ctx, token, "", func(db *gorm.DB, a string, _ []string, now time.Time) error {
		actor = a
		binding := s.usersBinding(a, "users", "", q)
		after, e := s.usersAfter(binding, q.Cursor, now)
		if e != nil {
			return e
		}
		rows, e := usersRows(ctx, db, q, after, now)
		if e != nil {
			return e
		}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			last := rows[len(rows)-1]
			out.NextCursor, e = s.usersNext(binding, last.CreatedAt, last.ID, now)
			if e != nil {
				return e
			}
		}
		for _, r := range rows {
			out.Items = append(out.Items, r.summary(now))
		}
		// 列表没有学生目标引用；详情留迹才关联目标并由原注销清理删除。
		return audit(db, a, a, "admin_users_list")
	})
	if err == nil {
		s.logger.Info("后台用户查询完成", zap.String("actor_id", actor), zap.String("request_id", rid), zap.Bool("filter_user_id", q.UserID != ""), zap.Bool("filter_nickname", q.Nickname != ""), zap.Bool("filter_verification", q.Verification != ""), zap.Bool("filter_restriction", q.Restriction != ""), zap.Int("result_count", len(out.Items)))
	}
	return
}

// UserDetailRead 返回同一快照中的账号版本、可读限制和普通案件导航。
func (s *Service) UserDetailRead(ctx context.Context, token, rid, id string) (out UserDetail, err error) {
	defer func() {
		if err != nil {
			out = UserDetail{}
		}
	}()
	defer s.done("admin_user_detail", rid, time.Now(), &err)
	id, err = NormalizeUserID(id)
	if err != nil {
		return
	}
	err = s.usersRead(ctx, token, id, func(db *gorm.DB, actor string, roles []string, now time.Time) error {
		rows, e := usersRows(ctx, db, UsersQuery{UserID: id, Limit: 1}, usersCursor{}, now)
		if e != nil {
			return e
		}
		if len(rows) != 1 {
			return command.ErrNotFound
		}
		out = UserDetail{UserSummary: rows[0].summary(now), AccountVersion: rows[0].Version, ActivePenalties: []ActivePenalty{}, AllowedActions: []string{"view_penalties"}}
		out.ActivePenalties, e = usersActivePenalties(ctx, db, id, roles, now)
		if e != nil {
			return e
		}
		cases, appeals, e := usersNavigation(ctx, db, id)
		if e != nil {
			return e
		}
		if cases {
			out.AllowedActions = append(out.AllowedActions, "open_case_workspace")
		}
		if appeals {
			out.AllowedActions = append(out.AllowedActions, "open_appeal_workspace")
		}
		return audit(db, actor, id, "admin_user_detail")
	})
	return
}

// UserPenalties 查询当前角色有权读取的处罚历史，权限过滤先于截断和游标计算。
func (s *Service) UserPenalties(ctx context.Context, token, rid, id string, q UsersQuery) (out UserPenaltyPage, err error) {
	defer func() {
		if err != nil {
			out = UserPenaltyPage{}
		}
	}()
	defer s.done("admin_user_penalties", rid, time.Now(), &err)
	id, err = NormalizeUserID(id)
	if err != nil {
		return
	}
	q, err = NormalizeUsersQuery(q)
	if err != nil {
		return
	}
	if q.UserID != "" || q.Nickname != "" || q.Verification != "" || q.Restriction != "" {
		return out, command.ErrInvalid
	}
	out.Items = []UserPenalty{}
	err = s.usersRead(ctx, token, id, func(db *gorm.DB, actor string, roles []string, now time.Time) error {
		binding := s.usersBinding(actor, "penalties", id, q)
		after, e := s.usersAfter(binding, q.Cursor, now)
		if e != nil {
			return e
		}
		rows, e := usersPenaltyRows(ctx, db, id, roles, q.Limit, after, now)
		if e != nil {
			return e
		}
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
			last := rows[len(rows)-1]
			out.NextCursor, e = s.usersNext(binding, last.StartsAt, last.ID, now)
			if e != nil {
				return e
			}
		}
		for _, r := range rows {
			out.Items = append(out.Items, r.projection(now))
		}
		return audit(db, actor, id, "admin_user_penalties")
	})
	return
}
