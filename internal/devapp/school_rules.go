package devapp

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// developmentSchoolRules 仅由限制为 integration 的开发宿主注入，不能作为生产学校权威校验。
// 仅模拟外部规则通过；输入合法性、学号唯一占用、资产归属和人工审核仍由领域服务执行。
type developmentSchoolRules struct{ logger *zap.Logger }

func (s developmentSchoolRules) Validate(ctx context.Context, _ string, _ string) (err error) {
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		// 完成日志标明模拟，禁止记录姓名、学号或供应商输入。
		s.logger.Info("开发学校规则模拟校验完成", zap.String("operation", "development_school_rules"), zap.Bool("simulated", true), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	return ctx.Err()
}
