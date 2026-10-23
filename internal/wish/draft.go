package wish

import (
	"context"
	"slices"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// DraftPlan 同时携带私有内容与状态计划，必须由调用方在同一根事务加密持久化。
// 它不分配资源标识、不授予所有权，也不代表数据库提交成功。
type DraftPlan struct {
	Content    Content
	Transition Transition
}

// CreateDraft 在当前作者资格通过后校验完整输入，初始化无审核、无上架时钟的私有草稿。
// 调用方必须在命令引擎内复核当前身份，并由持久层建立作者关系与幂等回执。
func (p *Policy) CreateDraft(ctx context.Context, requestID string, owner Eligibility, input ContentInput) (out DraftPlan, err error) {
	start := time.Now()
	defer func() {
		if err != nil {
			out = DraftPlan{}
		}
		p.complete("prepare_create_draft", requestID, start, err)
	}()
	if ctx.Err() != nil {
		return out, command.ErrUnavailable
	}
	if !owner.Active || !owner.Verified || owner.Restricted {
		return out, command.ErrForbidden
	}
	content, err := p.ValidateContent(ctx, requestID, input)
	if err != nil {
		return out, err
	}
	out.Content = content
	// 草稿不占活跃额度；有效期从首次成功上架起算，创建不得提前启动时钟。
	out.Transition.Next = Snapshot{
		State:            Draft,
		Version:          1,
		ContentVersion:   1,
		DurationDays:     content.DurationDays,
		RestrictionEpoch: 1,
	}
	return out, nil
}

// EditDraft 把完整内容校验与编辑状态计划绑定，避免期限与正文分别校验后出现不一致。
// 当前快照必须在持锁事务中读取；返回计划须与旧审核、申请作废及幂等回执原子提交。
func (p *Policy) EditDraft(ctx context.Context, requestID string, now time.Time, owner Eligibility, current Snapshot, expectedVersion int64, refreshExpiry bool, input ContentInput) (out DraftPlan, err error) {
	start := time.Now()
	defer func() {
		if err != nil {
			out = DraftPlan{}
		}
		p.complete("prepare_edit_draft", requestID, start, err)
	}()
	if ctx.Err() != nil {
		return out, command.ErrUnavailable
	}
	// 先检查当前授权、版本与可编辑状态；不让旧请求触发字典外部访问。
	transition, err := p.Plan(requestID, now, owner, current, 0, Change{
		Action:          Edit,
		ExpectedVersion: expectedVersion,
		DurationDays:    current.DurationDays,
		RefreshExpiry:   refreshExpiry,
	})
	if err != nil {
		return out, err
	}
	content, err := p.ValidateContent(ctx, requestID, input)
	if err != nil {
		return out, err
	}
	// 隐私修订不借编辑已建立关系刷新期限或改变期限配置。
	if slices.Contains([]State{Matching, Realized, Fulfilled}, current.State) && (refreshExpiry || content.DurationDays != current.DurationDays) {
		return out, command.ErrConflict
	}
	transition.Next.DurationDays = content.DurationDays
	out.Content = content
	out.Transition = transition
	return out, nil
}
