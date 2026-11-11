package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// TraitSelection 只向本人返回未生效词条及其审核状态。
type TraitSelection struct {
	Purpose string `json:"purpose"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
}

// Trait 是运营词条及按当前账号性别聚合的人数。
type Trait struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Status           string    `json:"status"`
	Version          int64     `json:"version"`
	Heat             int64     `json:"heat"`
	MaleCount        int64     `json:"male_count"`
	FemaleCount      int64     `json:"female_count"`
	UndisclosedCount int64     `json:"undisclosed_count"`
	CreatedAt        time.Time `json:"created_at"`
	Reason           string    `json:"reason"`
}

// TraitDetail 仅对审核运营公开词库依据和审核人。
type TraitDetail struct {
	Trait
	Evidence   json.RawMessage `json:"evidence"`
	ReviewedAt *time.Time      `json:"reviewed_at,omitempty"`
	ReviewedBy *string         `json:"reviewed_by,omitempty"`
}

// TraitPage 是筛选后的词条分页。
type TraitPage struct {
	Items []Trait `json:"items"`
	Total int64   `json:"total"`
}

// TraitWord 是推荐和词云使用的最小投影，不含选用者或审核证据。
type TraitWord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Heat int64  `json:"heat"`
}

// TraitCloud 的总数是匹配词条数，Items 只保留规定数量。
type TraitCloud struct {
	Items []TraitWord `json:"items"`
	Total int64       `json:"total"`
}

// TraitQuery 统一列表与词云的筛选口径，空状态默认已通过。
type TraitQuery struct {
	Purpose string
	Q       string
	Gender  string
	Status  string
	MinHeat *int64
	MaxHeat *int64
	Sort    string
	Page    int
	Limit   int
}

func normalizeTraits(names []string) ([]string, error) {
	if len(names) > 10 {
		return nil, command.ErrInvalid
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(norm.NFKC.String(name))
		if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 20 {
			return nil, command.ErrInvalid
		}
		for _, ch := range name {
			if unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch) {
				return nil, command.ErrInvalid
			}
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

func readTraitSelections(db *gorm.DB, account string) ([]TraitSelection, error) {
	out := []TraitSelection{}
	err := db.Table("public.matchmaking_traits AS t").Select("t.id,t.name,t.status,t.reason,s.purpose").
		Joins("JOIN public.matchmaking_trait_selections s ON s.trait_id=t.id").
		Where("s.account_id=? AND t.status<>'rejected'", account).Order("s.created_at,t.name,t.id,s.purpose").Scan(&out).Error
	return out, err
}

// saveTraitSelections 在资料事务中创建词条和待审案件，唯一索引确保并发同名只建一次。
func (s *Service) saveTraitSelections(ctx context.Context, db *gorm.DB, account string, profile Preferences, now time.Time) error {
	groups := []struct {
		purpose string
		names   []string
	}{{"ideal", profile.IdealTraits}, {"personal", profile.PersonalTraits}}
	all := []string{}
	for i := range groups {
		if groups[i].names == nil {
			continue
		}
		names, err := normalizeTraits(groups[i].names)
		if err != nil {
			return err
		}
		groups[i].names = names
		all = append(all, names...)
	}
	// 两种用途共享唯一词库，必须合并后排序；分别排序仍会在交叉新建时死锁。
	slices.Sort(all)
	names := slices.Compact(all)
	approvedIDs := map[string]string{}
	var err error
	for _, name := range names {
		var row entity.MatchmakingTrait
		err = db.Where("name=?", name).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if s.cfg.ScanTrait == nil {
				return command.ErrUnavailable
			}
			pending, evidence, e := s.cfg.ScanTrait(ctx, db, name)
			if e != nil {
				return e
			}
			row = entity.MatchmakingTrait{ID: uuid.NewString(), Name: name, Status: "approved", Version: 1, Evidence: evidence, CreatedAt: now}
			if pending {
				row.Status = "pending"
			}
			if e = db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "name"}}, DoNothing: true}).Create(&row).Error; e != nil {
				return e
			}
			// 并发命中唯一键后清空未插入的 UUID，避免 GORM 将它隐式追加到查询条件。
			row = entity.MatchmakingTrait{}
			if e = db.Where("name=?", name).Take(&row).Error; e != nil {
				return e
			}
		} else if err != nil {
			return err
		}
		// 账号锁在先、词条锁在后，与审核命令保持相同锁序。
		if e := db.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=?", row.ID).Take(&row).Error; e != nil {
			return e
		}
		// 已驳回词条不能被旧草稿或重试重新关联到资料。
		if row.Status == "rejected" {
			continue
		}
		approvedIDs[name] = row.ID
	}
	for _, group := range groups {
		if group.names == nil {
			continue
		}
		ids := []string{}
		for _, name := range group.names {
			if id := approvedIDs[name]; id != "" {
				ids = append(ids, id)
			}
		}
		remove := db.Where("account_id=? AND purpose=?", account, group.purpose)
		if len(ids) > 0 {
			remove = remove.Where("trait_id NOT IN ?", ids)
		}
		if err = remove.Delete(&entity.MatchmakingTraitSelection{}).Error; err != nil {
			return err
		}
		for _, id := range ids {
			row := entity.MatchmakingTraitSelection{AccountID: account, Purpose: group.purpose, TraitID: id, CreatedAt: now}
			if err = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

type traitReviewCommand struct {
	ID string `json:"id"`
	ReviewInput
}

// ReviewTrait 仅处理当前版本的待审词条，终态不可重复翻转。
func (s *Service) ReviewTrait(ctx context.Context, token, key, rid, id string, input ReviewInput) (out Receipt, err error) {
	defer s.done("trait_review", rid, time.Now(), &err)
	if !validID(id) || input.ExpectedVersion < 1 || !slices.Contains([]string{"approved", "rejected"}, input.Decision) || !optionalText(input.Reason, 500) || input.Decision == "rejected" && strings.TrimSpace(input.Reason) == "" {
		return out, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, "", "trait_review", "POST", "/api/v1/admin/matchmaking/traits/"+id+"/review", traitReviewCommand{ID: id, ReviewInput: input}, func(tx *command.Tx, _ entity.MatchmakingActivity) (string, error) {
		var row entity.MatchmakingTrait
		if e := tx.DB().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).Take(&row).Error; e != nil {
			return "", e
		}
		if row.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if row.Status != "pending" {
			return "", command.ErrConflict
		}
		e := tx.DB().Model(&row).Updates(map[string]any{"status": input.Decision, "reason": strings.TrimSpace(input.Reason), "version": row.Version + 1, "reviewed_at": tx.Now(), "reviewed_by": tx.Actor().ID}).Error
		if e != nil {
			return "", e
		}
		// 审核改变报名预览，推进所选账号版本，使旧预览必须重新确认。
		accounts := tx.DB().Model(&entity.MatchmakingTraitSelection{}).Select("account_id").Where("trait_id=?", id)
		e = tx.DB().Model(&entity.Account{}).Where("id IN (?)", accounts).UpdateColumn("version", gorm.Expr("version+1")).Error
		if e != nil {
			return "", e
		}
		if input.Decision == "rejected" {
			// 保留词条案件和拒绝依据，仅移除选用关系；先更新版本再删除，防止漏掉主体。
			e = tx.DB().Where("trait_id=?", id).Delete(&entity.MatchmakingTraitSelection{}).Error
		}
		return id, e
	}, nil)
}
