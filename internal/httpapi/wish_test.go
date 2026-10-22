package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

type wishCreatorFixture struct {
	input       wish.ContentInput
	called      bool
	failure     error
	panicCreate bool
}

func (f *wishCreatorFixture) Create(_ context.Context, token, key, requestID string, in wish.ContentInput) (wish.CreatedDraft, error) {
	f.called = true
	f.input = in
	if token != "synthetic-token" || key != "synthetic-key" || requestID == "" {
		return wish.CreatedDraft{}, command.ErrInternal
	}
	if f.panicCreate {
		panic("SYNTHETIC_PRIVATE_BODY")
	}
	return wish.CreatedDraft{ID: "6c500000-0000-4000-8000-000000000001", Version: 1, ContentVersion: 1, State: wish.Draft}, f.failure
}

// TestWishCreationProtocol 追溯 T014/REQ-003/004/005/018：正式0.2.0创建形状与HTTP失败协议；服务为显式合成夹具。
func TestWishCreationProtocol(t *testing.T) {
	const valid = `{"category":"help","body":"SYNTHETIC_PRIVATE_BODY","campus":"any"}`
	for _, tc := range []struct {
		name, body          string
		failure             error
		status              int
		called, panicCreate bool
		noToken, noKey      bool
	}{
		{name: "created", body: valid, status: 201, called: true},
		{name: "full", body: `{"category":"help","body":"SYNTHETIC_PRIVATE_BODY","campus":"any","identity_tags":{"gender":"undisclosed"},"reward_method":"sincere_thanks","duration_days":30}`, status: 201, called: true},
		{name: "no_token", body: valid, status: 401, noToken: true},
		{name: "no_key", body: valid, status: 400, noKey: true},
		{name: "unknown", body: `{"category":"help","body":"x","campus":"any","owner_id":"x"}`, status: 400},
		{name: "missing", body: `{"category":"help","body":"x"}`, status: 400},
		{name: "null", body: `{"category":"help","body":"x","campus":"any","duration_days":null}`, status: 400},
		{name: "duplicate", body: `{"category":"help","body":"x","body":"y","campus":"any"}`, status: 400},
		{name: "trailing", body: valid + `{}`, status: 400},
		{name: "capitalization", body: `{"Category":"help","body":"x","campus":"any"}`, status: 400},
		{name: "tag_null", body: `{"category":"help","body":"x","campus":"any","identity_tags":{"gender":null}}`, status: 400},
		{name: "tag_duplicate", body: `{"category":"help","body":"x","campus":"any","identity_tags":{"gender":"male","gender":"female"}}`, status: 400},
		{name: "tag_unknown", body: `{"category":"help","body":"x","campus":"any","identity_tags":{"name":"x"}}`, status: 400},
		{name: "tag_array", body: `{"category":"help","body":"x","campus":"any","identity_tags":[]}`, status: 400},
		{name: "fraction", body: `{"category":"help","body":"x","campus":"any","duration_days":1.5}`, status: 400},
		{name: "wrong_type", body: `{"category":"help","body":1,"campus":"any"}`, status: 400},
		{name: "array", body: `[]`, status: 400},
		{name: "bad_utf8", body: strings.Replace(valid, "SYNTHETIC_PRIVATE_BODY", string([]byte{0xff}), 1), status: 400},
		{name: "too_large", body: strings.Replace(valid, "SYNTHETIC_PRIVATE_BODY", strings.Repeat("a", 1<<20), 1), status: 400},
		{name: "invalid", body: valid, failure: command.ErrInvalid, status: 400, called: true},
		{name: "unauthenticated", body: valid, failure: command.ErrUnauthenticated, status: 401, called: true},
		{name: "forbidden", body: valid, failure: command.ErrForbidden, status: 403, called: true},
		{name: "not_found", body: valid, failure: command.ErrNotFound, status: 404, called: true},
		{name: "conflict", body: valid, failure: command.ErrIdempotencyConflict, status: 409, called: true},
		{name: "unavailable", body: valid, failure: errors.New("SYNTHETIC_PRIVATE_BODY"), status: 503, called: true},
		{name: "panic", body: valid, status: 500, called: true, panicCreate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			r := New(zap.New(core), func(context.Context) error { return nil })
			fixture := &wishCreatorFixture{failure: tc.failure, panicCreate: tc.panicCreate}
			if err := RegisterWishCreation(r, fixture); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/v1/wishes", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if !tc.noToken {
				req.Header.Set("Authorization", "Bearer synthetic-token")
			}
			if !tc.noKey {
				req.Header.Set("Idempotency-Key", "synthetic-key")
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.status || fixture.called != tc.called {
				t.Fatalf("状态/调用错误: %d %v %s", rec.Code, fixture.called, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Request-ID") == "" {
				t.Fatal("缺少敏感缓存或请求关联头")
			}
			if strings.Contains(rec.Body.String(), "SYNTHETIC_PRIVATE_BODY") {
				t.Fatal("响应泄露私有正文")
			}
			entries := logs.FilterField(zap.String("event", "http_request_completed")).All()
			if len(entries) != 1 || entries[0].ContextMap()["status"] != int64(tc.status) {
				t.Fatal("缺少成功/失败/恢复路径最终请求日志", entries)
			}
			if tc.status == 201 {
				var wire struct {
					Data struct {
						ID             string   `json:"id"`
						Version        int64    `json:"version"`
						ContentVersion int64    `json:"content_version"`
						State          string   `json:"current_state"`
						Actions        []string `json:"allowed_actions"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
					t.Fatal(err)
				}
				if wire.Data.State != "draft" || wire.Data.Version != 1 || wire.Data.ContentVersion != 1 || wire.Data.ID == "" || wire.Data.Actions == nil || len(wire.Data.Actions) != 0 {
					t.Fatal("创建投影不符合契约", wire)
				}
				if tc.name == "created" && (fixture.input.DurationDays != nil || fixture.input.RewardMethod != nil || fixture.input.IdentityTags != nil) {
					t.Fatal("省略字段被HTTP层填充")
				}
				if tc.name == "full" && (*fixture.input.DurationDays != 30 || *fixture.input.IdentityTags.Gender != "undisclosed") {
					t.Fatal("显式字段丢失")
				}
			}
		})
	}
}

// TestWishRegistrationIsExplicit 验证默认入口关闭及显式装配参数，不让健康服务隐式暴露写操作。
func TestWishRegistrationIsExplicit(t *testing.T) {
	r := New(zap.NewNop(), func(context.Context) error { return nil })
	if RegisterWishCreation(nil, &wishCreatorFixture{}) == nil || RegisterWishCreation(r, nil) == nil {
		t.Fatal("接受无效装配")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/wishes", nil))
	if rec.Code != 404 {
		t.Fatal("默认创建入口未关闭")
	}
}
