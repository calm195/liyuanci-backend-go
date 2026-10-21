package adminauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// 实体 CRUD 使用绑定参数；调用方事务拥有锁、提交与资源生命周期，并记录完成结果。
func projection(ctx context.Context, db *gorm.DB, id string) (Account, error) {
	var rows []Account
	if err := db.WithContext(ctx).Model(&entity.AdminAccount{}).Select("id,version,login_name,status='active' AS active").Where("id=? AND login_name IS NOT NULL", id).Find(&rows).Error; err != nil {
		return Account{}, err
	}
	if len(rows) != 1 {
		return Account{}, command.ErrNotFound
	}
	out := rows[0]
	out.Roles = []string{}
	var roles []struct{ Role string }
	if err := db.WithContext(ctx).Model(&entity.AdminRoleBinding{}).Select("role").Where("admin_id=?", id).Order("role").Find(&roles).Error; err != nil {
		return Account{}, err
	}
	for _, r := range roles {
		out.Roles = append(out.Roles, r.Role)
	}
	return out, nil
}

func audit(ctx context.Context, db *gorm.DB, actor, target, action, reason string) error {
	return db.WithContext(ctx).Create(&entity.AdminAccountAction{ID: uuid.NewString(), ActorID: actor, TargetID: target, ActionKind: action, Reason: reason}).Error
}

func (s *Service) lockTimeout(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Exec("SELECT set_config('lock_timeout',?,true)", strconv.FormatInt(s.config.Command.LockTimeout.Milliseconds(), 10)+"ms").Error
}

// authorized 保持账号guard→会话的锁序；命名空间、到期、撤销和当前代次在锁后复核。
func (s *Service) authorized(ctx context.Context, db *gorm.DB, token string) (credential, time.Time, error) {
	if token == "" {
		return credential{}, time.Time{}, ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var refs []struct{ AdminID string }
	if e := db.WithContext(ctx).Model(&entity.Session{}).Select("admin_id").Where("token_hash=? AND subject_kind='admin'", hash[:]).Find(&refs).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	if len(refs) != 1 {
		return credential{}, time.Time{}, ErrUnauthenticated
	}
	if e := db.WithContext(ctx).Exec("SELECT * FROM public.lock_command_admin(?)", refs[0].AdminID).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	var accounts []credential
	if e := db.WithContext(ctx).Model(&entity.AdminAccount{}).Select("id,password_hash,auth_epoch,status").Where("id=?", refs[0].AdminID).Find(&accounts).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	var sessions []struct {
		AdminID   string
		AuthEpoch int64
		ExpiresAt time.Time
		RevokedAt *time.Time
	}
	if e := db.WithContext(ctx).Model(&entity.Session{}).Select("admin_id,auth_epoch,expires_at,revoked_at").Where("token_hash=? AND subject_kind='admin'", hash[:]).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&sessions).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	var now time.Time
	if e := db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	if len(accounts) != 1 || accounts[0].Status != "active" || len(sessions) != 1 || sessions[0].AdminID != accounts[0].ID || sessions[0].RevokedAt != nil || !sessions[0].ExpiresAt.After(now) || sessions[0].AuthEpoch != accounts[0].AuthEpoch {
		return credential{}, now, ErrUnauthenticated
	}
	return accounts[0], now, nil
}

func bootstrapAvailable(ctx context.Context, db *gorm.DB) error {
	db = db.WithContext(ctx)
	if e := db.Exec("SELECT pg_advisory_xact_lock(624109175)").Error; e != nil {
		return e
	}
	var count int64
	if e := db.Model(&entity.AdminAccount{}).Count(&count).Error; e != nil {
		return e
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}
func insertLocal(ctx context.Context, db *gorm.DB, id, name, hash string) error {
	return db.WithContext(ctx).Create(&entity.AdminAccount{ID: id, LoginName: &name, PasswordHash: &hash}).Error
}
func insertRole(ctx context.Context, db *gorm.DB, id, role string) error {
	return db.WithContext(ctx).Create(&entity.AdminRoleBinding{AdminID: id, Role: role}).Error
}
func findByName(ctx context.Context, db *gorm.DB, name string, out *[]credential) error {
	return db.WithContext(ctx).Model(&entity.AdminAccount{}).Select("id", "password_hash", "auth_epoch", "status").Where("login_name = ?", name).Scan(out).Error
}
func findByID(ctx context.Context, db *gorm.DB, id string, out *[]credential) error {
	return db.WithContext(ctx).Model(&entity.AdminAccount{}).Select("id", "password_hash", "auth_epoch", "status").Where("id = ?", id).Scan(out).Error
}
func lockAdmin(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Exec("SELECT * FROM public.lock_command_admin(?)", id).Error
}
func databaseNow(ctx context.Context, db *gorm.DB, out *time.Time) error {
	return db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(out).Error
}
func insertSession(ctx context.Context, db *gorm.DB, id string, hash []byte, epoch int64, now, expiry time.Time) error {
	return db.WithContext(ctx).Create(&entity.Session{ID: uuid.NewString(), AdminID: &id, TokenHash: hash, AuthEpoch: epoch, CreatedAt: now, ExpiresAt: expiry}).Error
}

type passwordReceipt struct {
	RequestHash []byte
	ExpiresAt   time.Time
}

func findReceipt(ctx context.Context, db *gorm.DB, id, scope string, key []byte, out *[]passwordReceipt) error {
	return db.WithContext(ctx).Model(&entity.CommandReceipt{}).Select("request_hash,expires_at").Where("actor_kind='admin' AND actor_id=? AND command_kind=? AND key_hash=?", id, scope, key).Find(out).Error
}
func replacePassword(ctx context.Context, db *gorm.DB, id, hash string, now time.Time) error {
	db = db.WithContext(ctx)
	if e := db.Model(&entity.AdminAccount{}).Where("id = ?", id).Updates(map[string]any{"password_hash": hash, "auth_epoch": gorm.Expr("auth_epoch + 1"), "version": gorm.Expr("version + 1")}).Error; e != nil {
		return e
	}
	return db.Model(&entity.Session{}).Where("admin_id = ? AND revoked_at IS NULL", id).Update("revoked_at", now).Error
}
func saveReceipt(ctx context.Context, db *gorm.DB, id, scope string, key, digest []byte, expiry time.Time) error {
	// 冲突只替换回执内容，保留主体和幂等键；调用方事务统一提交业务结果。
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "actor_kind"}, {Name: "actor_id"}, {Name: "command_kind"}, {Name: "key_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"request_hash", "result_ref", "expires_at"}),
	}).Create(&entity.CommandReceipt{ActorKind: "admin", ActorID: id, CommandKind: scope, KeyHash: key, RequestHash: digest, ResultRef: id, ExpiresAt: expiry}).Error
}
func revokeSession(ctx context.Context, db *gorm.DB, hash []byte, now time.Time) error {
	return db.WithContext(ctx).Model(&entity.Session{}).Where("token_hash = ? AND subject_kind = ?", hash, "admin").Update("revoked_at", now).Error
}

