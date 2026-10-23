package wish

import (
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// State 与持久化心愿状态保持一致；规则不创建匹配或还愿记录。
type State string

const (
	// Draft 表示尚未提交的私有草稿。
	Draft State = "draft"
	// PendingReview 表示当前内容等待审核。
	PendingReview State = "pending_review"
	// Rejected 表示内容审核驳回。
	Rejected State = "rejected"
	// Ready 表示内容通过但尚未上架，不占活跃额度。
	Ready State = "ready"
	// Published 表示已上架，仍须实时核验截止时间。
	Published State = "published"
	// Matching 表示应愿中，不因原截止时间自动结束。
	Matching State = "matching"
	// Realized 表示已实现、等待还愿，不占活跃额度。
	Realized State = "realized"
	// Fulfilled 表示已提交还愿故事，故事公开资格需独立审核。
	Fulfilled State = "fulfilled"
	// Withdrawn 表示已撤下，不公开。
	Withdrawn State = "withdrawn"
	// Expired 表示已超过上架有效期。
	Expired State = "expired"
)

// Review 是当前版本的已审投影，不得从原文或旧审核复制构造。
type Review struct {
	ContentVersion     int64
	RestrictionEpoch   int64
	HoldEpoch          int64
	Approved           bool
	DetectionCompleted bool
	SchoolRelated      *bool
	HumanReviewed      bool
	PublicBody         string
	Tags               IdentityTags
}

// Snapshot 必须由调用方在持锁事务内读取，公开查询也须使用当前数据库快照。
type Snapshot struct {
	State            State
	Version          int64
	ContentVersion   int64
	DurationDays     int
	ExpiresAt        *time.Time
	PublishedAt      *time.Time
	RefreshOnPublish bool
	RestrictionEpoch int64
	HoldEpoch        int64
	PrivacyHeld      bool
	Restricted       bool
	Review           Review
}

// Eligibility 是当前作者资格，不是客户端声称的权限；隐藏设置必须来自当前账号。
type Eligibility struct {
	Active           bool
	Verified         bool
	Restricted       bool
	HideIdentityTags bool
}

// Action 由服务端路由固定选择，不支持客户端任意状态赋值。
type Action string

const (
	// Edit 创建新内容版本并使旧审核失效。
	Edit Action = "edit"
	// SubmitReview 显式提交指定内容版本。
	SubmitReview Action = "submit_review"
	// Publish 尝试上架，可能因额度或时间得到待上架或过期结果。
	Publish Action = "publish"
	// Withdraw 主动撤下当前有效的上架心愿。
	Withdraw Action = "withdraw"
	// DeleteDraft 只删除草稿，不提供普通内容或证据通用删除入口。
	DeleteDraft Action = "delete_draft"
)

// Change 不携带正文；编辑前必须先用 ValidateContent 验证完整内容。
type Change struct {
	Action                 Action
	ExpectedVersion        int64
	ExpectedContentVersion int64
	DurationDays           int
	RefreshExpiry          bool
}

// Transition 只描述同一根事务必须执行的结果，计算成功不能当作已提交。
type Transition struct {
	Next                   Snapshot
	Delete                 bool
	InvalidateApplications bool
	InvalidateReview       bool
	EnqueueReview          bool
}

// PublicProjection 仅含已审正文及允许公开标签，绝不包含原文密文或身份引用。
type PublicProjection struct {
	Body         string
	IdentityTags IdentityTags
	NoticeCodes  []string
	NoticeText   string
}

func validSnapshot(s Snapshot) bool {
	if !slices.Contains([]State{Draft, PendingReview, Rejected, Ready, Published, Matching, Realized, Fulfilled, Withdrawn, Expired}, s.State) || s.Version < 1 || s.ContentVersion < 1 || s.DurationDays < 1 || s.DurationDays > 30 || s.RestrictionEpoch < 1 || s.HoldEpoch < 0 {
		return false
	}
	if s.ExpiresAt != nil && s.ExpiresAt.IsZero() || s.PublishedAt != nil && s.PublishedAt.IsZero() {
		return false
	}
	return (s.State != Published && s.State != Matching && s.PublishedAt == nil) || (s.PublishedAt != nil && s.ExpiresAt != nil)
}

func reviewReady(s Snapshot) bool {
	r := s.Review
	return r.Approved && r.ContentVersion == s.ContentVersion && r.RestrictionEpoch == s.RestrictionEpoch && r.HoldEpoch == s.HoldEpoch && r.DetectionCompleted && r.SchoolRelated != nil && (!*r.SchoolRelated || r.HumanReviewed) && utf8.ValidString(r.PublicBody) && strings.TrimSpace(r.PublicBody) != "" && utf8.RuneCountInString(r.PublicBody) <= 500
}

func ordinary(e Eligibility, s Snapshot) bool {
	return e.Active && e.Verified && !e.Restricted && !s.Restricted && !s.PrivacyHeld
}

// manageable允许隐私屏障下替换原稿并提交新审；公开及交互仍由ordinary拒绝。
func manageable(e Eligibility, s Snapshot, a Action) bool {
	return ordinary(e, s) || (s.PrivacyHeld && e.Active && e.Verified && !e.Restricted && !s.Restricted && (a == Edit || a == SubmitReview || a == DeleteDraft))
}

func cloneSnapshot(s Snapshot) Snapshot {
	if s.ExpiresAt != nil {
		value := *s.ExpiresAt
		s.ExpiresAt = &value
	}
	if s.PublishedAt != nil {
		value := *s.PublishedAt
		s.PublishedAt = &value
	}
	if s.Review.SchoolRelated != nil {
		value := *s.Review.SchoolRelated
		s.Review.SchoolRelated = &value
	}
	s.Review.Tags = copyTags(s.Review.Tags)
	return s
}

// Plan 使用数据库时间和账号 guard 下的额度，返回需与版本/审核/申请失效一起原子提交的变更。
// 本方法不持锁、不写库、不授权本人；调用方必须先经过统一命令引擎的当前身份和所有权复核。
func (p *Policy) Plan(requestID string, now time.Time, owner Eligibility, current Snapshot, activeCount int, change Change) (out Transition, err error) {
	start := time.Now()
	defer func() {
		if err != nil {
			out = Transition{}
		}
		p.complete("plan_transition", requestID, start, err)
	}()
	if now.IsZero() || !validSnapshot(current) || change.ExpectedVersion < 1 || activeCount < 0 {
		return out, command.ErrInvalid
	}
	if !manageable(owner, current, change.Action) {
		return out, command.ErrForbidden
	}
	if change.ExpectedVersion != current.Version {
		return out, command.ErrVersionConflict
	}
	if current.Version == math.MaxInt64 {
		return out, command.ErrConflict
	}
	out.Next = cloneSnapshot(current)
	switch change.Action {
	case Edit:
		if !slices.Contains([]State{Draft, PendingReview, Rejected, Ready, Published, Withdrawn, Expired}, current.State) && !(current.PrivacyHeld && slices.Contains([]State{Matching, Realized, Fulfilled}, current.State)) {
			return out, command.ErrConflict
		}
		if change.DurationDays < 1 || change.DurationDays > 30 {
			return out, command.ErrInvalid
		}
		if current.ContentVersion == math.MaxInt64 {
			return out, command.ErrConflict
		}
		// 修改先回草稿并使旧审核失效，随后需显式提交；仅刷新期限同样不能沿用批准。
		out.Next.State = Draft
		if current.PrivacyHeld && slices.Contains([]State{Matching, Realized, Fulfilled}, current.State) {
			out.Next.State = current.State
		}
		out.Next.ContentVersion++
		out.Next.DurationDays = change.DurationDays
		out.Next.RefreshOnPublish = change.RefreshExpiry
		out.Next.Review = Review{}
		out.InvalidateReview = true
		out.InvalidateApplications = current.State == Published
	case SubmitReview:
		if current.State != Draft && current.State != Rejected && !(current.PrivacyHeld && slices.Contains([]State{Matching, Realized, Fulfilled, Withdrawn}, current.State)) {
			return out, command.ErrConflict
		}
		if change.ExpectedContentVersion < 1 {
			return out, command.ErrInvalid
		}
		if change.ExpectedContentVersion != current.ContentVersion {
			return out, command.ErrVersionConflict
		}
		out.Next.State = PendingReview
		if current.PrivacyHeld && slices.Contains([]State{Matching, Realized, Fulfilled}, current.State) {
			out.Next.State = current.State
		}
		out.Next.Review = Review{}
		out.InvalidateReview = true
		out.EnqueueReview = true
	case Publish:
		if current.State != Ready || !reviewReady(current) {
			return out, command.ErrConflict
		}
		// 先判到期，后判额度。满额不设置首次截止时间，也不提前消耗待刷新选择。
		if !current.RefreshOnPublish && current.ExpiresAt != nil && !now.Before(*current.ExpiresAt) {
			out.Next.State = Expired
		} else if activeCount >= 3 {
			out.Next.State = Ready
		} else {
			out.Next.State = Published
			if current.ExpiresAt == nil || current.RefreshOnPublish {
				deadline := now.Add(time.Duration(current.DurationDays) * 24 * time.Hour)
				out.Next.ExpiresAt = &deadline
			}
			out.Next.PublishedAt = &now
			out.Next.RefreshOnPublish = false
		}
	case Withdraw:
		if current.State != Published || !now.Before(*current.ExpiresAt) {
			return out, command.ErrConflict
		}
		out.Next.State = Withdrawn
		out.InvalidateApplications = true
	case DeleteDraft:
		if current.State != Draft {
			return out, command.ErrConflict
		}
		out.Delete = true
	default:
		return out, command.ErrInvalid
	}
	out.Next.Version++
	return out, nil
}

// CountActive 计算同一作者已锁定的完整候选集合；应愿中跨截止时间仍占额。
// 调用方不得传分页结果或缓存计数，否则不能作为并发上架的额度依据。
func (p *Policy) CountActive(requestID string, now time.Time, rows []Snapshot) (count int, err error) {
	start := time.Now()
	defer func() {
		if err != nil {
			count = 0
		}
		p.complete("count_active", requestID, start, err)
	}()
	if now.IsZero() {
		return 0, command.ErrInvalid
	}
	for _, row := range rows {
		if !validSnapshot(row) {
			return 0, command.ErrInvalid
		}
		if row.State == Matching || row.State == Published && now.Before(*row.ExpiresAt) {
			count++
		}
	}
	return count, nil
}

// Public 重新核对当前资格、期限、屏障与审核版本，仅输出已审投影；任何不满足均不可公开。
func (p *Policy) Public(requestID string, now time.Time, owner Eligibility, current Snapshot) (out PublicProjection, err error) {
	start := time.Now()
	defer func() { p.complete("public_projection", requestID, start, err) }()
	if now.IsZero() || !validSnapshot(current) {
		return out, command.ErrInvalid
	}
	if !ordinary(owner, current) || current.State != Published || !now.Before(*current.ExpiresAt) || !reviewReady(current) {
		return out, command.ErrNotFound
	}
	out.Body = current.Review.PublicBody
	out.NoticeCodes = []string{}
	if !owner.HideIdentityTags {
		out.IdentityTags = copyTags(current.Review.Tags)
	}
	if *current.Review.SchoolRelated {
		out.NoticeCodes = []string{"UNVERIFIED_SCHOOL"}
		out.NoticeText = "未经证实"
	}
	return out, nil
}
