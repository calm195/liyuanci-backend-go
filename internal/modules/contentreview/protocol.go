// Package contentreview 定义心愿、应愿、故事和聊天共用的普通内容审核协议。
// 本包只保存稳定的任务路由和结果投影，不提供服务、查询或具体内容生命周期规则。
package contentreview

import "github.com/calm195/liyuanci-backend-go/internal/execution/durable"

// Route 返回生产者和审核消费者共用的持久工作映射。
// 历史名称wish_review及wish.review_requested已经写入数据库，不能随包名或功能归组变更。
// 消费者仍须复核任务、版本及当前授权，不直接信任技术唤醒。
func Route() durable.Route {
	return durable.Route{Kind: "wish_review", TargetKind: "moderation_task", EventType: "wish.review_requested"}
}

// Result 描述当前精确内容版本的本轮审核结论，审核通过不等于业务上架或消息送达。
type Result struct {
	Decision           string  `json:"decision"`
	ContentVersion     int64   `json:"content_version"`
	Reason             *string `json:"reason,omitempty"`
	RevisionSuggestion *string `json:"revision_suggestion,omitempty"`
}
