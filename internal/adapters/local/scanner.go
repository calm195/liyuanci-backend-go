package local

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// Scanner 按无外部图片扫描服务的运行策略直接通过。
type Scanner struct{ Logger *zap.Logger }

// Scan 直接通过图片安全扫描；解码、大小及归属校验仍由领域负责。
func (s Scanner) Scan(ctx context.Context, _ []byte, _ string) (approved bool, err error) {
	defer completed(s.Logger, "image_scan_bypass", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return false, err
	}
	s.Logger.Info("图片安全扫描按无服务策略通过", zap.Bool("external_service", false), zap.String("result", "approved"))
	return true, nil
}
