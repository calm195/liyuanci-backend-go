package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// Config 显式注入技术预算、受控年级与共享词库检查，禁止明文或缺依赖降级。
type Config struct {
	Command   command.Config
	Grades    func(context.Context) []string
	CheckText func(context.Context, *gorm.DB, string) error
}

// Service 借用数据库、命令引擎和加密器，不拥有其生命周期。
type Service struct {
	db     *gorm.DB
	engine *command.Engine
	codec  *securedata.Codec
	log    *zap.Logger
	cfg    Config
}

// New 只验证和构造依赖，普通启动不创建业务数据或表结构。
func New(db *gorm.DB, log *zap.Logger, codec *securedata.Codec, cfg Config) (*Service, error) {
	if codec == nil || cfg.Grades == nil || cfg.CheckText == nil {
		return nil, command.ErrInvalid
	}
	engine, err := command.New(db, log, cfg.Command)
	if err != nil {
		return nil, err
	}
	return &Service{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), engine: engine, codec: codec, log: log, cfg: cfg}, nil
}

func safe(err error) error {
	if err == nil {
		return nil
	}
	for _, v := range []error{
		command.ErrInvalid,
		command.ErrUnauthenticated,
		command.ErrForbidden,
		command.ErrNotFound,
		command.ErrVersionConflict,
		command.ErrIdempotencyConflict,
		command.ErrConflict,
		command.ErrVerificationRequired,
		command.ErrReviewPending,
		command.ErrBlockedInteraction,
		command.ErrRetry,
		command.ErrUnavailable,
		command.ErrInternal,
	} {
		if errors.Is(err, v) {
			return v
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return command.ErrNotFound
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		switch state.SQLState() {
		case "23505", "23503", "23514":
			return command.ErrConflict
		case "40001", "40P01", "55P03":
			return command.ErrRetry
		}
	}
	return command.ErrUnavailable
}

func (s *Service) done(op, rid string, start time.Time, err *error) {
	*err = safe(*err)
	result := "success"
	if *err != nil {
		result = "failure"
	}
	s.log.Info("包分配请求完成", zap.String("operation", op), zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

func (s *Service) encrypt(ctx context.Context, row entity.MatchmakingEnrollment, p Profile) (json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	envelope, err := s.codec.Encrypt(ctx, securedata.Binding{Purpose: "matchmaking.profile.v1", ObjectID: row.ID, Version: uint64(row.ProfileVersion)}, raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

func (s *Service) decrypt(ctx context.Context, row entity.MatchmakingEnrollment) (Profile, error) {
	var out Profile
	var envelope securedata.Envelope
	if err := json.Unmarshal(row.ProfileCipher, &envelope); err != nil {
		return out, err
	}
	raw, err := s.codec.Decrypt(ctx, securedata.Binding{Purpose: "matchmaking.profile.v1", ObjectID: row.ID, Version: uint64(row.ProfileVersion)}, envelope)
	if err != nil {
		return out, err
	}
	defer clear(raw)
	err = json.Unmarshal(raw, &out)
	return out, err
}

func activity(db *gorm.DB, id string) (entity.MatchmakingActivity, error) {
	var row entity.MatchmakingActivity
	err := db.Where("id=?", id).Take(&row).Error
	return row, safe(err)
}

func discover(ctx context.Context, db *gorm.DB, id string) (command.Discovery, error) {
	out := command.Discovery{}
	if id == "" {
		return out, nil
	}
	if !validID(id) {
		return out, command.ErrInvalid
	}
	if _, err := activity(db.WithContext(ctx), id); err != nil {
		return out, err
	}
	if err := db.WithContext(ctx).Model(&entity.MatchmakingEnrollment{}).Where("activity_id=?", id).Pluck("account_id", &out.Accounts).Error; err != nil {
		return out, err
	}
	out.Resources = []command.Resource{{Kind: command.MatchmakingActivity, ID: id}}
	return out, nil
}

// eligible 在持锁事务或一致读取快照中复核账号，不缓存历史认证和处罚状态。
func eligible(db *gorm.DB, id string, now time.Time) (bool, error) {
	var count int64
	err := db.Model(&entity.Account{}).Where("id=? AND lifecycle='active' AND verification='verified'", id).Count(&count).Error
	if err != nil || count != 1 {
		return false, err
	}
	err = db.Model(&entity.Penalty{}).Where("account_id=? AND action_kind IN ('ban','mute') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", id, now, now).Count(&count).Error
	return count == 0, err
}

func blocked(db *gorm.DB, a, b string) (bool, error) {
	var count int64
	err := db.Model(&entity.Block{}).Where("(owner_id=? AND target_id=?) OR (owner_id=? AND target_id=?)", a, b, b, a).Count(&count).Error
	return count > 0, err
}

func pairMembers(db *gorm.DB, id string) ([]entity.MatchmakingMember, error) {
	rows := []entity.MatchmakingMember{}
	err := db.Where("pair_id=?", id).Order("enrollment_id").Find(&rows).Error
	if err == nil && len(rows) != 2 {
		return nil, command.ErrConflict
	}
	return rows, err
}

func pairEligible(db *gorm.DB, members []entity.MatchmakingMember, now time.Time) (bool, error) {
	if len(members) != 2 || !members[0].Active || !members[1].Active {
		return false, nil
	}
	for _, m := range members {
		ok, err := eligible(db, m.AccountID, now)
		if err != nil || !ok {
			return false, err
		}
		var enrollment entity.MatchmakingEnrollment
		err = db.Where("id=? AND activity_id=? AND account_id=? AND profile_version=? AND status='approved'", m.EnrollmentID, m.ActivityID, m.AccountID, m.ProfileVersion).Take(&enrollment).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	no, err := blocked(db, members[0].AccountID, members[1].AccountID)
	return !no, err
}

func templates(db *gorm.DB, id string) ([]Template, error) {
	rows := []entity.MatchmakingTemplate{}
	err := db.Where("activity_id=?", id).Order("fixed_day,id").Find(&rows).Error
	out := make([]Template, 0, len(rows))
	for _, v := range rows {
		out = append(out, Template{Kind: v.Kind, Body: v.Body, FixedDay: v.FixedDay, MinDay: v.MinDay})
	}
	return out, err
}

func audit(db *gorm.DB, a, actor, subject, action, reason string, now time.Time) error {
	return db.Create(&entity.MatchmakingAudit{ID: uuid.NewString(), ActivityID: a, ActorID: actor, SubjectRef: subject, Action: action, Reason: reason, At: now}).Error
}

func notify(db *gorm.DB, recipient, kind, ref string, now time.Time) error {
	return db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: recipient, Type: kind, TargetRef: ref, Version: 1, CreatedAt: now}).Error
}

func bump(db *gorm.DB, id string) error {
	return db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).UpdateColumn("version", gorm.Expr("version+1")).Error
}

func enrollment(db *gorm.DB, a, id string) (entity.MatchmakingEnrollment, error) {
	var out entity.MatchmakingEnrollment
	err := db.Where("id=? AND activity_id=?", id, a).Take(&out).Error
	return out, safe(err)
}

func unused(db *gorm.DB, id string) (bool, error) {
	var count int64
	err := db.Model(&entity.MatchmakingMember{}).Where("enrollment_id=? AND active", id).Count(&count).Error
	return count == 0, err
}

func (s *Service) validateEnrollmentProfile(ctx context.Context, p Profile) error {
	if !validProfile(p) || !slices.Contains(campusCodes, p.Campus) || !slices.Contains(s.cfg.Grades(ctx), p.Grade) {
		return command.ErrInvalid
	}
	return nil
}

// recordAccess 只记录敏感资料的技术引用，注销清理将同时删除幸存运营的关联访问审计。
func recordAccess(db *gorm.DB, actor, subject, purpose string, now time.Time) error {
	return db.Create(&entity.AccessAudit{ID: uuid.NewString(), ActorID: actor, SubjectRef: subject, Purpose: purpose, At: now, Outcome: "allowed"}).Error
}
