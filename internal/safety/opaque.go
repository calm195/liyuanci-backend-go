package safety

import (
	"context"
	"github.com/calm195/liyuanci-backend-go/internal/blockpolicy"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"gorm.io/gorm"
	"time"
)

// LegacyBlocks 先校验当前会话，再报告客户端升级，不读取目标映射。
func (s *Service) LegacyBlocks(ctx context.Context, token string) (err error) {
	defer s.done("blocks.compatibility", "", time.Now(), &err)
	return s.read(ctx, token, false, func(db *gorm.DB, _ string, _ time.Time) error { return blockpolicy.Legacy(ctx, db) })
}

// DiscoverBlockEffects 发现双向限制的完整关系集合，调用方须由命令引擎重发现并取锁。
func DiscoverBlockEffects(ctx context.Context, db *gorm.DB, a, b string) (command.Discovery, error) {
	return accountPlan(ctx, db, a, b)
}

// ApplyBlockEffects 在双方账号锁下取消旧待审互动，解除时不会复活。
func ApplyBlockEffects(ctx context.Context, tx *command.Tx, a, b string) error {
	return cancelRelationships(ctx, tx, a, b)
}
