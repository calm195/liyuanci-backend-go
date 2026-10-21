package adminauth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
)

// T008/REQ-018、019：口令不截断、独立随机盐、畸形摘要失败关闭，期限必须显式注入。
func TestPasswordAndConfiguration(t *testing.T) {
	password := "SYNTHETIC_" + strings.Repeat("鲤", 40)
	first, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || strings.Contains(first, password) || !verifyPassword(first, password) || verifyPassword(first, password+"x") {
		t.Fatal("随机盐或完整口令校验失败")
	}
	for _, bad := range []string{"", "scrypt1$a$b", "scrypt1$" + strings.Repeat("x", 32) + "$" + strings.Repeat("a", 64), "SYNTHETIC_BAD"} {
		if verifyPassword(bad, password) {
			t.Fatal("非法摘要不能通过")
		}
	}
	if _, err = New(nil, nil, zap.NewNop(), Config{}); err == nil {
		t.Fatal("缺少依赖必须拒绝")
	}
	if _, err = New(&gorm.DB{}, nil, zap.NewNop(), Config{SessionTTL: time.Hour}); err == nil {
		t.Fatal("缺少限流或期限不能构造")
	}
	if _, err = NewRedisLimiter(nil, time.Minute, 1); err == nil {
		t.Fatal("缺共享缓存不能回退")
	}
	if !validKey("synthetic") || validKey("") || validKey(strings.Repeat("x", 129)) || validKey("bad\nkey") {
		t.Fatal("幂等键边界错误")
	}
	s := &Service{slots: make(chan struct{}, 1)}
	release, err := s.passwordSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.passwordSlot(context.Background()); err != ErrRateLimited {
		t.Fatal("计算并发必须有界")
	}
	release()
}

type panickingLimiter struct{}

func (panickingLimiter) Allow(context.Context, string) (bool, error) { panic("SYNTHETIC_PASSWORD") }

// T008/REQ-018：注入依赖panic仍失败关闭，不能由defer把异常误记为成功。
func TestLoginRecoveryCompletion(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	service := &Service{logger: zap.New(core), limiter: panickingLimiter{}, config: Config{Command: command.Config{Timeout: time.Second}}}
	result, err := service.Login(context.Background(), "synthetic", "SYNTHETIC_PASSWORD", "source", "recovery")
	if err != command.ErrInternal || result.Token != "" {
		t.Fatal("panic必须转稳定失败")
	}
	entries := logs.FilterMessage("后台身份操作完成").All()
	if len(entries) != 1 || entries[0].ContextMap()["result"] != "failure" || strings.Contains(entries[0].Message, "SYNTHETIC") {
		t.Fatal("异常不能误记成功或泄漏原文")
	}
}
