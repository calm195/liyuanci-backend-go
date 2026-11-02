package app

import (
	"time"

	"go.uber.org/zap"
)

func finishDevelopment(log *zap.Logger, operation string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	log.Info("开发资源操作完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
