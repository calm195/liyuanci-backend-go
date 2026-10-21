package wish

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// createDraftRows 只使用命令根事务；延迟外键允许聚合和首个内容版本在提交时一起建立。
// 检测器尚未执行，pending 是稳定的未检测标识，不能当成真实检测版本或通过结论。
func createDraftRows(ctx context.Context, db *gorm.DB, now time.Time, id, registryID, owner string, plan DraftPlan, cipher json.RawMessage) error {
	db = db.WithContext(ctx)
	next := plan.Transition.Next
	if err := db.Create(&entity.Wish{
		ID: id, OwnerID: owner, State: string(next.State), Version: next.Version,
		CurrentContentVersion: next.ContentVersion, DurationDays: int16(next.DurationDays),
		Category: plan.Content.Category, Campus: plan.Content.Campus,
		RestrictionEpoch: next.RestrictionEpoch, CreatedAt: now,
	}).Error; err != nil {
		return err
	}
	if err := db.Create(&entity.WishVersion{
		WishID: id, ContentVersion: next.ContentVersion, BodyCipher: cipher,
		IdentityTags: json.RawMessage(`{}`), RewardMethod: plan.Content.RewardMethod,
		ModerationStatus: "pending", SchoolDetectionVersion: "pending", SchoolDetectionState: "pending",
	}).Error; err != nil {
		return err
	}
	return db.Create(&entity.ContentRegistry{
		ID: registryID, Kind: "wish", EntityID: id, AuthorID: owner, ContentVersion: next.ContentVersion,
		Status: "pending", SchoolDetectionVersion: "pending", SchoolDetectionState: "pending",
	}).Error
}

// resolveCreated 不锁历史结果以免逆序取锁；作者 guard 已串行化同作者命令。
// 只读当前归属和版本，内容删除或转移后拒绝重放，不从回执重建正文或对象。
func resolveCreated(ctx context.Context, db *gorm.DB, id, owner string) (CreatedDraft, error) {
	var row entity.Wish
	err := db.WithContext(ctx).Select("id,version,current_content_version,state").Where("id=? AND owner_id=? AND draft_deleted_at IS NULL", id, owner).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CreatedDraft{}, command.ErrNotFound
	}
	if err != nil {
		return CreatedDraft{}, err
	}
	return CreatedDraft{ID: row.ID, Version: row.Version, ContentVersion: row.CurrentContentVersion, State: State(row.State)}, nil
}
