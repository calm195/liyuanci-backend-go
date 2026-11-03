package app

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
)

// replayDeletionIntents 先按领域分页重放账本，防止数据库恢复遗漏已确认的删除意图。
func replayDeletionIntents(ctx context.Context, service *privacy.Service, scope string) error {
	after := ""
	for {
		next, err := service.Recover(ctx, after, 100, scope)
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

// drainDeletionTasks 只在显式恢复模式排空；延迟重试或未完成任务不能被当作恢复成功。
func drainDeletionTasks(ctx context.Context, service *privacy.Service, db *gorm.DB, scope string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		processed, err := service.ProcessNext(ctx, scope)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		var count int64
		if err := db.WithContext(ctx).Model(&entity.DeletionJob{}).
			Where("scope=? AND phase<>'completed'", scope).
			Count(&count).Error; err != nil || count != 0 {
			return command.ErrUnavailable
		}
		return nil
	}
}
