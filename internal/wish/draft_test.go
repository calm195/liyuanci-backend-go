package wish

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

func draftInput() ContentInput {
	return ContentInput{Category: "help", Body: "合成私有正文", Campus: "any"}
}

// TestCreateDraftPreparation 追溯 T014/REQ-003/004/005：创建私有草稿不占额、不提前计时或获得审核。
func TestCreateDraftPreparation(t *testing.T) {
	p, _ := NewPolicy(zap.NewNop(), nil)
	input := draftInput()
	input.IdentityTags = &IdentityTags{Gender: pointer("undisclosed")}
	out, err := p.CreateDraft(context.Background(), "create", eligible(), input)
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{State: Draft, Version: 1, ContentVersion: 1, DurationDays: 7, RestrictionEpoch: 1}
	if !reflect.DeepEqual(out.Transition, Transition{Next: want}) || out.Content.RewardMethod != "sincere_thanks" {
		t.Fatalf("草稿初始化错误: %+v", out)
	}
	*input.IdentityTags.Gender = "female"
	if *out.Content.IdentityTags.Gender != "undisclosed" {
		t.Fatal("内容保留输入指针")
	}
	if count, err := p.CountActive("", at(), []Snapshot{out.Transition.Next}); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err := p.Public("", at(), eligible(), out.Transition.Next); !errors.Is(err, command.ErrNotFound) {
		t.Fatal(err)
	}
}

// TestDraftPreparationFailure 追溯 T014/REQ-014/018：资格、取消及无效字段失败时不返回部分计划。
func TestDraftPreparationFailure(t *testing.T) {
	p, _ := NewPolicy(zap.NewNop(), nil)
	for _, owner := range []Eligibility{{}, {Active: true}, {Active: true, Verified: true, Restricted: true}} {
		out, err := p.CreateDraft(context.Background(), "", owner, draftInput())
		if !errors.Is(err, command.ErrForbidden) || !reflect.DeepEqual(out, DraftPlan{}) {
			t.Fatal(out, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := p.CreateDraft(ctx, "", eligible(), draftInput()); !errors.Is(err, command.ErrUnavailable) || !reflect.DeepEqual(out, DraftPlan{}) {
		t.Fatal(out, err)
	}
	input := draftInput()
	input.Body = ""
	if out, err := p.CreateDraft(context.Background(), "", eligible(), input); !errors.Is(err, command.ErrInvalid) || !reflect.DeepEqual(out, DraftPlan{}) {
		t.Fatal(out, err)
	}
}

// TestEditDraftPreparation 追溯 T014/REQ-003/004/008：新内容与状态版本一起推进，旧批准作废且保留原期限。
func TestEditDraftPreparation(t *testing.T) {
	p, _ := NewPolicy(zap.NewNop(), nil)
	current := snapshot(Published)
	before := cloneSnapshot(current)
	input := draftInput()
	input.DurationDays = pointer(30)
	out, err := p.EditDraft(context.Background(), "", at(), eligible(), current, current.Version, true, input)
	if err != nil {
		t.Fatal(err)
	}
	next := out.Transition.Next
	if next.State != Draft || next.Version != current.Version+1 || next.ContentVersion != current.ContentVersion+1 || next.DurationDays != 30 || out.Content.DurationDays != 30 || !next.RefreshOnPublish || !next.ExpiresAt.Equal(*current.ExpiresAt) || !reflect.DeepEqual(next.Review, Review{}) || !out.Transition.InvalidateReview || !out.Transition.InvalidateApplications || out.Transition.EnqueueReview {
		t.Fatalf("编辑计划错误: %+v", out)
	}
	*next.ExpiresAt = at()
	if !reflect.DeepEqual(current, before) {
		t.Fatal("编辑改变输入快照")
	}
	input.Body = ""
	if out, err := p.EditDraft(context.Background(), "", at(), eligible(), current, current.Version, false, input); !errors.Is(err, command.ErrInvalid) || !reflect.DeepEqual(out, DraftPlan{}) {
		t.Fatal(out, err)
	}
}

// TestEditDraftRejectsBeforeDictionary 追溯 T014/REQ-018：旧版本或禁止状态不触发外部字典；失败日志不包含私有内容。
func TestEditDraftRejectsBeforeDictionary(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	p, _ := NewPolicy(zap.New(core), nil)
	input := draftInput()
	input.IdentityTags = &IdentityTags{Grade: pointer("未交付字典")}
	for _, tc := range []struct {
		state   State
		version int64
		want    error
	}{
		{Draft, 2, command.ErrVersionConflict},
		{Matching, 3, command.ErrConflict},
		{Draft, 3, command.ErrUnavailable},
	} {
		out, err := p.EditDraft(context.Background(), "edit", at(), eligible(), snapshot(tc.state), tc.version, false, input)
		if !errors.Is(err, tc.want) || !reflect.DeepEqual(out, DraftPlan{}) {
			t.Fatal(out, err)
		}
	}
	entries := logs.FilterField(zap.String("operation", "prepare_edit_draft")).All()
	if len(entries) != 3 {
		t.Fatal("缺少操作完成日志")
	}
	for _, entry := range entries {
		fields := entry.ContextMap()
		if fields["result"] != "failure" || fields["request_id"] != "edit" || len(fields) != 4 {
			t.Fatal(fields)
		}
	}
}

// TestDraftCompletionAndCancellation 验证成功完成日志及编辑取消的零输出，不以规则成功冒充持久化提交。
func TestDraftCompletionAndCancellation(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	p, _ := NewPolicy(zap.New(core), nil)
	if _, err := p.CreateDraft(context.Background(), "create", eligible(), draftInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EditDraft(context.Background(), "edit", at(), eligible(), snapshot(Draft), 3, false, draftInput()); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"prepare_create_draft", "prepare_edit_draft"} {
		entries := logs.FilterField(zap.String("operation", operation)).All()
		if len(entries) != 1 || entries[0].ContextMap()["result"] != "success" || entries[0].ContextMap()["duration"] == nil {
			t.Fatalf("缺少成功完成日志: %s", operation)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := p.EditDraft(ctx, "cancel", at(), eligible(), snapshot(Draft), 3, false, draftInput())
	if !errors.Is(err, command.ErrUnavailable) || !reflect.DeepEqual(out, DraftPlan{}) {
		t.Fatal(out, err)
	}
}
