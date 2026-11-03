package contentreview

import (
	"encoding/json"
	"testing"
)

// TestPersistedReviewProtocol 固定历史任务标识与对外JSON，避免归属调整让存量任务或回执不可读。
func TestPersistedReviewProtocol(t *testing.T) {
	route := Route()
	if route.Kind != "wish_review" || route.TargetKind != "moderation_task" || route.EventType != "wish.review_requested" {
		t.Fatal("持久审核路由发生变化", route)
	}
	reason, suggestion := "内容需修改", "请修改对应段落"
	for _, tc := range []struct {
		name  string
		value Result
		want  string
	}{
		{"通过", Result{Decision: "approved", ContentVersion: 7}, `{"decision":"approved","content_version":7}`},
		{
			"退回",
			Result{Decision: "rejected", ContentVersion: 7, Reason: &reason, RevisionSuggestion: &suggestion},
			`{"decision":"rejected","content_version":7,"reason":"内容需修改","revision_suggestion":"请修改对应段落"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil || string(raw) != tc.want {
				t.Fatalf("审核协议不兼容：%s %v", raw, err)
			}
		})
	}
}
