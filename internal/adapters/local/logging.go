package local

import (
	"time"

	"go.uber.org/zap"
)

func finishWhisper(log *zap.Logger, op string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	log.Info(
		"树洞运行操作完成",
		zap.String("operation", op),
		zap.String("result", result),
		zap.Duration("duration", time.Since(start)),
	)
}
