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

// checkDeletionRecovery 在读取或外发开启前扫描包含已确认项的独立日志，任何恢复目标仍存在则拒绝启动。
// 由专用隐私进程先恢复屏障并完成重删；普通删除中断后的冷启动也采用同一失败关闭边界。
func checkDeletionRecovery(ctx context.Context, db *gorm.DB, journal recoveryJournal, scope string) error {
	after := ""
	for {
		intents, e := journal.AllIntents(ctx, after, 100)
		if e != nil {
			return e
		}
		for _, in := range intents {
			after = in.ID
			if in.Scope != scope {
				continue
			}
			var n int64
			model := any(&entity.Account{})
			if scope == "evidence" {
				model = &entity.EvidenceCase{}
			}
			if e = db.WithContext(ctx).Model(model).Where("id=?", in.Target).Count(&n).Error; e != nil {
				return command.ErrUnavailable
			}
			if scope == "account" {
				var err error
				n, err = privacy.AccountRemaining(ctx, db, in.Target)
				if err != nil {
					return command.ErrUnavailable
				}
			}
			if n != 0 {
				return command.ErrUnavailable
			}
		}
		if len(intents) < 100 {
			return nil
		}
	}
}
