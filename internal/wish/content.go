// Package wish 实现心愿字段与状态规则，不替代事务中的当前身份和资源授权。
package wish

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// Grades 校验资料方提供的在读年级字典，不从年份或输入文本推导枚举。
type Grades interface {
	Allows(context.Context, string) (bool, error)
}

// IdentityTags 的空指针表示未填写；主动选择保密仍保留 undisclosed。
type IdentityTags struct {
	Campus *string `json:"campus,omitempty"`
	Grade  *string `json:"grade,omitempty"`
	Gender *string `json:"gender,omitempty"`
}

// ContentInput 保留默认字段是否省略的区别，显式零天和空报答方式不能使用默认值兜底。
type ContentInput struct {
	Category     string        `json:"category"`
	Body         string        `json:"body"`
	Campus       string        `json:"campus"`
	IdentityTags *IdentityTags `json:"identity_tags,omitempty"`
	RewardMethod *string       `json:"reward_method,omitempty"`
	DurationDays *int          `json:"duration_days,omitempty"`
}

// Content 是已通过字段规则且补齐默认值的私有内容；不得直接当作公开投影。
type Content struct {
	Category     string
	Body         string
	Campus       string
	IdentityTags IdentityTags
	RewardMethod string
	DurationDays int
}

// Policy 不拥有字典资源；年级未填写时不依赖外部字典，填写但字典缺失则失败关闭。
type Policy struct {
	grades Grades
	logger *zap.Logger
}

// NewPolicy 只构造规则服务，不读取配置、身份、字典或数据库。
func NewPolicy(logger *zap.Logger, grades Grades) (*Policy, error) {
	if logger == nil {
		return nil, command.ErrInvalid
	}
	return &Policy{grades: grades, logger: logger}, nil
}

// ValidateContent 按 Unicode 码点而非字节计算正文，不裁剪、规范化或改变用户原文。
func (p *Policy) ValidateContent(ctx context.Context, requestID string, input ContentInput) (out Content, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrUnavailable
		}
		if err != nil {
			out = Content{}
		}
		p.complete("validate_content", requestID, start, err)
	}()
	if ctx.Err() != nil {
		return out, command.ErrUnavailable
	}
	if !slices.Contains([]string{"help", "team", "items", "dating", "skill_exchange", "other"}, input.Category) || !campus(input.Campus) || !utf8.ValidString(input.Body) || strings.TrimSpace(input.Body) == "" || utf8.RuneCountInString(input.Body) > 500 {
		return out, command.ErrInvalid
	}
	out = Content{Category: input.Category, Body: input.Body, Campus: input.Campus, RewardMethod: "sincere_thanks", DurationDays: 7}
	if input.RewardMethod != nil {
		if !slices.Contains([]string{"milk_tea", "meal", "reciprocal_gift", "skill_exchange", "sincere_thanks"}, *input.RewardMethod) {
			return out, command.ErrInvalid
		}
		out.RewardMethod = *input.RewardMethod
	}
	if input.DurationDays != nil {
		if *input.DurationDays < 1 || *input.DurationDays > 30 {
			return out, command.ErrInvalid
		}
		out.DurationDays = *input.DurationDays
	}
	if tags := input.IdentityTags; tags != nil {
		if tags.Campus != nil && !campus(*tags.Campus) {
			return out, command.ErrInvalid
		}
		if tags.Gender != nil && !slices.Contains([]string{"male", "female", "undisclosed"}, *tags.Gender) {
			return out, command.ErrInvalid
		}
		if tags.Grade != nil {
			if !utf8.ValidString(*tags.Grade) || strings.TrimSpace(*tags.Grade) == "" {
				return out, command.ErrInvalid
			}
			if p.grades == nil {
				return out, command.ErrUnavailable
			}
			allowed, failure := p.grades.Allows(ctx, *tags.Grade)
			if failure != nil || ctx.Err() != nil {
				return out, command.ErrUnavailable
			}
			if !allowed {
				return out, command.ErrInvalid
			}
		}
		// 独立拷贝指针字段，调用方修改输入不能改变已校验内容。
		out.IdentityTags = copyTags(*tags)
	}
	return out, nil
}

func campus(value string) bool {
	return slices.Contains([]string{"wushan", "university_town", "international", "any"}, value)
}

func copyTags(tags IdentityTags) IdentityTags {
	copyValue := func(value *string) *string {
		if value == nil {
			return nil
		}
		clone := *value
		return &clone
	}
	return IdentityTags{Campus: copyValue(tags.Campus), Grade: copyValue(tags.Grade), Gender: copyValue(tags.Gender)}
}

func (p *Policy) complete(operation, requestID string, start time.Time, err error) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	if len(requestID) > 128 || strings.ContainsFunc(requestID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	}) {
		requestID = ""
	}
	// 不记录原文、标签、原始摘要或字典供应商错误，调用成功仅表示规则计算完成。
	p.logger.Info("心愿规则计算完成", zap.String("operation", operation), zap.String("request_id", requestID), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