// checkSchema 只检查已显式交付结构及读取权限，不执行迁移或修补旧账号。
func checkSchema(ctx context.Context, db *gorm.DB) error {
	if e := db.WithContext(ctx).Select("login_name").Limit(0).Find(&[]entity.AdminAccount{}).Error; e != nil {
		return e
	}
	if e := db.WithContext(ctx).Select("id").Limit(0).Find(&[]entity.AdminAccountAction{}).Error; e != nil {
		return e
	}
	// 启用角色写入口前要求显式迁移已扩展留迹动作，避免旧结构下部分能力启动成功。
	var ready bool
	if e := db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint
 WHERE conrelid='public.admin_account_actions'::regclass AND conname='admin_account_actions_action_kind_check'
 AND pg_catalog.pg_get_constraintdef(oid) LIKE '%roles_change%')`).Scan(&ready).Error; e != nil {
		return e
	}
	if !ready {
		return ErrUnavailable
	}
	return nil
}

// precheckSession 只作事务外的计算资源准入；真正授权仍在持guard后复核，不能用此快照提交业务。
func precheckSession(ctx context.Context, db *gorm.DB, token string) error {
	if token == "" {
		return ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	var valid int64
	err := db.WithContext(ctx).Model(&entity.Session{}).Table("public.sessions AS s").
		Joins("JOIN public.admin_accounts AS a ON a.id = s.admin_id").
		Where("s.token_hash = ? AND s.subject_kind = ? AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp() AND a.status = ? AND a.auth_epoch = s.auth_epoch", digest[:], "admin", "active").Count(&valid).Error
	if err != nil {
		return err
	}
	if valid == 0 {
		return ErrUnauthenticated
	}
	return nil
}

// accountRows 固定UUID升序作键集分页；单条查询聚合角色，避免分页过程中读取混合角色快照。
func accountRows(ctx context.Context, db *gorm.DB, after string, limit int) ([]Account, error) {
	var rows []struct {
		ID        string
		Version   int64
		LoginName string
		Active    bool
		RoleJSON  []byte
	}
	// JSON 聚合保留 PostgreSQL 表达式，分页与过滤交给实体查询，仍只读取一次角色快照。
	query := db.WithContext(ctx).Model(&entity.AdminAccount{}).Table("public.admin_accounts AS a").
		Select(`a.id,a.version,a.login_name,a.status='active' AS active,
 coalesce((SELECT json_agg(r.role ORDER BY r.role) FROM public.admin_role_bindings r WHERE r.admin_id=a.id),'[]'::json) AS role_json`).
		Where("a.login_name IS NOT NULL")
	if after != "" {
		query = query.Where("a.id > ?::uuid", after)
	}
	if err := query.Order("a.id").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows))
	for _, row := range rows {
		roles := []string{}
		if err := json.Unmarshal(row.RoleJSON, &roles); err != nil {
			return nil, err
		}
		out = append(out, Account{ID: row.ID, Version: row.Version, LoginName: row.LoginName, Active: row.Active, Roles: roles})
	}
	return out, nil
}

// replaceRoles 的调用方已按全局顺序锁定目标guard；角色、版本和留迹必须共同提交。
func replaceRoles(ctx context.Context, db *gorm.DB, target string, roles []string) error {
	db = db.WithContext(ctx)
	if e := db.Where("admin_id = ?", target).Delete(&entity.AdminRoleBinding{}).Error; e != nil {
		return e
	}
	// 角色使用单次批量写入；空集合只删除，不调用空切片 Create。
	if len(roles) > 0 {
		bindings := make([]entity.AdminRoleBinding, len(roles))
		for i, role := range roles {
			bindings[i] = entity.AdminRoleBinding{AdminID: target, Role: role}
		}
		if e := db.Create(&bindings).Error; e != nil {
			return e
		}
	}
	return db.Model(&entity.AdminAccount{}).Where("id = ?", target).Update("version", gorm.Expr("version + 1")).Error
}
