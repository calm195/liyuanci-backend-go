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
	"github.com/calm195/liyuanci-backend-go/internal/modules/education"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

var campusCodes = []string{"wushan", "university_town", "international"}

// AccountProfile 是本人可编辑的账号资料，Ready 表示资料已足以报名。
type AccountProfile struct {
	Version         int64            `json:"version"`
	AccountVersion  int64            `json:"account_version"`
	Profile         *Profile         `json:"profile,omitempty"`
	Ready           bool             `json:"ready"`
	TraitSelections []TraitSelection `json:"trait_selections"`
}

// ProfileInput 使用乐观版本保护账号资料的并发保存。
type ProfileInput struct {
	ExpectedVersion int64       `json:"expected_version"`
	Profile         Preferences `json:"profile"`
}

func profileBinding(row entity.MatchmakingProfile) securedata.Binding {
	return securedata.Binding{
		Purpose:  "matchmaking.account_profile.v1",
		ObjectID: row.AccountID,
		Version:  uint64(row.Version),
	}
}

func (s *Service) accountProfile(db *gorm.DB, id string) (AccountProfile, error) {
	var account entity.Account
	if err := db.Select("nickname", "identity_tags", "mbti", "version", "enrollment_year", "education_level", "grade_override").Where("id=?", id).Take(&account).Error; err != nil {
		return AccountProfile{}, err
	}
	selections, err := readTraitSelections(db, id)
	if err != nil {
		return AccountProfile{}, err
	}
	result := AccountProfile{AccountVersion: account.Version, TraitSelections: selections}
	var row entity.MatchmakingProfile
	if err := db.Where("account_id=?", id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, nil
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
	if err = json.Unmarshal(raw, &profile.Preferences); err != nil {
		return AccountProfile{}, err
	}
	var tags struct {
		Campus string `json:"campus"`
		Gender string `json:"gender"`
		Grade  string `json:"grade"`
	}
	if err = json.Unmarshal(account.IdentityTags, &tags); err != nil {
		return AccountProfile{}, err
	}
	// 报名预览与提交都使用锁内读取的账号资料，既有报名快照独立保留。
	profile.Name, profile.MBTI = account.Nickname, account.MBTI
	profile.Gender, profile.Campus = tags.Gender, tags.Campus
	var now time.Time
	if err = db.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
		return AccountProfile{}, err
	}
	profile.EnrollmentYear, profile.EducationLevel, profile.GradeOverride = account.EnrollmentYear, account.EducationLevel, account.GradeOverride
	profile.Grade = education.GradeCode(account.EnrollmentYear, account.EducationLevel, account.GradeOverride, tags.Grade, now)
	if education.ValidGradeCode(profile.Grade) {
		profile.GradeLabel = education.Label(profile.Grade)
	}
	profile.PreferredCampuses = uniqueCodes(profile.PreferredCampuses)
	profile.PreferredMBTI = uniqueCodes(profile.PreferredMBTI)
	if profile.Gender == "undisclosed" {
		profile.Gender = ""
	}
	if profile.Campus == "any" {
		profile.Campus = ""
	}
	profile.IdealTraits = []string{}
	profile.PersonalTraits = []string{}
	for _, selection := range selections {
		if selection.Status == "approved" {
			if selection.Purpose == "personal" {
				profile.PersonalTraits = append(profile.PersonalTraits, selection.Name)
			} else {
				profile.IdealTraits = append(profile.IdealTraits, selection.Name)
			}
		}
	}
	profile.Consent = true
	result.Ready = s.validateEnrollmentProfile(ctx, profile) == nil
	profile.Consent = false
	result.Version, result.Profile = row.Version, &profile
	return result, nil
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

// SaveProfile 只维护包分配专用资料，通用字段由账号资料入口维护。
func (s *Service) SaveProfile(ctx context.Context, token, key, rid string, input ProfileInput) (out Receipt, err error) {
	defer s.done("profile_save", rid, time.Now(), &err)
	if input.ExpectedVersion < 0 || !validAccountProfile(input.Profile) {
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
		// 省略偏好字段保留已保存值；显式空数组恢复不限。
		if !fresh && (p.PreferredCampuses == nil || p.PreferredMBTI == nil) {
			previous, e := s.accountProfile(tx.DB(), id)
			if e != nil {
				return "", e
			}
			if previous.Profile != nil {
				if p.PreferredCampuses == nil {
					p.PreferredCampuses = previous.Profile.PreferredCampuses
				}
				if p.PreferredMBTI == nil {
					p.PreferredMBTI = previous.Profile.PreferredMBTI
				}
			}
		}
		p.PreferredCampuses = uniqueCodes(p.PreferredCampuses)
		p.PreferredMBTI = uniqueCodes(p.PreferredMBTI)
		if p.Introduction != "" || p.Wechat != "" {
			if err = s.cfg.CheckText(ctx, tx.DB(), p.Introduction+"\n"+p.Wechat); err != nil {
				return "", err
			}
		}
		if err = s.saveTraitSelections(ctx, tx.DB(), id, p, tx.Now()); err != nil {
			return "", err
		}
		// 当前选择以关系表为准，密文不重复保存待审核标签。
		p.IdealTraits = nil
		p.PersonalTraits = nil
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

func validAccountProfile(p Preferences) bool {
	if !validPreferenceCodes(p.PreferredCampuses, campusCodes) || !validPreferenceCodes(p.PreferredMBTI, mbtis) {
		return false
	}
	if _, err := normalizeTraits(p.PersonalTraits); err != nil {
		return false
	}
	if _, err := normalizeTraits(p.IdealTraits); err != nil {
		return false
	}
	if !optionalText(p.Introduction, 500) || !optionalText(p.Wechat, 64) {
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

func validPreferenceCodes(values, allowed []string) bool {
	if len(values) > 64 {
		return false
	}
	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return false
		}
	}
	return true
}

func uniqueCodes(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	slices.Sort(out)
	return out
}
