package wish

import (
	"math"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

func at() time.Time         { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }
func eligible() Eligibility { return Eligibility{Active: true, Verified: true} }
func snapshot(state State) Snapshot {
	s := Snapshot{State: state, Version: 3, ContentVersion: 2, DurationDays: 7, RestrictionEpoch: 1,
		Review: Review{ContentVersion: 2, RestrictionEpoch: 1, Approved: true, DetectionCompleted: true, SchoolRelated: pointer(false), PublicBody: "合成已审投影"}}
	if state == Published || state == Matching {
		s.PublishedAt = pointer(at().Add(-time.Hour))
		s.ExpiresAt = pointer(at().Add(time.Hour))
	}
	return s
}

// TestPublishDeadlinesAndQuota 追溯 T014/REQ-004/005：额度、首次上架、刷新与截止时刻。
func TestPublishDeadlinesAndQuota(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expiry   *time.Time
		refresh  bool
		count    int
		want     State
		deadline *time.Time
	}{
		{"first", nil, false, 0, Published, pointer(at().Add(7 * 24 * time.Hour))},
		{"limit", nil, false, 3, Ready, nil},
		{"limit_exceeded", nil, false, 4, Ready, nil},
		{"slot_available", nil, false, 2, Published, pointer(at().Add(7 * 24 * time.Hour))},
		{"preserve", pointer(at().Add(time.Hour)), false, 0, Published, pointer(at().Add(time.Hour))},
		{"deadline_equal", pointer(at()), false, 0, Expired, pointer(at())},
		{"deadline_past_limit", pointer(at().Add(-time.Hour)), false, 3, Expired, pointer(at().Add(-time.Hour))},
		{"refresh", pointer(at().Add(-time.Hour)), true, 0, Published, pointer(at().Add(7 * 24 * time.Hour))},
		{"refresh_limit", pointer(at().Add(-time.Hour)), true, 3, Ready, pointer(at().Add(-time.Hour))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := NewPolicy(zap.NewNop(), nil)
			s := snapshot(Ready)
			s.ExpiresAt = tc.expiry
			s.RefreshOnPublish = tc.refresh
			out, err := p.Plan("", at(), eligible(), s, tc.count, Change{Action: Publish, ExpectedVersion: 3})
			if err != nil || out.Next.State != tc.want || out.Next.Version != 4 {
				t.Fatalf("%+v %v", out, err)
			}
			if (out.Next.ExpiresAt == nil) != (tc.deadline == nil) || tc.deadline != nil && !out.Next.ExpiresAt.Equal(*tc.deadline) {
				t.Fatal("截止时间错误")
			}
			if out.Next.State == Published {
				if !out.Next.PublishedAt.Equal(at()) || out.Next.RefreshOnPublish {
					t.Fatal("上架时间或刷新未消费")
				}
			} else if out.Next.PublishedAt != nil || out.Next.RefreshOnPublish != s.RefreshOnPublish {
				t.Fatal("未上架提前修改时钟或刷新选择")
			}
			if s.State != Ready || s.Version != 3 {
				t.Fatal("修改输入快照")
			}
		})
	}
}

