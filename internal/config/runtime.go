package config

import (
	"bytes"
	"path/filepath"
	"strings"
	"time"
)

// Runtime 保存完整业务与独立树洞共用的运行预算、资源及派生密钥；不提供额外配置入口。
type Runtime struct {
	// Tencent 显式配置腾讯云COS与CI，业务和适配器之间由app转换。
	Tencent Tencent
	// LocalTest 仅用于integration业务联调，使用单一业务连接和本机测试适配。
	LocalTest   bool
	DatabaseDSN string `json:"-"`
	// ObjectDirectory 仅供隔离集成本地对象适配器使用，正式环境仍使用供应商。
	ObjectDirectory          string
	JournalFile              string
	WrapKey, RecoveryWrapKey []byte `json:"-"`
	// Enabled 只控制树洞与胶囊模块，不关闭数据库任务、对象服务或其他业务。
	Enabled bool
	// 数据库预算只从顶层database注入，业务块不能重复设置。
	DatabaseMaxOpen, DatabaseMaxIdle                                                  int           `json:"-"`
	DatabaseMaxLifetime                                                               time.Duration `json:"-"`
	ReceiptKey, CursorKey, RateKey, GovernanceKey, PrivacyKey, EvidenceKey, SafetyKey []byte        `json:"-"`
	ResourcesFile                                                                     string
	Command                                                                           Command
	Tasks                                                                             Tasks
	ProviderTimeout                                                                   time.Duration
}

// ValidateWhispers 校验统一入口已加载的独立树洞配置，不重复读取文件。
func ValidateWhispers(cfg Runtime, environment string) (Runtime, error) {
	return validateRuntime(cfg, environment, false)
}

// validateRuntime 校验共享运行预算；只有完整业务宿主可申请生产启用树洞。
func validateRuntime(cfg Runtime, environment string, business bool) (Runtime, error) {
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
	if len(cfg.WrapKey) != 32 || len(cfg.RecoveryWrapKey) != 32 || bytes.Equal(cfg.WrapKey, cfg.RecoveryWrapKey) {
		return cfg, ErrInvalid
	}
	if !cfg.LocalTest && strings.TrimSpace(cfg.ResourcesFile) == "" {
		return cfg, ErrInvalid
	}
	if err := validateTencent(cfg.Tencent); err != nil {
		return cfg, err
	}
	keys := [][]byte{
		cfg.WrapKey,
		cfg.RecoveryWrapKey,
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
	if cfg.Command.Timeout <= 0 || cfg.Command.LockTimeout < time.Millisecond || cfg.Command.LockTimeout > cfg.Command.Timeout ||
		cfg.Command.ReceiptTTL <= 0 ||
		cfg.Command.MaxAttempts < 1 ||
		cfg.Command.MaxAttempts > 10 ||
		cfg.Tasks.ScanInterval < time.Millisecond ||
		cfg.Tasks.OperationTimeout <= 0 ||
		cfg.ProviderTimeout <= 0 ||
		cfg.ProviderTimeout > 30*time.Second {
		return cfg, ErrInvalid
	}
	if cfg.Tasks.AnalysisTimeout <= 0 || cfg.Tasks.Consumer.Timeout <= 0 || cfg.Tasks.Consumer.LockTimeout < time.Millisecond ||
		cfg.Tasks.Consumer.LockTimeout > cfg.Tasks.Consumer.Timeout ||
		cfg.Tasks.Consumer.RetryDelay < time.Millisecond ||
		cfg.Tasks.Consumer.MaxAttempts < 1 ||
		cfg.Tasks.Consumer.MaxAttempts > 1000 ||
		cfg.Tasks.OperationTimeout < 2*cfg.Tasks.Consumer.Timeout+cfg.Tasks.AnalysisTimeout {
		return cfg, ErrInvalid
	}
	return cfg, nil
}

// Tasks 保存持久任务的扫描、事务、分析和整次处理预算。
// Consumer仅约束短事务，OperationTimeout还必须覆盖分析与结果提交阶段。
type Tasks struct {
	Consumer         Consumer
	AnalysisTimeout  time.Duration
	ScanInterval     time.Duration
	OperationTimeout time.Duration
}
