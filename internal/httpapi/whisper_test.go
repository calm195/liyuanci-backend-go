package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

type whisperTransport struct {
	WhisperService
	calls int
}

func (s *whisperTransport) CreatePost(context.Context, string, string, string, whisper.ContentInput) (whisper.OwnerPost, error) {
	s.calls++
	return whisper.OwnerPost{}, nil
}
func (s *whisperTransport) ListPosts(context.Context, string, string, bool, whisper.Query) (any, error) {
	s.calls++
	return nil, nil
}

// TestWhisperStrictJSON 在领域调用前拒绝重复、大小写变体、嵌套无效键和代理项。
func TestWhisperStrictJSON(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"重复", `{"body":"甲","body":"乙","identity":{"mode":"anonymous"}}`},
		{"大小写", `{"Body":"甲","identity":{"mode":"anonymous"}}`},
		{"未知", `{"body":"甲","identity":{"mode":"anonymous","account_id":"泄漏"}}`},
		{"空值", `{"body":null,"identity":{"mode":"anonymous"}}`},
		{"嵌套重复", `{"body":"甲","identity":{"mode":"anonymous","mode":"anonymous"}}`},
		{"高代理项", `{"body":"\ud800","identity":{"mode":"anonymous"}}`},
		{"低代理项", `{"body":"\udc00","identity":{"mode":"anonymous"}}`},
		{"超码点", `{"body":"` + strings.Repeat("鲤", 501) + `","identity":{"mode":"anonymous"}}`},
		{"纯空白", `{"body":"  ","identity":{"mode":"anonymous"}}`},
		{"缺身份", `{"body":"甲"}`},
		{"超字节", `{"body":"` + strings.Repeat("字", 5500) + `","identity":{"mode":"anonymous"}}`},
		{"非法编码", `{"body":"` + string([]byte{255}) + `","identity":{"mode":"anonymous"}}`},
		{"标签空值", `{"body":"甲","identity":{"mode":"semi","dictionary_version":1,"tags":{"campus":"wushan","grade":null,"gender":"male"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			s := &whisperTransport{}
			if e := RegisterWhispers(r, s); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/me/whispers/posts", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set("Idempotency-Key", uuid.NewString())
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != 400 || s.calls != 0 || !strings.Contains(w.Body.String(), "VALIDATION_FAILED") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("严格协议校验失效", w.Code, w.Body.String())
			}
		})
	}
	if !strictWhisperUnicode([]byte(`{"text":"\ud83d\ude00","literal":"\\ud800"}`)) {
		t.Fatal("错误拒绝合法代理对或字面转义")
	}
}

// TestWhisperQueryAndAuth 验证公开入口非法凭据不退为游客，分页不接受作者过滤。
func TestWhisperQueryAndAuth(t *testing.T) {
	for _, tc := range []struct {
		path, auth string
		status     int
	}{
		{"/api/v1/whispers/posts?limit=0", "", 400}, {"/api/v1/whispers/posts?limit=51", "", 400}, {"/api/v1/whispers/posts?author_id=secret", "", 400}, {"/api/v1/whispers/posts?cursor=", "", 400}, {"/api/v1/whispers/posts?limit=2&limit=3", "", 400}, {"/api/v1/whispers/posts", "Bad token", 401}, {"/api/v1/me/whispers/posts", "", 401},
	} {
		r := gin.New()
		s := &whisperTransport{}
		_ = RegisterWhispers(r, s)
		req := httptest.NewRequest("GET", tc.path, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.status || s.calls != 0 {
			t.Fatal("分页或会话边界失效", tc.path, w.Code)
		}
	}
}
