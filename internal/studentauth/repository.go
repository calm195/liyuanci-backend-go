package studentauth

import (
	"context"
	"crypto/sha256"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

type exchangeReceipt struct {
	ID          string
	RequestHash []byte
	Nonce       []byte
	Status      string
	SessionID   *string
	ExpiresAt   time.Time
}

// claimExchange 用唯一code摘要保证只有一个调用者执行外部交换；冲突调用者不能抢占或重置。
func claimExchange(ctx context.Context, db *gorm.DB, id string, code, request, nonce []byte, ttl time.Duration) (bool, error) {
	result := db.WithContext(ctx).Exec(`INSERT INTO public.student_login_exchanges(id,code_hash,request_hash,nonce,expires_at)
 VALUES (?,?,?,?,clock_timestamp()+(? * interval '1 millisecond')) ON CONFLICT(code_hash) DO NOTHING`, id, code, request, nonce, ttl.Milliseconds())
	return result.RowsAffected == 1, result.Error
}

func readExchange(ctx context.Context, db *gorm.DB, hash []byte) (exchangeReceipt, error) {
	var rows []exchangeReceipt
	if e := db.WithContext(ctx).Raw("SELECT * FROM public.student_login_exchanges WHERE code_hash=?", hash).Scan(&rows).Error; e != nil {
		return exchangeReceipt{}, e
	}
	if len(rows) != 1 {
		return exchangeReceipt{}, command.ErrUnauthenticated
	}
	return rows[0], nil
}

func exchangeForUpdate(ctx context.Context, db *gorm.DB, hash []byte) (exchangeReceipt, error) {
	var rows []exchangeReceipt
	if e := db.WithContext(ctx).Raw("SELECT * FROM public.student_login_exchanges WHERE code_hash=? FOR UPDATE", hash).Scan(&rows).Error; e != nil {
		return exchangeReceipt{}, e
	}
	if len(rows) != 1 {
		return exchangeReceipt{}, command.ErrUnauthenticated
	}
	return rows[0], nil
}

func finishExchange(ctx context.Context, db *gorm.DB, id, session string) error {
	return db.WithContext(ctx).Exec("UPDATE public.student_login_exchanges SET status='completed',session_id=? WHERE id=?", session, id).Error
}

func setTimeout(ctx context.Context, db *gorm.DB, timeout time.Duration) error {
	return db.WithContext(ctx).Exec("SELECT set_config('lock_timeout',?,true)", strconv.FormatInt(timeout.Milliseconds(), 10)+"ms").Error
}

func databaseNow(ctx context.Context, db *gorm.DB, out *time.Time) error {
	return db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(out).Error
}

func accountEpoch(ctx context.Context, db *gorm.DB, id string, out *int64) error {
	return db.WithContext(ctx).Raw("SELECT auth_epoch FROM public.accounts WHERE id=? AND lifecycle='active'", id).Scan(out).Error
}

func insertSession(ctx context.Context, db *gorm.DB, id, account string, hash []byte, epoch int64, expiry time.Time) error {
	return db.WithContext(ctx).Exec("INSERT INTO public.sessions(id,account_id,token_hash,auth_epoch,expires_at) VALUES (?,?,?,?,?)", id, account, hash, epoch, expiry).Error
}

// authorize 遵循账号guard→会话锁序，所有读取投影和退出也复核当前主体状态。
func authorize(ctx context.Context, db *gorm.DB, token string) (id string, now time.Time, err error) {
	if token == "" {
		return "", now, command.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var refs []struct{ AccountID string }
	if e := db.WithContext(ctx).Raw("SELECT account_id FROM public.sessions WHERE token_hash=? AND subject_kind='student'", hash[:]).Scan(&refs).Error; e != nil {
		return "", now, e
	}
	if len(refs) != 1 {
		return "", now, command.ErrUnauthenticated
	}
	var guards []command.Guard
	if e := db.WithContext(ctx).Raw("SELECT * FROM public.lock_command_account(?)", refs[0].AccountID).Scan(&guards).Error; e != nil {
		return "", now, e
	}
	if len(guards) != 1 || guards[0].Lifecycle != "active" {
		return "", now, command.ErrUnauthenticated
	}
	var sessions []struct {
		AccountID string
		AuthEpoch int64
		ExpiresAt time.Time
		RevokedAt *time.Time
	}
	if e := db.WithContext(ctx).Raw("SELECT account_id,auth_epoch,expires_at,revoked_at FROM public.sessions WHERE token_hash=? AND subject_kind='student' FOR UPDATE", hash[:]).Scan(&sessions).Error; e != nil {
		return "", now, e
	}
	if e := databaseNow(ctx, db, &now); e != nil {
		return "", now, e
	}
	if len(sessions) != 1 || sessions[0].AccountID != guards[0].ID || sessions[0].AuthEpoch != guards[0].AuthEpoch || sessions[0].RevokedAt != nil || !sessions[0].ExpiresAt.After(now) {
		return "", now, command.ErrUnauthenticated
	}
	return guards[0].ID, now, nil
}

func projection(ctx context.Context, db *gorm.DB, id string, now time.Time) (Me, error) {
	var rows []Me
	if e := db.WithContext(ctx).Raw("SELECT id,version,nickname,verification,hide_identity_tags FROM public.accounts WHERE id=? AND lifecycle='active'", id).Scan(&rows).Error; e != nil {
		return Me{}, e
	}
	if len(rows) != 1 {
		return Me{}, command.ErrUnauthenticated
	}
	out := rows[0]
	out.AllowedActions = []string{"deleteAuthSession"}
	var restricted bool
	if e := db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM public.penalties WHERE account_id=? AND action_kind IN ('mute','ban')
 AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?))`, id, now, now).Scan(&restricted).Error; e != nil {
		return Me{}, e
	}
	if restricted {
		out.Nickname = ""
		out.PenaltyNotice = "当前账号交互权限受限"
	} else {
		out.AllowedActions = append(out.AllowedActions, "putMeProfile")
	}
	// 尚未实现的认证、心愿等写操作不列入允许动作，认证状态本身也不会授予未装配路由。
	return out, nil
}

func replaceProfile(ctx context.Context, db *gorm.DB, id string, input ProfileInput) error {
	return db.WithContext(ctx).Exec("UPDATE public.accounts SET nickname=?,hide_identity_tags=?,version=version+1 WHERE id=?", input.Nickname, *input.HideIdentityTags, id).Error
}

func revoke(ctx context.Context, db *gorm.DB, hash []byte, now time.Time) error {
	return db.WithContext(ctx).Exec("UPDATE public.sessions SET revoked_at=? WHERE token_hash=? AND subject_kind='student'", now, hash).Error
}
