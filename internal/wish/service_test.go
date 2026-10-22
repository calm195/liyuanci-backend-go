package wish

import (
	"bytes"
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// testKeys 仅限合成测试；不实现生产密钥供应商。
type testKeys struct {
	failed    bool
	panicWrap bool
}

func (k *testKeys) Wrap(_ context.Context, key []byte) (string, []byte, error) {
	if k.panicWrap {
		panic("SYNTHETIC_SECRET")
	}
	if k.failed {
		return "", nil, command.ErrUnavailable
	}
	return "synthetic-only", append([]byte(nil), key...), nil
}
func (k *testKeys) Unwrap(_ context.Context, ref string, key []byte) ([]byte, error) {
	if k.failed || ref != "synthetic-only" {
		return nil, command.ErrUnavailable
	}
	return append([]byte(nil), key...), nil
}
func serviceConfig() ServiceConfig {
	return ServiceConfig{Command: command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}, ReceiptKey: bytes.Repeat([]byte{71}, 32)}
}

// TestServiceConfiguration 追溯 T014/REQ-018：无隐式数据库、日志器、密钥或回执配置。
func TestServiceConfiguration(t *testing.T) {
	logger := zap.NewNop()
	codec, _ := securedata.New(&testKeys{}, logger)
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 user=unused dbname=unused", PreferSimpleProtocol: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := db.DB()
	t.Cleanup(func() { pool.Close() })
	for _, tc := range []struct {
		db    *gorm.DB
		log   *zap.Logger
		codec *securedata.Codec
		cfg   ServiceConfig
	}{
		{nil, logger, codec, serviceConfig()}, {db, nil, codec, serviceConfig()}, {db, logger, nil, serviceConfig()}, {db, logger, codec, ServiceConfig{}},
	} {
		if got, err := NewService(tc.db, tc.log, tc.codec, nil, tc.cfg); err == nil || got != nil {
			t.Fatal("错误配置被接受")
		}
	}
	cfg := serviceConfig()
	s, err := NewService(db, logger, codec, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.requestDigest(draftInput())
	cfg.ReceiptKey[0]++
	after, _ := s.requestDigest(draftInput())
	if before != after || len(before) != 64 {
		t.Fatal("密钥未独立复制")
	}
	changed := draftInput()
	changed.Body += "改变"
	different, _ := s.requestDigest(changed)
	if before == different {
		t.Fatal("不同正文摘要相同")
	}
}

// TestCreateRequestDigestRejectsInvalidUTF8 防止JSON替换字符使非法请求与有效回执碰撞。
func TestCreateRequestDigestRejectsInvalidUTF8(t *testing.T) {
	s := &Service{receiptKey: serviceConfig().ReceiptKey}
	bad := string([]byte{0xff})
	for _, input := range []ContentInput{
		{Category: bad}, {Body: bad}, {Campus: bad}, {RewardMethod: &bad},
		{IdentityTags: &IdentityTags{Campus: &bad}},
		{IdentityTags: &IdentityTags{Grade: &bad}},
		{IdentityTags: &IdentityTags{Gender: &bad}},
	} {
		if digest, err := s.requestDigest(input); err != command.ErrInvalid || digest != "" {
			t.Fatal("非法UTF-8进入回执摘要")
		}
	}
}
