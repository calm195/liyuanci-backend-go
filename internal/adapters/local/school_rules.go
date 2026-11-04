package local

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// SchoolRules 按无学校权威服务的运行策略直接通过。
// 仅跳过外部规则；输入合法性、学号唯一占用、资产归属和人工审核仍由领域服务执行。
type SchoolRules struct{ Logger *zap.Logger }

// Validate 按无服务策略通过外部学校规则，取消时失败；主体归属仍由领域校验。
func (s SchoolRules) Validate(ctx context.Context, _ string, _ string) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		// 完成日志标明无外部服务，禁止记录姓名、学号或供应商输入。
		s.Logger.Info(
			"学校校验按无服务策略完成",
			zap.String("operation", "school_rules_bypass"),
			zap.Bool("external_service", false),
			zap.String("result", result),
			zap.Duration("duration", time.Since(start)),
		)
	}()
	return ctx.Err()
}
