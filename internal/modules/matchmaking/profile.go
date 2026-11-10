package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

var campusCodes = []string{"wushan", "university_town", "international"}
var educationCodes = []string{"本科生", "研究生", "博士生"}

// AccountProfile 是本人可编辑的账号资料，Ready 表示资料已足以报名。
type AccountProfile struct {
	Version int64    `json:"version"`
	Profile *Profile `json:"profile,omitempty"`
	Ready   bool     `json:"ready"`
}

// ProfileInput 使用乐观版本保护账号资料的并发保存。
type ProfileInput struct {
	ExpectedVersion int64   `json:"expected_version"`
	Profile         Profile `json:"profile"`
}

func profileBinding(row entity.MatchmakingProfile) securedata.Binding {
	return securedata.Binding{
		Purpose:  "matchmaking.account_profile.v1",
		ObjectID: row.AccountID,
		Version:  uint64(row.Version),
	}
}

func (s *Service) accountProfile(db *gorm.DB, id string) (AccountProfile, error) {
	var row entity.MatchmakingProfile
	if err := db.Where("account_id=?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AccountProfile{}, nil
		}
		return AccountProfile{}, err
	}
	var envelope securedata.Envelope
	if err := json.Unmarshal(row.ProfileCipher, &envelope); err != nil {
		return AccountProfile{}, err
	}
	ctx := db.Statement.Context
	raw, err := s.codec.Decrypt(ctx, profileBinding(row), envelope)
	if err != nil {
		return AccountProfile{}, err
	}
	defer clear(raw)
	var profile Profile
	if err = json.Unmarshal(raw, &profile); err != nil {
		return AccountProfile{}, err
	}
	profile.Consent = true
	ready := s.validateEnrollmentProfile(ctx, profile) == nil
	profile.Consent = false
	return AccountProfile{Version: row.Version, Profile: &profile, Ready: ready}, nil
}

// ReadProfile 仅返回当前账号的资料，不读取历史报名快照。
func (s *Service) ReadProfile(ctx context.Context, token, rid string) (out AccountProfile, err error) {
	defer s.done("profile_read", rid, time.Now(), &err)
	err = s.read(ctx, token, "", false, func(db *gorm.DB, actor reader) error {
		var e error
		out, e = s.accountProfile(db, actor.ID)
		return e
	})
	return out, err
}

// SaveProfile 允许先保存部分资料，例如单独维护人格标签。
func (s *Service) SaveProfile(ctx context.Context, token, key, rid string, input ProfileInput) (out Receipt, err error) {
	defer s.done("profile_save", rid, time.Now(), &err)
	input.Profile.Consent = false
	if input.ExpectedVersion < 0 || !s.validAccountProfile(ctx, input.Profile) {
		return Receipt{}, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, "", "profile_save", "PUT", "/api/v1/matchmaking/profile", input, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		id := tx.Actor().ID
		var row entity.MatchmakingProfile
		err := tx.DB().Where("account_id=?", id).Take(&row).Error
		fresh := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !fresh {
			return "", err
		}
		if row.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		p := input.Profile
		if p.Name != "" || p.Introduction != "" || p.Wechat != "" {
			if err = s.cfg.CheckText(ctx, tx.DB(), p.Name+"\n"+p.Introduction+"\n"+p.Wechat); err != nil {
				return "", err
			}
		}
		row.AccountID = id
		row.Version++
		raw, err := json.Marshal(p)
		if err != nil {
			return "", err
		}
		defer clear(raw)
		envelope, err := s.codec.Encrypt(ctx, profileBinding(row), raw)
		if err != nil {
			return "", err
		}
		row.ProfileCipher, err = json.Marshal(envelope)
		if err != nil {
			return "", err
		}
		if fresh {
			err = tx.DB().Create(&row).Error
		} else {
			err = tx.DB().Model(&row).Select("version", "profile_cipher").Updates(&row).Error
		}
		return id, err
	}, nil)
}

func (s *Service) validAccountProfile(ctx context.Context, p Profile) bool {
	if !optionalText(p.Name, 32) || !optionalText(p.Introduction, 500) || !optionalText(p.Wechat, 64) {
		return false
	}
	if p.MBTI != "" && !slices.Contains(mbtis, p.MBTI) ||
		p.Gender != "" && !slices.Contains([]string{"male", "female"}, p.Gender) ||
		p.Campus != "" && !slices.Contains(campusCodes, p.Campus) ||
		p.Education != "" && !slices.Contains(educationCodes, p.Education) ||
		p.Grade != "" && !slices.Contains(s.cfg.Grades(ctx), p.Grade) {
		return false
	}
	return validOptionalRange(p.Age, p.AgeMin, p.AgeMax, 18, 80) &&
		validOptionalRange(p.Height, p.HeightMin, p.HeightMax, 100, 250) &&
		validOptionalRange(p.Weight, p.WeightMin, p.WeightMax, 50, 500)
}

func optionalText(value string, limit int) bool {
	return value == "" || text(value, limit)
}

func validOptionalRange(value, lower, upper, min, max int) bool {
	return (value == 0 || in(value, min, max)) &&
		(lower == 0 || in(lower, min, max)) &&
		(upper == 0 || in(upper, min, max)) &&
		(lower == 0 || upper == 0 || lower <= upper)
}
