package command

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func ids(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	for _, value := range result {
		if !uuid(value) {
			return nil, ErrInvalid
		}
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}

func rank(kind ResourceKind) int {
	switch kind {
	case Asset:
		return 0
	case IdentityReview:
		return 1
	case Wish:
		return 2
	case Application:
		return 3
	case Match:
		return 4
	case Conversation:
		return 5
	case ModerationTask:
		return 6
	case Message:
		return 7
	case Notification:
		return 8
	case Report:
		return 9
	case Penalty:
		return 10
	case Appeal:
		return 11
	case CrisisCase:
		return 12
	case EvidenceCase:
		return 13
	default:
		return -1
	}
}

func normalize(discovery Discovery, subject Subject) (Discovery, error) {
	if !uuid(subject.ID) || (subject.Kind != "student" && subject.Kind != "admin") {
		return Discovery{}, ErrInvalid
	}
	accounts := append([]string(nil), discovery.Accounts...)
	admins := append([]string(nil), discovery.Admins...)
	if subject.Kind == "student" {
		accounts = append(accounts, subject.ID)
	} else {
		admins = append(admins, subject.ID)
	}
	discovery.Accounts = accounts
	discovery.Admins = admins
	return NormalizePlan(discovery)
}

// NormalizePlan 排序并校验后台工作发现的完整参与者集合，不隐式加入客户端主体。
func NormalizePlan(discovery Discovery) (Discovery, error) {
	var err error
	discovery.Accounts, err = ids(discovery.Accounts)
	if err != nil {
		return Discovery{}, err
	}
	discovery.Admins, err = ids(discovery.Admins)
	if err != nil {
		return Discovery{}, err
	}
	discovery.Resources = append([]Resource(nil), discovery.Resources...)
	for _, ref := range discovery.Resources {
		if rank(ref.Kind) < 0 || !uuid(ref.ID) {
			return Discovery{}, ErrInvalid
		}
	}
	slices.SortFunc(discovery.Resources, func(a, b Resource) int {
		if rank(a.Kind) != rank(b.Kind) {
			return rank(a.Kind) - rank(b.Kind)
		}
		return strings.Compare(a.ID, b.ID)
	})
	discovery.Resources = slices.Compact(discovery.Resources)
	return discovery, nil
}

func same(a, b Discovery) bool {
	return slices.Equal(a.Accounts, b.Accounts) && slices.Equal(a.Admins, b.Admins) && slices.Equal(a.Resources, b.Resources)
}

type adminGuard struct {
	ID        string
	Status    string
	AuthEpoch int64
}

func (e *Engine) lock(ctx context.Context, db *gorm.DB, subject Subject, discovery Discovery) (*Tx, error) {
	if err := db.Exec("SELECT set_config('lock_timeout', ?, true)", strconv.FormatInt(e.config.LockTimeout.Milliseconds(), 10)+"ms").Error; err != nil {
		return nil, err
	}
	tx := &Tx{db: db.WithContext(ctx), actor: Actor{Subject: subject}, guards: make(map[string]Guard), resources: make(map[Resource]Snapshot)}
	// 独立后台主体先于业务账号锁，所有参与者集合在初次只读发现时就确定。
	for _, id := range discovery.Admins {
		var guards []adminGuard
		if err := db.Raw("SELECT id,status,auth_epoch FROM public.lock_command_admin(?)", id).Scan(&guards).Error; err != nil {
			return nil, err
		}
		if len(guards) != 1 {
			if subject.Kind == "admin" && id == subject.ID {
				return nil, ErrUnauthenticated
			}
			return nil, ErrNotFound
		}
		if subject.Kind == "admin" && id == subject.ID {
			if guards[0].Status != "active" {
				return nil, ErrUnauthenticated
			}
			tx.actor.AuthEpoch = guards[0].AuthEpoch
		}
	}
	for _, id := range discovery.Accounts {
		var guards []Guard
		if err := db.Raw("SELECT id,lifecycle,verification,auth_epoch,version FROM public.lock_command_account(?)", id).Scan(&guards).Error; err != nil {
			return nil, err
		}
		if len(guards) != 1 {
			if subject.Kind == "student" && id == subject.ID {
				return nil, ErrUnauthenticated
			}
			return nil, ErrNotFound
		}
		tx.guards[id] = guards[0]
		if subject.Kind == "student" && id == subject.ID {
			tx.actor.AuthEpoch = guards[0].AuthEpoch
		}
	}
	return tx, nil
}

func (t *Tx) lockResources(resources []Resource) error {
	for _, ref := range resources {
		// 表由封闭资源类型选择；投影缺少的版本字段保持零值，不引入动态表名输入。
		var model any
		columns := []string{"id"}
		switch ref.Kind {
		case Asset:
			model = &entity.Asset{}
			columns = append(columns, "version")
		case IdentityReview:
			model = &entity.IdentityReview{}
			columns = append(columns, "version")
		case Wish:
			model = &entity.Wish{}
			columns = append(columns, "version")
		case Application:
			model = &entity.Application{}
			columns = append(columns, "version")
		case Match:
			model = &entity.Match{}
		case Conversation:
			model = &entity.Conversation{}
			columns = append(columns, "write_epoch", "version")
		case ModerationTask:
			model = &entity.ModerationTask{}
			columns = append(columns, "version")
		case Notification:
			model = &entity.Notification{}
			columns = append(columns, "version")
		case Report:
			model = &entity.Report{}
			columns = append(columns, "version")
		case Penalty:
			model = &entity.Penalty{}
			columns = append(columns, "version")
		case Appeal:
			model = &entity.Appeal{}
			columns = append(columns, "version")
		case CrisisCase:
			model = &entity.EvidenceCrisisCase{}
			columns = append(columns, "version")
		case EvidenceCase:
			model = &entity.EvidenceCase{}
			columns = append(columns, "version")
		case Message:
			model = &entity.Message{}
			columns = append(columns, "version")
		default:
			return ErrInvalid
		}
		var rows []struct {
			ID         string
			Version    int64
			WriteEpoch int64
		}
		if err := t.db.Model(model).Select(columns).Where("id = ?", ref.ID).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != 1 {
			return ErrNotFound
		}
		t.resources[ref] = Snapshot{Resource: ref, Version: rows[0].Version, WriteEpoch: rows[0].WriteEpoch}
	}
	return nil
}

func (e *Engine) authorize(db *gorm.DB, tx *Tx, d Definition, hash []byte) error {
	var sessions []struct {
		SubjectID string
		AuthEpoch int64
		ExpiresAt time.Time
		RevokedAt *time.Time
	}
	if err := db.Model(&entity.Session{}).Select("subject_id,auth_epoch,expires_at,revoked_at").Where("token_hash=? AND subject_kind=?", hash, d.ActorKind).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&sessions).Error; err != nil {
		return err
	}
	if len(sessions) != 1 || sessions[0].SubjectID != tx.actor.ID || sessions[0].RevokedAt != nil || !sessions[0].ExpiresAt.After(tx.now) || sessions[0].AuthEpoch != tx.actor.AuthEpoch {
		return ErrUnauthenticated
	}
	if d.ActorKind == "admin" {
		var roles []struct{ Role string }
		if err := db.Model(&entity.AdminRoleBinding{}).Select("role").Where("admin_id=?", tx.actor.ID).Order("role").Find(&roles).Error; err != nil {
			return err
		}
		allowed := false
		for _, row := range roles {
			tx.actor.Roles = append(tx.actor.Roles, row.Role)
			if slices.Contains(d.AllowedRoles, row.Role) {
				allowed = true
			}
		}
		if !allowed {
			return ErrForbidden
		}
		return nil
	}
	guard := tx.guards[tx.actor.ID]
	if guard.Lifecycle != "active" {
		return ErrForbidden
	}
	if d.Access == Verified && guard.Verification != "verified" {
		return ErrForbidden
	}
	if d.Access != Restricted {
		var count int64
		if err := db.Model(&entity.Penalty{}).Where("account_id = ? AND action_kind IN ? AND revoked_at IS NULL AND starts_at <= ? AND (ends_at IS NULL OR ends_at > ?)", guard.ID, []string{"mute", "ban"}, tx.now, tx.now).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrForbidden
		}
	}
	return nil
}

// LockPlan 在调用方现有事务中按统一锁序取得后台工作依赖；本函数不提交或决定业务资格。
// 调用方必须重发现参与者、锁工作源、校验当前版本/资格，并在同事务提交结果与消费回执。
func LockPlan(ctx context.Context, db *gorm.DB, plan Discovery, timeout time.Duration) (*Tx, error) {
	if db == nil || db.Statement == nil || timeout < time.Millisecond {
		return nil, ErrInvalid
	}
	if _, ok := db.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, ErrInvalid
	}
	normalized, err := NormalizePlan(plan)
	if err != nil {
		return nil, err
	}
	engine := &Engine{config: Config{LockTimeout: timeout}}
	tx, err := engine.lock(ctx, db, Subject{}, normalized)
	if err != nil {
		return nil, normalizeError(err)
	}
	if err = tx.lockResources(normalized.Resources); err != nil {
		return nil, normalizeError(err)
	}
	if err = db.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&tx.now).Error; err != nil {
		return nil, normalizeError(err)
	}
	return tx, nil
}
