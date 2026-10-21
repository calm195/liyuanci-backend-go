package command

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
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
	case Wish:
		return 0
	case Application:
		return 1
	case Match:
		return 2
	case Conversation:
		return 3
	case ModerationTask:
		return 4
	case Message:
		return 5
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
	var err error
	discovery.Accounts, err = ids(accounts)
	if err != nil {
		return Discovery{}, err
	}
	discovery.Admins, err = ids(admins)
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
		if err := db.Raw("SELECT id,lifecycle,verification,auth_epoch,version FROM public.accounts WHERE id=? FOR UPDATE", id).Scan(&guards).Error; err != nil {
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
		var query string
		switch ref.Kind {
		case Wish:
			query = "SELECT id,version,0 AS write_epoch FROM public.wishes WHERE id=? FOR UPDATE"
		case Application:
			query = "SELECT id,version,0 AS write_epoch FROM public.applications WHERE id=? FOR UPDATE"
		case Match:
			query = "SELECT id,0 AS version,0 AS write_epoch FROM public.matches WHERE id=? FOR UPDATE"
		case Conversation:
			query = "SELECT id,0 AS version,write_epoch FROM public.conversations WHERE id=? FOR UPDATE"
		case ModerationTask:
			query = "SELECT id,0 AS version,0 AS write_epoch FROM public.moderation_tasks WHERE id=? FOR UPDATE"
		case Message:
			query = "SELECT id,version,0 AS write_epoch FROM public.messages WHERE id=? FOR UPDATE"
		default:
			return ErrInvalid
		}
		var rows []struct {
			ID         string
			Version    int64
			WriteEpoch int64
		}
		if err := t.db.Raw(query, ref.ID).Scan(&rows).Error; err != nil {
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
	if err := db.Raw("SELECT subject_id,auth_epoch,expires_at,revoked_at FROM public.sessions WHERE token_hash=? AND subject_kind=? FOR UPDATE", hash, d.ActorKind).Scan(&sessions).Error; err != nil {
		return err
	}
	if len(sessions) != 1 || sessions[0].SubjectID != tx.actor.ID || sessions[0].RevokedAt != nil || !sessions[0].ExpiresAt.After(tx.now) || sessions[0].AuthEpoch != tx.actor.AuthEpoch {
		return ErrUnauthenticated
	}
	if d.ActorKind == "admin" {
		var roles []struct{ Role string }
		if err := db.Raw("SELECT role FROM public.admin_role_bindings WHERE admin_id=? ORDER BY role", tx.actor.ID).Scan(&roles).Error; err != nil {
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
		if err := db.Raw(`SELECT count(*) FROM public.penalties WHERE account_id=? AND action_kind IN ('mute','ban') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)`, guard.ID, tx.now, tx.now).Scan(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrForbidden
		}
	}
	return nil
}
