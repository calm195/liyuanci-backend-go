package whisper

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// 稳定错误由边界映射为契约码，不能暴露外部依赖原文。
var (
	// ErrCapsuleLimit 表示本人未终结胶囊已达权威活动额度。
	ErrCapsuleLimit         = command.ErrCapsuleLimit
	ErrModuleUnavailable    = errors.New("模块未启用")
	ErrVerificationRequired = command.ErrVerificationRequired
	ErrModeUnavailable      = command.ErrModeUnavailable
	ErrDictionaryChanged    = command.ErrDictionaryChanged
	ErrNicknameNotReady     = command.ErrNicknameNotReady
)

// RateError 仅携带可公开重试秒数。
type RateError struct{ Seconds int }

// Error 返回稳定中文说明，不含主体或依赖信息。
func (e *RateError) Error() string { return "操作频率过高" }

// ValidKey 接受 UUID 幂等键的规范连字符形态，大小写保持原始回执域。
func ValidKey(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, e := uuid.Parse(value)
	return e == nil
}

// ValidID 只接受规范小写 UUID，避免同一资源出现多个回执域。
func ValidID(value string) bool { id, e := uuid.Parse(value); return e == nil && id.String() == value }

func validText(value string, min, max int) bool {
	n := utf8.RuneCountInString(value)
	return utf8.ValidString(value) && n >= min && n <= max && strings.TrimSpace(value) != "" && !strings.ContainsRune(value, 0)
}

func tagsValid(tags *Tags, dict Dictionary) bool {
	if tags == nil || !slices.Contains([]string{"wushan", "university_town", "international"}, tags.Campus) || !slices.Contains([]string{"male", "female", "undisclosed"}, tags.Gender) {
		return false
	}
	return slices.ContainsFunc(dict.Items, func(i Item) bool { return i.Code == tags.Grade })
}

func identityShape(i IdentityInput) bool {
	switch i.Mode {
	case "anonymous":
		return i.Tags == nil && i.DictionaryVersion == nil && i.NicknameVersion == nil
	case "semi":
		return i.Tags != nil && i.DictionaryVersion != nil && *i.DictionaryVersion > 0 && i.NicknameVersion == nil
	case "named":
		return i.Tags == nil && i.DictionaryVersion == nil && i.NicknameVersion != nil && *i.NicknameVersion > 0
	}
	return false
}

func randomAlias() (string, error) {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		return "", command.ErrUnavailable
	}
	return "树洞旅人" + hex.EncodeToString(b), nil
}

func normalizeCatalog(c CatalogSnapshot) (CatalogSnapshot, error) {
	if c.Grades.Version == nil || len(c.Grades.Items) == 0 {
		c.Grades = Dictionary{Items: []Item{}}
	} else {
		if *c.Grades.Version < 1 {
			return c, command.ErrUnavailable
		}
		codes := map[string]bool{}
		for _, i := range c.Grades.Items {
			if codes[i.Code] || !validText(i.Code, 1, 32) || !validText(i.Label, 1, 200) {
				return c, command.ErrUnavailable
			}
			codes[i.Code] = true
		}
	}
	if c.Reports == nil {
		c.Reports = []Item{}
	}
	if c.Help.Status != "ready" {
		c.Help = HelpDirectory{Status: "unavailable", Items: []HelpItem{}}
		return c, nil
	}
	if c.Help.Version == nil || *c.Help.Version < 1 || c.Help.VerifiedAt == nil || len(c.Help.Items) == 0 {
		return c, command.ErrUnavailable
	}
	t, e := time.Parse(time.RFC3339, *c.Help.VerifiedAt)
	if e != nil || timestamp(t) != *c.Help.VerifiedAt {
		return c, command.ErrUnavailable
	}
	for _, i := range c.Help.Items {
		u, e := url.Parse(i.SourceURL)
		if !slices.Contains([]string{"school_appointment", "hotline"}, i.Kind) || !validText(i.Label, 1, 500) || !validText(i.Contact, 1, 500) || e != nil || u.Scheme != "https" || u.Host == "" {
			return c, command.ErrUnavailable
		}
	}
	return c, nil
}
