package matchmaking

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func validTraitCategory(value string) bool {
	return slices.Contains([]string{"personality", "interest", "lifestyle", "uncategorized"}, value)
}

func validTraitReason(value string) bool {
	return text(strings.TrimSpace(value), 500) && !strings.ContainsRune(value, 0)
}

// profileTraitCategories 只描述本次选用词条，新客户端不能借分类映射修改其他共享词条。
func profileTraitCategories(profile Preferences) (map[string]string, error) {
	out := map[string]string{}
	for _, group := range []struct {
		names      []string
		categories map[string]string
	}{{profile.PersonalTraits, profile.PersonalTraitCategories}, {profile.IdealTraits, profile.IdealTraitCategories}} {
		names, err := normalizeTraits(group.names)
		if err != nil {
			return nil, err
		}
		for raw, category := range group.categories {
			keys, err := normalizeTraits([]string{raw})
			if err != nil || len(keys) != 1 || !validTraitCategory(category) || !slices.Contains(names, keys[0]) {
				return nil, command.ErrInvalid
			}
			name := keys[0]
			if previous, exists := out[name]; exists && previous != category {
				return nil, command.ErrInvalid
			}
			out[name] = category
		}
	}
	return out, nil
}

// TraitCreateInput 创建共享词条，审核结论仍来自现有词库扫描。
type TraitCreateInput struct {
	Name     string `json:"name"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
}

// TraitUpdateInput 分开维护分类、可选状态和审核结论；Enabled 必须显式提交。
type TraitUpdateInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Category        string `json:"category"`
	Enabled         *bool  `json:"enabled"`
	Reason          string `json:"reason"`
}

type traitUpdateCommand struct {
	ID string `json:"id"`
	TraitUpdateInput
}

// CreateTrait 沿用当前审核运营授权及命令幂等，不允许同名新增覆盖分类或审核状态。
func (s *Service) CreateTrait(ctx context.Context, token, key, rid string, input TraitCreateInput) (out Receipt, err error) {
	defer s.done("trait_create", rid, time.Now(), &err)
	names, err := normalizeTraits([]string{input.Name})
	if err != nil || len(names) != 1 || !validTraitCategory(input.Category) || !validTraitReason(input.Reason) {
		return out, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, "", "trait_create", "POST", "/api/v1/admin/matchmaking/traits", input, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		if s.cfg.ScanTrait == nil {
			return "", command.ErrUnavailable
		}
		pending, evidence, err := s.cfg.ScanTrait(ctx, tx.DB(), names[0])
		if err != nil {
			return "", err
		}
		row := entity.MatchmakingTrait{
			ID: uuid.NewString(), Name: names[0], Category: input.Category,
			Enabled: true, Status: "approved", Version: 1, Evidence: evidence,
			Reason: strings.TrimSpace(input.Reason), CreatedAt: tx.Now(),
		}
		if pending {
			row.Status = "pending"
		}
		result := tx.DB().Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}).Create(&row)
		if result.Error != nil {
			return "", result.Error
		}
		if result.RowsAffected != 1 {
			return "", command.ErrConflict
		}
		return row.ID, writeTraitMaintenanceAudit(tx, nil, row, input.Reason)
	}, nil)
}

// UpdateTrait 与审核保持账号在前、词条在后的锁序，分类改变会使旧报名预览失效。
func (s *Service) UpdateTrait(ctx context.Context, token, key, rid, id string, input TraitUpdateInput) (out Receipt, err error) {
	defer s.done("trait_update", rid, time.Now(), &err)
	if !validID(id) || input.ExpectedVersion < 1 || !validTraitCategory(input.Category) || input.Enabled == nil || !validTraitReason(input.Reason) {
		return out, command.ErrInvalid
	}
	request := traitUpdateCommand{ID: id, TraitUpdateInput: input}
	return s.write(ctx, token, key, rid, "", "trait_update", "PUT", "/api/v1/admin/matchmaking/traits/"+id, request, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		var row entity.MatchmakingTrait
		if err := tx.DB().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; err != nil {
			return "", err
		}
		if row.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		before := row
		if err := tx.DB().Model(&row).Updates(map[string]any{
			"category": input.Category, "enabled": *input.Enabled, "version": row.Version + 1,
		}).Error; err != nil {
			return "", err
		}
		accounts := tx.DB().Model(&entity.MatchmakingTraitSelection{}).Select("account_id").Where("trait_id=?", id)
		if err := tx.DB().Model(&entity.Account{}).Where("id IN (?)", accounts).UpdateColumn("version", gorm.Expr("version+1")).Error; err != nil {
			return "", err
		}
		after := before
		after.Category, after.Enabled, after.Version = input.Category, *input.Enabled, before.Version+1
		return id, writeTraitMaintenanceAudit(tx, &before, after, input.Reason)
	}, nil)
}

func writeTraitMaintenanceAudit(tx *command.Tx, before *entity.MatchmakingTrait, after entity.MatchmakingTrait, reason string) error {
	row := entity.MatchmakingTraitMaintenanceAudit{ID: uuid.NewString(), ActorID: tx.Actor().ID, TraitID: after.ID,
		Action: "create", NewCategory: after.Category, NewEnabled: after.Enabled, Reason: strings.TrimSpace(reason), Version: after.Version, At: tx.Now()}
	if before != nil {
		row.OldCategory, row.OldEnabled = &before.Category, &before.Enabled
		switch {
		case before.Category != after.Category && before.Enabled != after.Enabled:
			row.Action = "category_enabled"
		case before.Category != after.Category:
			row.Action = "category"
		case before.Enabled != after.Enabled:
			row.Action = "enabled"
		default:
			row.Action = "confirm"
		}
	}
	return tx.DB().Create(&row).Error
}
