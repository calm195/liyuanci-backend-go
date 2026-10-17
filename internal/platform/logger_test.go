package platform

import (
	"testing"

	"go.uber.org/zap"
)

// T004/REQ-018、020：保留模板配置与安全日志质量回归。
func TestNewLoggerLevel(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			log, err := NewLogger(level)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = log.Sync() }()
			want, err := zap.ParseAtomicLevel(level)
			if err != nil {
				t.Fatal(err)
			}
			if !log.Core().Enabled(want.Level()) {
				t.Fatal("configured level is disabled")
			}
			if log.Core().Enabled(want.Level() - 1) {
				t.Fatal("lower level must be disabled")
			}
		})
	}
}

// T004/REQ-018、020：保留模板配置与安全日志质量回归。
func TestNewLoggerRejectsInvalidLevel(t *testing.T) {
	if _, err := NewLogger("invalid"); err == nil {
		t.Fatal("expected invalid level error")
	}
}