// TestTransitionMatrix 验证允许/拒绝的状态矩阵；匹配及还愿回退不由本模块冒充实现。
func TestTransitionMatrix(t *testing.T) {
	p, _ := NewPolicy(zap.NewNop(), nil)
	for _, state := range []State{Draft, PendingReview, Rejected, Ready, Published, Matching, Realized, Fulfilled, Withdrawn, Expired} {
		for _, action := range []Action{Edit, SubmitReview, Publish, Withdraw, DeleteDraft} {
			t.Run(string(state)+"/"+string(action), func(t *testing.T) {
				change := Change{Action: action, ExpectedVersion: 3, ExpectedContentVersion: 2, DurationDays: 30, RefreshExpiry: true}
				out, err := p.Plan("", at(), eligible(), snapshot(state), 0, change)
				allowed := action == Edit && state != Matching && state != Realized && state != Fulfilled || action == SubmitReview && (state == Draft || state == Rejected) || action == Publish && state == Ready || action == Withdraw && state == Published || action == DeleteDraft && state == Draft
				if !allowed {
					if err != command.ErrConflict || out.Next.Version != 0 {
						t.Fatal("非法流转未清空结果", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				switch action {
				case Edit:
					if out.Next.State != Draft || out.Next.ContentVersion != 3 || out.Next.Review.Approved || !out.InvalidateReview || out.Next.DurationDays != 30 || !out.Next.RefreshOnPublish || out.InvalidateApplications != (state == Published) {
						t.Fatal("编辑没有使旧批准失效")
					}
				case SubmitReview:
					if out.Next.State != PendingReview || !out.EnqueueReview || !out.InvalidateReview || out.Next.Review.Approved {
						t.Fatal("提交没有重置审核")
					}
				case Withdraw:
					if out.Next.State != Withdrawn || !out.InvalidateApplications {
						t.Fatal("撤下没有作废申请")
					}
				case DeleteDraft:
					if !out.Delete {
						t.Fatal("缺删除标记")
					}
				}
			})
		}
	}
}

// TestCurrentReviewAndPrivacy 验证公开、上架复用相同屏障和当前审核条件，旧任务不能放行。
func TestCurrentReviewAndPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Snapshot, *Eligibility)
	}{
		{"inactive", func(_ *Snapshot, e *Eligibility) { e.Active = false }},
		{"unverified", func(_ *Snapshot, e *Eligibility) { e.Verified = false }},
		{"penalized", func(_ *Snapshot, e *Eligibility) { e.Restricted = true }},
		{"restricted", func(s *Snapshot, _ *Eligibility) { s.Restricted = true }},
		{"hold", func(s *Snapshot, _ *Eligibility) { s.PrivacyHeld = true }},
		{"old_content", func(s *Snapshot, _ *Eligibility) { s.Review.ContentVersion-- }},
		{"old_restriction", func(s *Snapshot, _ *Eligibility) { s.RestrictionEpoch++ }},
		{"old_hold_epoch", func(s *Snapshot, _ *Eligibility) { s.HoldEpoch++ }},
		{"not_approved", func(s *Snapshot, _ *Eligibility) { s.Review.Approved = false }},
		{"pending_detection", func(s *Snapshot, _ *Eligibility) { s.Review.DetectionCompleted = false }},
		{"unknown_school", func(s *Snapshot, _ *Eligibility) { s.Review.SchoolRelated = nil }},
		{"school_without_human", func(s *Snapshot, _ *Eligibility) { s.Review.SchoolRelated = pointer(true) }},
		{"no_projection", func(s *Snapshot, _ *Eligibility) { s.Review.PublicBody = " \t" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := NewPolicy(zap.NewNop(), nil)
			s, e := snapshot(Published), eligible()
			tc.change(&s, &e)
			if out, err := p.Public("", at(), e, s); err != command.ErrNotFound || out.Body != "" {
				t.Fatal("公开失败关闭失效", err)
			}
			s.State = Ready
			if _, err := p.Plan("", at(), e, s, 0, Change{Action: Publish, ExpectedVersion: 3}); err == nil {
				t.Fatal("不合法审核仍上架")
			}
		})
	}
}

