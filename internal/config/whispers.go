package config

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// Whispers 来自外部只读私有文件，API和worker使用同一显式业务约束，凭据不进入日志。
type Whispers struct {
	// LocalTest 仅用于integration业务联调，使用单一业务连接和本机测试适配。
	LocalTest                bool
	DatabaseDSN              string `json:"-"`
	JournalFile, Contact     string
	WrapKey, RecoveryWrapKey []byte `json:"-"`
	Enabled                  bool
	// 数据库预算只从顶层database注入，业务块不能重复设置。
	DatabaseMaxOpen, DatabaseMaxIdle                                                  int           `json:"-"`
	DatabaseMaxLifetime                                                               time.Duration `json:"-"`
	GatewayURL, GatewayToken, GatewayCA, ProviderVersion                              string
	KeyRefs, RecoveryKeyRefs                                                          []string
	ReceiptKey, CursorKey, RateKey, GovernanceKey, PrivacyKey, EvidenceKey, SafetyKey []byte `json:"-"`
	CatalogFile                                                                       string
	Command                                                                           Command
	Worker                                                                            struct {
		Consumer        Consumer
		AnalysisTimeout time.Duration
	}
	Interval, OperationTimeout, ProviderTimeout time.Duration
}

// WhisperResources 保存版本化公共资料及独立关怀内容，不包含主体或密钥。
type WhisperResources struct {
	Version string
	Catalog Catalog
	Care    *CareResources
}

// readWhisperYAML 不读取其他凭据文件，所有秘密值来自当前私有YAML。
func readWhisperYAML(ctx context.Context, path string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := yamlconfig.Read(path, out); err != nil {
		return ErrInvalid
	}
	return ctx.Err()
}

// LoadWhispers 在开连接前拒绝无效配置、密钥复用及生产启用，绝不生成凭据或运行迁移。
func LoadWhispers(ctx context.Context, path, environment string) (cfg Whispers, err error) {
	if err = ctx.Err(); err != nil {
		return cfg, err
	}
	app, e := Load(path)
	if e != nil {
		return cfg, e
	}
	cfg = app.Whispers
	return validateWhispers(cfg, environment, false)
}

// validateWhispers 只有完整业务宿主可申请生产启用；独立树洞入口继续保留原门禁。
func validateWhispers(cfg Whispers, environment string, business bool) (Whispers, error) {
	if cfg.DatabaseMaxOpen == 0 && cfg.DatabaseMaxIdle != 0 || cfg.DatabaseMaxOpen < 0 || cfg.DatabaseMaxOpen > 100 ||
		cfg.DatabaseMaxIdle < 0 ||
		cfg.DatabaseMaxIdle > cfg.DatabaseMaxOpen {
		return cfg, ErrInvalid
	}
	if (environment != "integration" && environment != "development") && environment != "production" {
		return cfg, ErrInvalid
	}
	if environment == "production" && (cfg.LocalTest || cfg.Enabled && !business) {
		return cfg, ErrForbidden
	}
	if cfg.DatabaseDSN == "" || !filepath.IsAbs(cfg.JournalFile) {
		return cfg, ErrInvalid
	}
	if cfg.LocalTest {
		if strings.TrimSpace(cfg.DatabaseDSN) == "" || !filepath.IsAbs(cfg.JournalFile) || len(cfg.WrapKey) != 32 ||
			len(cfg.RecoveryWrapKey) != 32 {
			return cfg, ErrInvalid
		}
	}
	for _, v := range []string{cfg.CatalogFile, cfg.ProviderVersion, cfg.GatewayToken} {
		if !cfg.LocalTest && strings.TrimSpace(v) == "" {
			return cfg, ErrInvalid
		}
	}
	keys := [][]byte{
		cfg.ReceiptKey,
		cfg.CursorKey,
		cfg.RateKey,
		cfg.GovernanceKey,
		cfg.PrivacyKey,
		cfg.EvidenceKey,
		cfg.SafetyKey,
	}
	for i, key := range keys {
		if len(key) != 32 {
			return cfg, ErrInvalid
		}
		for _, prior := range keys[:i] {
			if bytes.Equal(key, prior) {
				return cfg, ErrInvalid
			}
		}
	}
	if !cfg.LocalTest && (len(cfg.KeyRefs) == 0 || len(cfg.RecoveryKeyRefs) == 0) {
		return cfg, ErrInvalid
	}
	for _, key := range cfg.KeyRefs {
		if strings.TrimSpace(key) == "" {
			return cfg, ErrInvalid
		}
		for _, other := range cfg.RecoveryKeyRefs {
			if key == other {
				return cfg, ErrInvalid
			}
		}
	}
	if cfg.Command.Timeout <= 0 || cfg.Command.LockTimeout < time.Millisecond || cfg.Command.LockTimeout > cfg.Command.Timeout ||
		cfg.Command.ReceiptTTL <= 0 ||
		cfg.Command.MaxAttempts < 1 ||
		cfg.Command.MaxAttempts > 10 ||
		cfg.Interval < time.Millisecond ||
		cfg.OperationTimeout <= 0 ||
		cfg.ProviderTimeout <= 0 ||
		cfg.ProviderTimeout > 30*time.Second {
		return cfg, ErrInvalid
	}
	if cfg.Worker.AnalysisTimeout <= 0 || cfg.Worker.Consumer.Timeout <= 0 || cfg.Worker.Consumer.LockTimeout < time.Millisecond ||
		cfg.Worker.Consumer.RetryDelay < time.Millisecond ||
		cfg.Worker.Consumer.MaxAttempts < 1 ||
		cfg.OperationTimeout < 2*cfg.Worker.Consumer.Timeout+cfg.Worker.AnalysisTimeout {
		return cfg, ErrInvalid
	}
	return cfg, nil
}

// LoadWhisperResources 读取当前版本的公共资源文件，缺失或无版本时失败关闭。
func LoadWhisperResources(ctx context.Context, path string) (out WhisperResources, err error) {
	if err = readWhisperYAML(ctx, path, &out); err != nil {
		return
	}
	if strings.TrimSpace(out.Version) == "" {
		return out, ErrUnavailable
	}
	return
}
