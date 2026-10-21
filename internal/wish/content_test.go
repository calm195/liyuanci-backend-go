package wish

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

type gradeFunc func(context.Context, string) (bool, error)

func (f gradeFunc) Allows(ctx context.Context, value string) (bool, error) { return f(ctx, value) }
func pointer[T any](value T) *T                                            { return &value }
func input() ContentInput                                                  { return ContentInput{Category: "help", Body: "合成心愿", Campus: "any"} }

// TestContentBoundaries 追溯 T014/REQ-003、014：精确码点、默认值、枚举和可选标签。
func TestContentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ContentInput)
		want   error
	}{
		{"defaults", func(*ContentInput) {}, nil},
		{"500_emoji", func(in *ContentInput) { in.Body = strings.Repeat("🐟", 500) }, nil},
		{"501_emoji", func(in *ContentInput) { in.Body = strings.Repeat("🐟", 501) }, command.ErrInvalid},
		{"combining_codepoints", func(in *ContentInput) { in.Body = strings.Repeat("e\u0301", 251) }, command.ErrInvalid},
		{"whitespace", func(in *ContentInput) { in.Body = " \t\n\u3000" }, command.ErrInvalid},
		{"invalid_utf8", func(in *ContentInput) { in.Body = string([]byte{255}) }, command.ErrInvalid},
		{"category", func(in *ContentInput) { in.Category = "unknown" }, command.ErrInvalid},
		{"campus", func(in *ContentInput) { in.Campus = "unknown" }, command.ErrInvalid},
		{"day_zero", func(in *ContentInput) { in.DurationDays = pointer(0) }, command.ErrInvalid},
		{"day_negative", func(in *ContentInput) { in.DurationDays = pointer(-1) }, command.ErrInvalid},
		{"day_one", func(in *ContentInput) { in.DurationDays = pointer(1) }, nil},
		{"day_thirty", func(in *ContentInput) { in.DurationDays = pointer(30) }, nil},
		{"day_31", func(in *ContentInput) { in.DurationDays = pointer(31) }, command.ErrInvalid},
		{"empty_reward", func(in *ContentInput) { in.RewardMethod = pointer("") }, command.ErrInvalid},
		{"reward", func(in *ContentInput) { in.RewardMethod = pointer("meal") }, nil},
		{"tags_empty", func(in *ContentInput) { in.IdentityTags = &IdentityTags{} }, nil},
		{"gender", func(in *ContentInput) { in.IdentityTags = &IdentityTags{Gender: pointer("undisclosed")} }, nil},
		{"gender_unknown", func(in *ContentInput) { in.IdentityTags = &IdentityTags{Gender: pointer("other")} }, command.ErrInvalid},
		{"tag_campus", func(in *ContentInput) { in.IdentityTags = &IdentityTags{Campus: pointer("")} }, command.ErrInvalid},
		{"grade_empty", func(in *ContentInput) { in.IdentityTags = &IdentityTags{Grade: pointer(" ")} }, command.ErrInvalid},
		{"grade_missing_dictionary", func(in *ContentInput) { in.IdentityTags = &IdentityTags{Grade: pointer("合成年级")} }, command.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, _ := NewPolicy(zap.NewNop(), nil)
			in := input()
			tc.change(&in)
			out, err := policy.ValidateContent(context.Background(), "request", in)
			if err != tc.want {
				t.Fatalf("错误=%v，期望=%v", err, tc.want)
			}
			if err != nil {
				if out.Body != "" {
					t.Fatal("失败返回部分正文")
				}
				return
			}
			if out.Body != in.Body {
				t.Fatal("正文被修改")
			}
			if in.DurationDays == nil && out.DurationDays != 7 {
				t.Fatal("默认期限错误")
			}
			if in.RewardMethod == nil && out.RewardMethod != "sincere_thanks" {
				t.Fatal("默认报答错误")
			}
		})
	}
}

// TestGradeFailures 覆盖资料未交付、拒绝、故障和 panic，不臆造年级规则或泄漏供应商原文。
func TestGradeFailures(t *testing.T) {
	for _, mode := range []string{"allowed", "denied", "failure", "panic", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy, _ := NewPolicy(zap.New(core), gradeFunc(func(context.Context, string) (bool, error) {
				switch mode {
				case "denied":
					return false, nil
				case "failure":
					return false, errors.New("SECRET")
				case "panic":
					panic("SECRET")
				case "cancelled":
					cancel()
				}
				return true, nil
			}))
			in := input()
			in.IdentityTags = &IdentityTags{Grade: pointer("合成年级"), Gender: pointer("undisclosed")}
			out, err := policy.ValidateContent(ctx, "SECRET\n", in)
			want := command.ErrUnavailable
			if mode == "allowed" {
				want = nil
			} else if mode == "denied" {
				want = command.ErrInvalid
			}
			if err != want {
				t.Fatal(err)
			}
			if err == nil {
				*in.IdentityTags.Grade = "已改变"
				if *out.IdentityTags.Grade != "合成年级" {
					t.Fatal("结果引用输入")
				}
			}
			if logs.Len() != 1 || logs.All()[0].ContextMap()["request_id"] != "" || len(logs.All()[0].ContextMap()) != 4 {
				t.Fatal("完成日志脱敏失败")
			}
		})
	}
}

func TestPolicyRequiresLoggerAndHonorsCancellation(t *testing.T) {
	if _, err := NewPolicy(nil, nil); err != command.ErrInvalid {
		t.Fatal("缺日志仍构造")
	}
	p, _ := NewPolicy(zap.NewNop(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.ValidateContent(ctx, "", input()); err != command.ErrUnavailable {
		t.Fatal("取消后仍成功")
	}
}
