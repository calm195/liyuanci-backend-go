package command

import (
	"slices"
	"testing"
)

// TestWhisperResourceOrder 验证树洞父帖先于暖评和审核，既有业务资源相对锁序不变。
func TestWhisperResourceOrder(t *testing.T) {
	kinds := []ResourceKind{Asset, IdentityReview, Wish, Application, Match, Conversation, WhisperPost, WhisperNickname, WhisperCapsule, WhisperComment, ModerationTask, Message, Notification, Report, Penalty, Appeal, CrisisCase, EvidenceCase}
	var resources []Resource
	for _, kind := range kinds {
		resources = append(resources, Resource{Kind: kind, ID: actorID})
	}
	slices.Reverse(resources)
	plan, err := NormalizePlan(Discovery{Resources: resources, Accounts: []string{actorID, otherID, actorID}})
	if err != nil || len(plan.Accounts) != 2 || plan.Accounts[0] != otherID {
		t.Fatal("账号集合未规范化", err)
	}
	for i, ref := range plan.Resources {
		if ref.Kind != kinds[i] {
			t.Fatalf("锁序错误：位置%d，实际%s", i, ref.Kind)
		}
	}
}

// TestWhisperExpectations 验证资源版本、父帖版本与评论代次独立绑定，同字段或错对象拒绝。
func TestWhisperExpectations(t *testing.T) {
	for _, kind := range []ResourceKind{WhisperPost, WhisperComment, WhisperNickname, WhisperCapsule} {
		t.Run(string(kind), func(t *testing.T) {
			in := input("synthetic-whisper")
			in.Expectations[0].Target.Kind = kind
			if _, err := requestHash(definition(), in); err != nil {
				t.Fatal("树洞资源版本被拒绝", err)
			}
		})
	}
	post := Resource{Kind: WhisperPost, ID: otherID}
	d := definition()
	d.RequiredExpectations = []string{"expected_version", "expected_post_version", "expected_comments_epoch"}
	in := input("synthetic-comment")
	in.Expectations[0].Target.Kind = WhisperComment
	in.Expectations = append(in.Expectations, Expectation{Name: "expected_post_version", Target: post, Value: 3}, Expectation{Name: "expected_comments_epoch", Target: post, Value: 8})
	if !validDefinition(d) {
		t.Fatal("新并发字段未注册")
	}
	first, err := requestHash(d, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Expectations[2].Value++
	changed, err := requestHash(d, in)
	if err != nil || slices.Equal(first, changed) {
		t.Fatal("评论代次未纳入请求指纹", err)
	}
	in.Expectations[2].Target.Kind = Conversation
	if _, err = requestHash(d, in); err != ErrInvalid {
		t.Fatal("会话不可冒充评论代次", err)
	}
	in.Expectations[2].Target = post
	in.Expectations = append(in.Expectations, in.Expectations[2])
	if _, err = requestHash(d, in); err != ErrInvalid {
		t.Fatal("重复并发字段必须拒绝", err)
	}
}
