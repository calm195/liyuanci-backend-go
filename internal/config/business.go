package config

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// Business 是完整业务宿主的显式配置；不接受开发适配，按进程校验所需身份与凭据。
// Runtime 复用树洞的密钥、供应商、恢复日志和角色边界，同一用途的历史引用必须保留。
type Business struct {
	Runtime                                                            Whispers
	StudentDSN, AdminDSN                                               string
	SessionKey, CardKey, VerificationKey, WishKey, AdminKey, CrisisKey []byte
	LookupKeyRefs                                                      []string
	WeChatAppID, WeChatSecretFile, AdminPasswordFile                   string
	CardMQFile, ContentMQFile                                          string
	SessionTTL, ExchangeTTL, AdminSessionTTL, LoginWindow              time.Duration
	LoginLimit, MaxTextBytes                                           int
	Card                                                               struct {
		MaxBytes, MaxPixels      int64
		DecodeSlots, MaxAttempts int
		Lease, RetryDelay        time.Duration
	}
}

// LoadBusiness 严格解析私有配置，拒绝未知字段、开发模式和缺失依赖；不连接网络或改写文件。
func LoadBusiness(ctx context.Context, path, environment, process string) (cfg Business, err error) {
	if err = readWhisperJSON(ctx, path, &cfg); err != nil {
		return
	}
	err = ValidateBusiness(cfg, environment, process)
	return
}

// ValidateBusiness 按用途校验配置，生产不得借用集成轮询或本地密钥、对象、学校规则。
func ValidateBusiness(c Business, environment, process string) error {
	if !slices.Contains([]string{"api", "card", "content", "whispers", "account", "evidence", "bootstrap"}, process) || c.Runtime.LocalTest || c.Runtime.DatabaseDSN != "" || c.Runtime.JournalFile != "" || len(c.Runtime.WrapKey) > 0 || len(c.Runtime.RecoveryWrapKey) > 0 || c.Runtime.Contact != "" {
		return command.ErrInvalid
	}
	if _, e := validateWhispers(c.Runtime, environment, true); e != nil {
		return e
	}
	u, e := url.Parse(c.Runtime.GatewayURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return command.ErrInvalid
	}
	allRefs := []string{}
	for _, group := range [][]string{c.Runtime.KeyRefs, c.Runtime.RecoveryKeyRefs, c.LookupKeyRefs} {
		for _, ref := range group {
			if strings.TrimSpace(ref) == "" || slices.Contains(allRefs, ref) {
				return command.ErrInvalid
			}
			allRefs = append(allRefs, ref)
		}
	}
	keys := [][]byte{c.Runtime.ReceiptKey, c.Runtime.CursorKey, c.Runtime.RateKey, c.Runtime.GovernanceKey, c.Runtime.PrivacyKey, c.Runtime.EvidenceKey, c.Runtime.SafetyKey}
	for _, key := range [][]byte{c.SessionKey, c.CardKey, c.VerificationKey, c.WishKey, c.AdminKey, c.CrisisKey} {
		if len(key) == 0 {
			continue
		}
		if len(key) != 32 {
			return command.ErrInvalid
		}
		for _, prior := range keys {
			if bytes.Equal(key, prior) {
				return command.ErrInvalid
			}
		}
		keys = append(keys, key)
	}
	if process == "api" || process == "bootstrap" {
		if c.StudentDSN == "" || c.AdminDSN == "" || len(c.LookupKeyRefs) == 0 || len(c.AdminKey) != 32 || c.AdminSessionTTL <= 0 || c.LoginWindow <= 0 || c.LoginLimit < 1 {
			return command.ErrInvalid
		}
		if _, e := BusinessSecret(c.AdminPasswordFile, 12); e != nil {
			return e
		}
	}
	if process == "api" {
		for _, key := range [][]byte{c.SessionKey, c.CardKey, c.VerificationKey, c.WishKey, c.CrisisKey} {
			if len(key) != 32 {
				return command.ErrInvalid
			}
		}
		for _, dsn := range []string{c.Runtime.APIDSN, c.Runtime.ReviewDSN, c.Runtime.CrisisDSN, c.Runtime.PrivacyDSN, c.Runtime.EvidenceDeletionDSN} {
			if dsn == "" {
				return command.ErrInvalid
			}
		}
		if c.WeChatAppID == "" || c.SessionTTL <= 0 || c.ExchangeTTL <= 0 || c.MaxTextBytes < 1 || c.MaxTextBytes > 1<<20 {
			return command.ErrInvalid
		}
		if _, e := BusinessSecret(c.WeChatSecretFile, 16); e != nil {
			return e
		}
	}
	if process == "api" || process == "card" {
		if len(c.CardKey) != 32 || c.Card.MaxBytes < 1 || c.Card.MaxBytes > 8<<20 || c.Card.MaxPixels < 1 || c.Card.MaxPixels > 32_000_000 || c.Card.DecodeSlots < 1 || c.Card.DecodeSlots > 16 || c.Card.Lease < c.Runtime.Command.Timeout || c.Card.RetryDelay < time.Millisecond || c.Card.MaxAttempts < 1 || c.Card.MaxAttempts > 1000 {
			return command.ErrInvalid
		}
	}
	if process == "card" || process == "content" || process == "whispers" {
		if c.Runtime.WorkerDSN == "" {
			return command.ErrInvalid
		}
	}
	if process == "card" && c.CardMQFile == "" && !c.Runtime.DatabaseTasks() || process == "content" && c.ContentMQFile == "" && !c.Runtime.DatabaseTasks() {
		return command.ErrInvalid
	}
	if c.Runtime.DatabaseTasks() && (c.CardMQFile != "" || c.ContentMQFile != "") {
		return command.ErrInvalid
	}
	if process == "account" && c.Runtime.PrivacyDSN == "" || process == "evidence" && c.Runtime.EvidencePurgerDSN == "" {
		return command.ErrInvalid
	}
	return nil
}

// BusinessSecret 只读取有界单行凭据；错误不含路径或内容，既有凭据永不自动替换。
func BusinessSecret(path string, minimum int) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", command.ErrUnavailable
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	defer clear(b)
	if e != nil || len(b) > 4096 {
		return "", command.ErrInvalid
	}
	s := strings.TrimSpace(string(b))
	if len(s) < minimum || strings.ContainsAny(s, "\r\n\x00") {
		return "", command.ErrInvalid
	}
	return s, nil
}
