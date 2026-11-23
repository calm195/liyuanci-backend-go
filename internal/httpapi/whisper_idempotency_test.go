package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
)

type interactionKeyTransport struct {
	WhisperInteractions
	calls int
}

func (s *interactionKeyTransport) SaveComment(context.Context, string, string, string, string, string, whisper.CommentInput) (whisper.OwnerComment, error) {
	s.calls++
	return whisper.OwnerComment{}, nil
}

type capsuleKeyTransport struct {
	WhisperCapsules
	calls int
}

func (s *capsuleKeyTransport) CreateCapsule(context.Context, string, string, string, whisper.CapsuleInput) (whisper.Capsule, error) {
	s.calls++
	return whisper.Capsule{}, nil
}

// TestWhisperWriteIdempotencyHeader 在领域调用前拒绝重复键，并保留单键请求。
func TestWhisperWriteIdempotencyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, body string
		register         func(*gin.Engine) (func() int, error)
	}{
		{
			"暖评", "/api/v1/whispers/posts/00000000-0000-4000-8000-000000000001/comments",
			`{"expected_post_version":1,"expected_comments_epoch":1,"body":"正文","identity":{"mode":"anonymous"}}`,
			func(r *gin.Engine) (func() int, error) {
				s := &interactionKeyTransport{}
				err := RegisterWhisperInteractions(r, s)
				return func() int { return s.calls }, err
			},
		},
		{
			"胶囊", "/api/v1/me/whispers/capsules",
			`{"body":"正文","unlock_at":"2026-10-29T00:00:00Z","safety_notice_accepted":true}`,
			func(r *gin.Engine) (func() int, error) {
				s := &capsuleKeyTransport{}
				err := RegisterWhisperCapsules(r, s)
				return func() int { return s.calls }, err
			},
		},
	} {
		for _, headers := range []struct {
			name          string
			keys          []string
			status, calls int
		}{
			{"缺失", nil, http.StatusBadRequest, 0},
			{"重复相同", []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000001"}, http.StatusBadRequest, 0},
			{"重复不同", []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"}, http.StatusBadRequest, 0},
			{"单键", []string{"00000000-0000-4000-8000-000000000001"}, http.StatusAccepted, 1},
		} {
			t.Run(tc.name+"/"+headers.name, func(t *testing.T) {
				r := gin.New()
				calls, err := tc.register(r)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
				req.Header.Set("Authorization", "Bearer synthetic")
				req.Header.Set("Content-Type", "application/json")
				for _, key := range headers.keys {
					req.Header.Add("Idempotency-Key", key)
				}
				out := httptest.NewRecorder()
				r.ServeHTTP(out, req)
				if out.Code != headers.status || calls() != headers.calls {
					t.Fatalf("状态 %d，领域调用 %d，响应 %s", out.Code, calls(), out.Body.String())
				}
			})
		}
	}
}
