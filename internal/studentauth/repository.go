package studentauth

import (
	"context"
	"crypto/sha256"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
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
	// map 允许期限使用数据库表达式，避免跨进程时钟偏差；冲突处理由唯一约束原子裁决。
	result := db.WithContext(ctx).Model(&entity.StudentLoginExchange{}).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "code_hash"}}, DoNothing: true,
	}).Create(map[string]any{
		"id": id, "code_hash": code, "request_hash": request, "nonce": nonce,
		"expires_at": gorm.Expr("clock_timestamp()+(? * interval '1 millisecond')", ttl.Milliseconds()),
	})
	return result.RowsAffected == 1, result.Error
}

func readExchange(ctx context.Context, db *gorm.DB, hash []byte) (exchangeReceipt, error) {
	var rows []exchangeReceipt
	if e := db.WithContext(ctx).Model(&entity.StudentLoginExchange{}).Where("code_hash = ?", hash).Find(&rows).Error; e != nil {
		return exchangeReceipt{}, e
	}
	if len(rows) != 1 {
		return exchangeReceipt{}, command.ErrUnauthenticated
	}
	return rows[0], nil
}

func exchangeForUpdate(ctx context.Context, db *gorm.DB, hash []byte) (exchangeReceipt, error) {
	var rows []exchangeReceipt
	if e := db.WithContext(ctx).Model(&entity.StudentLoginExchange{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where("code_hash = ?", hash).Find(&rows).Error; e != nil {
		return exchangeReceipt{}, e
	}
	if len(rows) != 1 {
		return exchangeReceipt{}, command.ErrUnauthenticated
	}
	return rows[0], nil
}

func finishExchange(ctx context.Context, db *gorm.DB, id, session string) error {
	return db.WithContext(ctx).Model(&entity.StudentLoginExchange{}).Where("id = ?", id).Updates(map[string]any{"status": "completed", "session_id": session}).Error
}

func setTimeout(ctx context.Context, db *gorm.DB, timeout time.Duration) error {
	return db.WithContext(ctx).Exec("SELECT set_config('lock_timeout',?,true)", strconv.FormatInt(timeout.Milliseconds(), 10)+"ms").Error
}

func databaseNow(ctx context.Context, db *gorm.DB, out *time.Time) error {
	return db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(out).Error
}

func accountEpoch(ctx context.Context, db *gorm.DB, id string, out *int64) error {
	return db.WithContext(ctx).Model(&entity.Account{}).Select("auth_epoch").Where("id=? AND lifecycle='active'", id).Find(out).Error
}

func insertSession(ctx context.Context, db *gorm.DB, id, account string, hash []byte, epoch int64, expiry time.Time) error {
	return db.WithContext(ctx).Create(&entity.Session{ID: id, AccountID: &account, TokenHash: hash, AuthEpoch: epoch, ExpiresAt: expiry}).Error
}

// authorize 遵循账号guard→会话锁序，所有读取投影和退出也复核当前主体状态。
func authorize(ctx context.Context, db *gorm.DB, token string) (id string, now time.Time, err error) {
	if token == "" {
		return "", now, command.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var refs []struct{ AccountID string }
	if e := db.WithContext(ctx).Model(&entity.Session{}).Select("account_id").Where("token_hash=? AND subject_kind='student'", hash[:]).Find(&refs).Error; e != nil {
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
	if e := db.WithContext(ctx).Model(&entity.Session{}).Select("account_id,auth_epoch,expires_at,revoked_at").Where("token_hash=? AND subject_kind='student'", hash[:]).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&sessions).Error; e != nil {
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
	if e := db.WithContext(ctx).Model(&entity.Account{}).Select("id,version,nickname,verification,hide_identity_tags").Where("id=? AND lifecycle='active'", id).Find(&rows).Error; e != nil {
		return Me{}, e
	}
	if len(rows) != 1 {
		return Me{}, command.ErrUnauthenticated
	}
	out := rows[0]
	out.AllowedActions = []string{"deleteAuthSession"}
	var restrictions int64
	if e := db.WithContext(ctx).Model(&entity.Penalty{}).Where("account_id = ? AND action_kind IN ? AND revoked_at IS NULL AND starts_at <= ? AND (ends_at IS NULL OR ends_at > ?)", id, []string{"mute", "ban"}, now, now).Count(&restrictions).Error; e != nil {
		return Me{}, e
	}
	if restrictions > 0 {
		out.Nickname = ""
		out.PenaltyNotice = "当前账号交互权限受限"
	} else {
		out.AllowedActions = append(out.AllowedActions, "putMeProfile")
	}
	// 基础投影只提供现有会话和资料动作，领域动作由显式装配开关追加，认证本身不授予未装配路由。
	return out, nil
}

// replaceProfile 显式更新零值字段，避免结构体 Updates 忽略 false；版本递增仍在数据库内完成。
func replaceProfile(ctx context.Context, db *gorm.DB, id string, input ProfileInput) error {
	return db.WithContext(ctx).Model(&entity.Account{}).Where("id = ?", id).Updates(map[string]any{"nickname": input.Nickname, "hide_identity_tags": *input.HideIdentityTags, "version": gorm.Expr("version + 1")}).Error
}

func revoke(ctx context.Context, db *gorm.DB, hash []byte, now time.Time) error {
	return db.WithContext(ctx).Model(&entity.Session{}).Where("token_hash = ? AND subject_kind = ?", hash, "student").Update("revoked_at", now).Error
}
