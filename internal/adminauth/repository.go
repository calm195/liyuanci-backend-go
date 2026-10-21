package adminauth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// 查询只使用服务端固定SQL及绑定参数；调用方事务拥有锁、提交与资源生命周期。
func projection(ctx context.Context, db *gorm.DB, id string) (Account, error) {
	var rows []Account
	if err := db.WithContext(ctx).Raw("SELECT id,version,login_name,status='active' AS active FROM public.admin_accounts WHERE id=? AND login_name IS NOT NULL", id).Scan(&rows).Error; err != nil {
		return Account{}, err
	}
	if len(rows) != 1 {
		return Account{}, command.ErrNotFound
	}
	out := rows[0]
	out.Roles = []string{}
	var roles []struct{ Role string }
	if err := db.WithContext(ctx).Raw("SELECT role FROM public.admin_role_bindings WHERE admin_id=? ORDER BY role", id).Scan(&roles).Error; err != nil {
		return Account{}, err
	}
	for _, r := range roles {
		out.Roles = append(out.Roles, r.Role)
	}
	return out, nil
}

func audit(ctx context.Context, db *gorm.DB, actor, target, action, reason string) error {
	return db.WithContext(ctx).Exec("INSERT INTO public.admin_account_actions(id,actor_id,target_id,action_kind,reason) VALUES (?,?,?,?,?)", uuid.NewString(), actor, target, action, reason).Error
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
	if e := db.WithContext(ctx).Raw("SELECT admin_id FROM public.sessions WHERE token_hash=? AND subject_kind='admin'", hash[:]).Scan(&refs).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	if len(refs) != 1 {
		return credential{}, time.Time{}, ErrUnauthenticated
	}
	if e := db.WithContext(ctx).Exec("SELECT * FROM public.lock_command_admin(?)", refs[0].AdminID).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	var accounts []credential
	if e := db.WithContext(ctx).Raw("SELECT id,password_hash,auth_epoch,status FROM public.admin_accounts WHERE id=?", refs[0].AdminID).Scan(&accounts).Error; e != nil {
		return credential{}, time.Time{}, e
	}
	var sessions []struct {
		AdminID   string
		AuthEpoch int64
		ExpiresAt time.Time
		RevokedAt *time.Time
	}
	if e := db.WithContext(ctx).Raw("SELECT admin_id,auth_epoch,expires_at,revoked_at FROM public.sessions WHERE token_hash=? AND subject_kind='admin' FOR UPDATE", hash[:]).Scan(&sessions).Error; e != nil {
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
	if e := db.Table("public.admin_accounts").Count(&count).Error; e != nil {
		return e
	}
	if count != 0 {
		return ErrConflict
	}
	return nil
}
func insertLocal(ctx context.Context, db *gorm.DB, id, name, hash string) error {
	return db.WithContext(ctx).Exec("INSERT INTO public.admin_accounts(id,login_name,password_hash) VALUES (?,?,?)", id, name, hash).Error
}
func insertRole(ctx context.Context, db *gorm.DB, id, role string) error {
	return db.WithContext(ctx).Exec("INSERT INTO public.admin_role_bindings(admin_id,role) VALUES (?,?)", id, role).Error
}
func findByName(ctx context.Context, db *gorm.DB, name string, out *[]credential) error {
	return db.WithContext(ctx).Raw("SELECT id,password_hash,auth_epoch,status FROM public.admin_accounts WHERE login_name=?", name).Scan(out).Error
}
func findByID(ctx context.Context, db *gorm.DB, id string, out *[]credential) error {
	return db.WithContext(ctx).Raw("SELECT id,password_hash,auth_epoch,status FROM public.admin_accounts WHERE id=?", id).Scan(out).Error
}
func lockAdmin(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Exec("SELECT * FROM public.lock_command_admin(?)", id).Error
}
func databaseNow(ctx context.Context, db *gorm.DB, out *time.Time) error {
	return db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(out).Error
}
func insertSession(ctx context.Context, db *gorm.DB, id string, hash []byte, epoch int64, now, expiry time.Time) error {
	return db.WithContext(ctx).Exec("INSERT INTO public.sessions(id,admin_id,token_hash,auth_epoch,created_at,expires_at) VALUES (?,?,?,?,?,?)", uuid.NewString(), id, hash, epoch, now, expiry).Error
}

type passwordReceipt struct {
	RequestHash []byte
	ExpiresAt   time.Time
}

func findReceipt(ctx context.Context, db *gorm.DB, id, scope string, key []byte, out *[]passwordReceipt) error {
	return db.WithContext(ctx).Raw("SELECT request_hash,expires_at FROM public.command_receipts WHERE actor_kind='admin' AND actor_id=? AND command_kind=? AND key_hash=?", id, scope, key).Scan(out).Error
}
func replacePassword(ctx context.Context, db *gorm.DB, id, hash string, now time.Time) error {
	db = db.WithContext(ctx)
	if e := db.Exec("UPDATE public.admin_accounts SET password_hash=?,auth_epoch=auth_epoch+1,version=version+1 WHERE id=?", hash, id).Error; e != nil {
		return e
	}
	return db.Exec("UPDATE public.sessions SET revoked_at=? WHERE admin_id=? AND revoked_at IS NULL", now, id).Error
}
func saveReceipt(ctx context.Context, db *gorm.DB, id, scope string, key, digest []byte, expiry time.Time) error {
	return db.WithContext(ctx).Exec(`INSERT INTO public.command_receipts(actor_kind,actor_id,command_kind,key_hash,request_hash,result_ref,expires_at) VALUES ('admin',?,?,?,?,?,?)
ON CONFLICT(actor_kind,actor_id,command_kind,key_hash) DO UPDATE SET request_hash=EXCLUDED.request_hash,result_ref=EXCLUDED.result_ref,expires_at=EXCLUDED.expires_at`, id, scope, key, digest, id, expiry).Error
}
func revokeSession(ctx context.Context, db *gorm.DB, hash []byte, now time.Time) error {
	return db.WithContext(ctx).Exec("UPDATE public.sessions SET revoked_at=? WHERE token_hash=? AND subject_kind='admin'", now, hash).Error
}

// checkSchema 只检查已显式交付结构及读取权限，不执行迁移或修补旧账号。
func checkSchema(ctx context.Context, db *gorm.DB) error {
	if e := db.WithContext(ctx).Exec("SELECT login_name FROM public.admin_accounts LIMIT 0").Error; e != nil {
		return e
	}
	return db.WithContext(ctx).Exec("SELECT id FROM public.admin_account_actions LIMIT 0").Error
}

// precheckSession 只作事务外的计算资源准入；真正授权仍在持guard后复核，不能用此快照提交业务。
func precheckSession(ctx context.Context, db *gorm.DB, token string) error {
	if token == "" {
		return ErrUnauthenticated
	}
	digest := sha256.Sum256([]byte(token))
	var valid bool
	err := db.WithContext(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM public.sessions s JOIN public.admin_accounts a ON a.id=s.admin_id
WHERE s.token_hash=? AND s.subject_kind='admin' AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND a.status='active' AND a.auth_epoch=s.auth_epoch)`, digest[:]).Scan(&valid).Error
	if err != nil {
		return err
	}
	if !valid {
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
	query := `SELECT a.id,a.version,a.login_name,a.status='active' AS active,
 coalesce((SELECT json_agg(r.role ORDER BY r.role) FROM public.admin_role_bindings r WHERE r.admin_id=a.id),'[]'::json) AS role_json
 FROM public.admin_accounts a WHERE a.login_name IS NOT NULL`
	args := []any{}
	if after != "" {
		query += " AND a.id>?::uuid"
		args = append(args, after)
	}
	query += " ORDER BY a.id LIMIT ?"
	args = append(args, limit)
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
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
	if e := db.Exec("DELETE FROM public.admin_role_bindings WHERE admin_id=?", target).Error; e != nil {
		return e
	}
	for _, role := range roles {
		if e := insertRole(ctx, db, target, role); e != nil {
			return e
		}
	}
	return db.Exec("UPDATE public.admin_accounts SET version=version+1 WHERE id=?", target).Error
}
