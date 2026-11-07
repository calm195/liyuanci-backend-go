package app

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/config"
)

// TestBusinessImagesFailClosed 正式装配不借用本地直接通过扫描器。
func TestBusinessImagesFailClosed(t *testing.T) {
	for _, store := range []objectGateway{nil, unavailableTestObjectGateway{}} {
		approved, err := imageScanner(store).Scan(context.Background(), []byte("image"), "image/png")
		if approved || err == nil {
			t.Fatal("没有CI不能通过图片")
		}
	}
}

// TestDevelopmentMediaUsesTencent 开发API与worker使用真实适配，只有integration环境保留本地夹具。
func TestDevelopmentMediaUsesTencent(t *testing.T) {
	cfg := DevelopmentSettings{}
	objects, scanner, closeMedia, err := developmentMedia("development", cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer closeMedia()
	if _, err := objects.PutOnce(context.Background(), "key", []byte("密文")); err == nil {
		t.Fatal("开发缺配置不能回退本地存储")
	}
	if approved, err := scanner.Scan(context.Background(), []byte("图片"), "image/png"); approved || err == nil {
		t.Fatal("开发缺配置不能模拟通过")
	}
	cfg.Tencent = config.Tencent{Enabled: true, Bucket: "development-1234567890", Region: "ap-shanghai", SecretID: "synthetic-id", SecretKey: "synthetic-key", BizType: "synthetic-biz"}
	cfg.ProviderTimeout = time.Second
	objects, scanner, closeMedia, err = developmentMedia("development", cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer closeMedia()
	if _, ok := objects.(*gateway.Tencent); !ok {
		t.Fatal("开发对象必须使用腾讯云")
	}
	if _, ok := scanner.(*gateway.Tencent); !ok {
		t.Fatal("开发扫描必须使用CI")
	}
}

// unavailableTestObjectGateway 验证没有CI能力的对象客户端不能成为图片扫描器。
type unavailableTestObjectGateway struct{ unavailableObjects }

func (unavailableTestObjectGateway) Close() {}

func (unavailableTestObjectGateway) RemoveAvatar(context.Context, string) error {
	return context.Canceled
}