func TestPublicProjectionAndCopy(t *testing.T) {
	p, _ := NewPolicy(zap.NewNop(), nil)
	s := snapshot(Published)
	s.Review.SchoolRelated = pointer(true)
	s.Review.HumanReviewed = true
	s.Review.Tags = IdentityTags{Gender: pointer("undisclosed"), Grade: pointer("合成年级")}
	out, err := p.Public("", at(), eligible(), s)
	if err != nil || out.NoticeText != "未经证实" || len(out.NoticeCodes) != 1 || out.NoticeCodes[0] != "UNVERIFIED_SCHOOL" {
		t.Fatal("涉校提示缺失", err)
	}
	*out.IdentityTags.Grade = "已修改"
	if *s.Review.Tags.Grade != "合成年级" {
		t.Fatal("公开标签引用源指针")
	}
	e := eligible()
	e.HideIdentityTags = true
	out, err = p.Public("", at(), e, s)
	if err != nil || out.IdentityTags.Gender != nil || out.IdentityTags.Grade != nil {
		t.Fatal("隐藏设置泄漏标签")
	}
	s.ExpiresAt = pointer(at())
	if _, err = p.Public("", at(), eligible(), s); err != command.ErrNotFound {
		t.Fatal("截止时刻仍公开")
	}
	for _, state := range []State{Draft, PendingReview, Ready, Matching, Realized, Fulfilled, Withdrawn, Expired, Rejected} {
		if _, err = p.Public("", at(), eligible(), snapshot(state)); err != command.ErrNotFound {
			t.Fatal("非上架状态公开", state, err)
		}
	}
}

func TestActiveCount(t *testing.T) {
	p, _ := NewPolicy(zap.NewNop(), nil)
	ended := snapshot(Published)
	ended.ExpiresAt = pointer(at())
	matching := snapshot(Matching)
	matching.ExpiresAt = pointer(at().Add(-time.Hour))
	rows := []Snapshot{snapshot(Published), ended, matching, snapshot(Ready), snapshot(Realized), snapshot(Fulfilled)}
	if n, err := p.CountActive("", at(), rows); n != 2 || err != nil {
		t.Fatalf("%d %v", n, err)
	}
	rows = append(rows, Snapshot{})
	if n, err := p.CountActive("", at(), rows); n != 0 || err != command.ErrInvalid {
		t.Fatal("错误返回部分额度")
	}
}

func TestVersionAndInvalidInputs(t *testing.T) {
	for _, mode := range []string{"version", "content_version", "missing_content_version", "missing_version", "invalid_duration", "version_overflow", "content_overflow", "unknown_action", "negative_count", "invalid_snapshot", "zero_time", "expired_withdraw"} {
		t.Run(mode, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			p, _ := NewPolicy(zap.New(core), nil)
			s := snapshot(Draft)
			now := at()
			count := 0
			change := Change{Action: SubmitReview, ExpectedVersion: 3, ExpectedContentVersion: 2}
			want := command.ErrInvalid
			switch mode {
			case "version":
				change.ExpectedVersion--
				want = command.ErrVersionConflict
			case "content_version":
				change.ExpectedContentVersion--
				want = command.ErrVersionConflict
			case "missing_content_version":
				change.ExpectedContentVersion = 0
			case "missing_version":
				change.ExpectedVersion = 0
			case "invalid_duration":
				change.Action = Edit
			case "version_overflow":
				s.Version = math.MaxInt64
				change.ExpectedVersion = s.Version
				want = command.ErrConflict
			case "content_overflow":
				s.ContentVersion = math.MaxInt64
				change.Action = Edit
				change.DurationDays = 7
				want = command.ErrConflict
			case "unknown_action":
				change.Action = "SECRET"
			case "negative_count":
				count = -1
			case "invalid_snapshot":
				s.State = "unknown"
			case "zero_time":
				now = time.Time{}
			case "expired_withdraw":
				s = snapshot(Published)
				s.ExpiresAt = pointer(at())
				change.Action = Withdraw
				want = command.ErrConflict
			}
			if out, err := p.Plan("SECRET\n", now, eligible(), s, count, change); err != want || out.Next.Version != 0 {
				t.Fatal("错误类型或失败结果不正确", err)
			}
			if logs.Len() != 1 || logs.All()[0].ContextMap()["result"] != "failure" || logs.All()[0].ContextMap()["request_id"] != "" {
				t.Fatal("失败日志错误")
			}
		})
	}
}
