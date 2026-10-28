package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

type interactionBlockStub struct{ calls int }

func (s *interactionBlockStub) CreateBlock(context.Context, string, string, string, whisper.BlockInput) (whisper.BlockAck, error) {
	s.calls++
	return whisper.BlockAck{Accepted: true}, nil
}
func (s *interactionBlockStub) ListBlocks(context.Context, string, string, whisper.Query) (whisper.Page[whisper.BlockEntry], error) {
	s.calls++
	return whisper.Page[whisper.BlockEntry]{}, nil
}
func (s *interactionBlockStub) DeleteBlock(context.Context, string, string, string, string, int64) error {
	s.calls++
	return nil
}

// TestInteractionBlockProtocol 拒绝额外账号、嵌套别名、重复键、空字段及伪造来源版本。
func TestInteractionBlockProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
	}{
		{"目标账号注入", "POST", "", `{"source":{"kind":"public_profile","id":"a"},"target_user_id":"b"}`, 400},
		{"来源多字段", "POST", "", `{"source":{"kind":"public_profile","id":"a","target_user_id":"b"}}`, 400},
		{"主页版本", "POST", "", `{"source":{"kind":"public_profile","id":"a","content_version":1}}`, 400},
		{"匿名别名", "POST", "", `{"source":{"kind":"whisper_post","id":"a","alias":"b","content_version":1}}`, 400},
		{"缺正文版本", "POST", "", `{"source":{"kind":"whisper_post","id":"a"}}`, 400},
		{"空来源", "POST", "", `{"source":null}`, 400},
		{"重复来源", "POST", "", `{"source":{},"source":{}}`, 400},
		{"版本字符串", "POST", "", `{"source":{"kind":"whisper_post","id":"a","content_version":"1"}}`, 400},
		{"解除额外字段", "DELETE", "/opaque", `{"expected_version":1,"target_user_id":"a"}`, 400},
		{"解除缺版本", "DELETE", "/opaque", `{}`, 400},
		{"列表多参数", "GET", "?limit=2&limit=3", "", 400},
		{"列表账号筛选", "GET", "?target_user_id=a", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &interactionBlockStub{}
			r := New(zap.NewNop(), nil)
			if e := RegisterInteractionBlocks(r, service); e != nil {
				t.Fatal(e)
			}
			req := httptest.NewRequest(tc.method, "/api/v1/me/interaction-blocks"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set("Idempotency-Key", "synthetic")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)
			if rr.Code != tc.status || service.calls != 0 || rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(rr.Code, rr.Body.String(), service.calls)
			}
		})
	}
}
