package config

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// Whispers 来自外部只读私有文件，API和worker使用同一显式业务约束，凭据不进入日志。
type Whispers struct {
	Enabled                                                                                     bool
	DatabasePolling                                                                             bool
	APIDSN, ReviewDSN, CrisisDSN, PrivacyDSN, EvidenceDeletionDSN, WorkerDSN, EvidencePurgerDSN string
	RecoveryWriterDSN, RecoveryReaderDSN                                                        string
	GatewayURL, GatewayTokenFile, GatewayCAFile, ProviderVersion                                string
	KeyRefs, RecoveryKeyRefs                                                                    []string
	ReceiptKey, CursorKey, RateKey, GovernanceKey, PrivacyKey, EvidenceKey, SafetyKey           []byte
	CatalogFile, ReviewMQFile, UnlockMQFile                                                     string
	Command                                                                                     command.Config
	Worker                                                                                      struct {
		Consumer        durable.ConsumerConfig
		AnalysisTimeout time.Duration
	}
	Interval, OperationTimeout, ProviderTimeout time.Duration
}

// WhisperResources 保存版本化公共资料及独立关怀内容，不包含主体或密钥。
type WhisperResources struct {
	Version string
	Catalog whisper.CatalogSnapshot
	Care    *moderation.CareResources
}

func readWhisperJSON(ctx context.Context, path string, out any) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	file, e := os.Open(path)
	if e != nil {
		return command.ErrUnavailable
	}
	defer file.Close()
	raw, e := io.ReadAll(io.LimitReader(file, 1<<20+1))
	defer clear(raw)
	if e != nil || len(raw) > 1<<20 {
		return command.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		return command.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return command.ErrInvalid
	}
	return ctx.Err()
}

// LoadWhispers 在开连接前拒绝无效配置、密钥复用及生产启用，绝不生成凭据或运行迁移。
func LoadWhispers(ctx context.Context, path, environment string) (cfg Whispers, err error) {
	if environment != "integration" && environment != "production" {
		return cfg, command.ErrInvalid
	}
	if err = readWhisperJSON(ctx, path, &cfg); err != nil {
		return
	}
	if environment == "production" && cfg.Enabled {
		return cfg, command.ErrForbidden
	}
	for _, v := range []string{cfg.RecoveryWriterDSN, cfg.RecoveryReaderDSN, cfg.CatalogFile, cfg.ProviderVersion, cfg.GatewayTokenFile} {
		if strings.TrimSpace(v) == "" {
			return cfg, command.ErrInvalid
		}
	}
	keys := [][]byte{cfg.ReceiptKey, cfg.CursorKey, cfg.RateKey, cfg.GovernanceKey, cfg.PrivacyKey, cfg.EvidenceKey, cfg.SafetyKey}
	for i, key := range keys {
		if len(key) != 32 {
			return cfg, command.ErrInvalid
		}
		for _, prior := range keys[:i] {
			if bytes.Equal(key, prior) {
				return cfg, command.ErrInvalid
			}
		}
	}
	if len(cfg.KeyRefs) == 0 || len(cfg.RecoveryKeyRefs) == 0 {
		return cfg, command.ErrInvalid
	}
	for _, key := range cfg.KeyRefs {
		if strings.TrimSpace(key) == "" {
			return cfg, command.ErrInvalid
		}
		for _, other := range cfg.RecoveryKeyRefs {
			if key == other {
				return cfg, command.ErrInvalid
			}
		}
	}
	if cfg.Command.Timeout <= 0 || cfg.Command.LockTimeout < time.Millisecond || cfg.Command.LockTimeout > cfg.Command.Timeout || cfg.Command.ReceiptTTL <= 0 || cfg.Command.MaxAttempts < 1 || cfg.Command.MaxAttempts > 10 || cfg.Interval < time.Millisecond || cfg.OperationTimeout <= 0 || cfg.ProviderTimeout <= 0 || cfg.ProviderTimeout > 30*time.Second {
		return cfg, command.ErrInvalid
	}
	if cfg.Worker.AnalysisTimeout <= 0 || cfg.Worker.Consumer.Timeout <= 0 || cfg.Worker.Consumer.LockTimeout < time.Millisecond || cfg.Worker.Consumer.RetryDelay < time.Millisecond || cfg.Worker.Consumer.MaxAttempts < 1 || cfg.OperationTimeout < 2*cfg.Worker.Consumer.Timeout+cfg.Worker.AnalysisTimeout {
		return cfg, command.ErrInvalid
	}
	return cfg, nil
}

// LoadWhisperResources 读取当前版本的公共资源文件，缺失或无版本时失败关闭。
func LoadWhisperResources(ctx context.Context, path string) (out WhisperResources, err error) {
	if err = readWhisperJSON(ctx, path, &out); err != nil {
		return
	}
	if strings.TrimSpace(out.Version) == "" {
		return out, command.ErrUnavailable
	}
	return
}
