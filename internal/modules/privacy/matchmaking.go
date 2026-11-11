package privacy

import (
	"context"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// matchmakingPlan 与活动写入使用相同的全部参与账号及活动锁，防止清理遗漏并发关系。
func matchmakingPlan(ctx context.Context, db *gorm.DB, id string) (p command.Discovery, err error) {
	db = db.WithContext(ctx)
	var activities []string
	if err = db.Model(&entity.MatchmakingEnrollment{}).Where("account_id=?", id).Pluck("activity_id", &activities).Error; err != nil {
		return
	}
	if err = db.Model(&entity.MatchmakingEnrollment{}).Where("activity_id IN ?", activities).Pluck("account_id", &p.Accounts).Error; err != nil {
		return
	}
	for _, activity := range activities {
		p.Resources = append(p.Resources, command.Resource{Kind: command.MatchmakingActivity, ID: activity})
	}
	return
}

// activateMatchmaking 受理注销即撤销双方访问，幸存者独立报名保留供后续人工处理。
func activateMatchmaking(ctx context.Context, db *gorm.DB, id string) error {
	db = db.WithContext(ctx)
	// 报名仍存在时先撤销所有相关预览，含已应用方案，避免清理后保留姓名与标签副本。
	// 与账号和活动共用锁序；重复恢复不重复推进已经清空的任务版本。
	activities := db.Model(&entity.MatchmakingEnrollment{}).Select("activity_id").Where("account_id=?", id)
	if err := db.Model(&entity.MatchmakingAutoJob{}).
		Where("activity_id IN (?) AND (result_cipher IS NOT NULL OR status <> 'stale')", activities).
		Updates(map[string]any{
			"result_cipher": nil,
			"status":        "stale",
			"error":         "参与成员已注销",
			"version":       gorm.Expr("version+1"),
		}).Error; err != nil {
		return err
	}
	var pairs []string
	if err := db.Model(&entity.MatchmakingMember{}).Where("account_id=?", id).Pluck("pair_id", &pairs).Error; err != nil {
		return err
	}
	// 活动锁已由accountPlan取得；仅实际解除关系时推进聚合版本，重复激活保持幂等。
	var changedActivities []string
	if err := db.Model(&entity.MatchmakingPair{}).Where("id IN ? AND status<>'ended'", pairs).
		Distinct("activity_id").Pluck("activity_id", &changedActivities).Error; err != nil {
		return err
	}
	if err := db.Model(&entity.MatchmakingPair{}).Where("id IN ? AND status<>'ended'", pairs).Updates(map[string]any{"status": "ended", "version": gorm.Expr("version+1"), "ended_at": gorm.Expr("clock_timestamp()"), "reason": "成员资格失效"}).Error; err != nil {
		return err
	}
	if err := db.Model(&entity.MatchmakingMember{}).Where("pair_id IN ? AND active", pairs).Update("active", false).Error; err != nil {
		return err
	}
	if err := db.Model(&entity.MatchmakingActivity{}).Where("id IN ?", changedActivities).
		UpdateColumn("version", gorm.Expr("version+1")).Error; err != nil {
		return err
	}
	return db.Model(&entity.MatchmakingEnrollment{}).Where("account_id=? AND status<>'withdrawn'", id).Updates(map[string]any{"status": "withdrawn", "version": gorm.Expr("version+1"), "reason": "成员资格失效"}).Error
}

func collectMatchmaking(db *gorm.DB, id string, g *cleanGraph) error {
	if err := db.Model(&entity.MatchmakingEnrollment{}).Where("account_id=?", id).Pluck("id", &g.matchmakingEnrollments).Error; err != nil {
		return err
	}
	if err := db.Model(&entity.MatchmakingMember{}).Where("account_id=?", id).Pluck("pair_id", &g.matchmakingPairs).Error; err != nil {
		return err
	}
	if err := db.Model(&entity.MatchmakingTask{}).Where("pair_id IN ?", g.matchmakingPairs).Pluck("id", &g.matchmakingTasks).Error; err != nil {
		return err
	}
	return nil
}

// AccountRemaining 将包分配普通副本和命令关联纳入完成及恢复门禁；任何残留都不能报告已清理。
func AccountRemaining(ctx context.Context, db *gorm.DB, id string) (int64, error) {
	var total int64
	for _, spec := range []struct {
		model any
		where string
		args  []any
	}{
		{&entity.Account{}, "id=?", []any{id}},
		{&entity.MatchmakingEnrollment{}, "account_id=?", []any{id}},
		{&entity.MatchmakingTraitSelection{}, "account_id=?", []any{id}},
		{&entity.MatchmakingMember{}, "account_id=?", []any{id}},
		{&entity.MatchmakingOpen{}, "account_id=?", []any{id}},
		{&entity.MatchmakingAudit{}, "actor_id=? OR subject_ref=?", []any{id, id}},
		{&entity.CommandReceipt{}, "actor_kind='student' AND actor_id=?", []any{id}},
	} {
		var count int64
		if err := db.WithContext(ctx).Model(spec.model).Where(spec.where, spec.args...).Count(&count).Error; err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}
