package local

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Scanner 仅由隔离开发宿主装配，模拟结论不代表真实内容审核。
type Scanner struct{ Logger *zap.Logger }

// Scan 仅返回明确标记的开发模拟结论；生产宿主禁止装配。
func (s Scanner) Scan(ctx context.Context, _ []byte, _ string) (approved bool, err error) {
	defer completed(s.Logger, "development_scan", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return false, err
	}
	s.Logger.Info("开发图片扫描模拟通过", zap.Bool("simulated", true), zap.String("result", "approved"))
	return true, nil
}
