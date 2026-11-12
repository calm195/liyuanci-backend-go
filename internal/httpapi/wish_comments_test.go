package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

type commentBoundaryService struct {
	WishComments
	calls int
	input wish.CommentInput
}

func (s *commentBoundaryService) SaveComment(_ context.Context, _, _, _, _, _ string, in wish.CommentInput) (wish.Comment, error) {
	s.calls++
	s.input = in
	return wish.Comment{ID: uuid.NewString(), Status: "pending_scan"}, nil
}

// TestWishCommentOptionalFields 验证最小合法请求不会被可选字段误拒；所有坏输入明确返回400。
func TestWishCommentOptionalFields(t *testing.T) {
	s := &commentBoundaryService{}
	r := New(zap.NewNop(), nil)
	RegisterWishComments(r, s)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"body":"补充信息","expected_post_version":1,"expected_comments_epoch":1}`, 202},
		{`{"body":"补充信息","kind":"author_supplement","expected_post_version":1,"expected_comments_epoch":1}`, 202},
		{`{"body":"缺失版本"}`, 400},
		{`{"body":null,"expected_post_version":1,"expected_comments_epoch":1}`, 400},
		{`{"body":"a","body":"b","expected_post_version":1,"expected_comments_epoch":1}`, 400},
		{`{"body":"a","unknown":1,"expected_post_version":1,"expected_comments_epoch":1}`, 400},
	} {
		q := httptest.NewRequest("POST", "/api/v1/wishes/"+uuid.NewString()+"/comments", strings.NewReader(tc.body))
		q.Header.Set("Authorization", "Bearer synthetic")
		q.Header.Set("Idempotency-Key", uuid.NewString())
		q.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, q)
		if out.Code != tc.status || out.Body.Len() == 0 {
			t.Fatalf("status=%d body=%s", out.Code, out.Body.String())
		}
	}
	if s.calls != 2 {
		t.Fatal("非法请求进入领域", s.calls)
	}
}
