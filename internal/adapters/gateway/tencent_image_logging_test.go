package gateway

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestTencentImageFailureLogs 仅记录状态和经过约束的错误码，不能泄漏供应商错误正文。
func TestTencentImageFailureLogs(t *testing.T) {
	for _, tc := range []struct {
		name, code, want string
	}{
		{"技术错误码", "AccessDenied", "AccessDenied"},
		{"自由文本", "synthetic private error", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testTencent(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, "<Error><Code>%s</Code><Message>synthetic private message</Message></Error>", tc.code)
			})
			core, logs := observer.New(zap.InfoLevel)
			client.logger = zap.New(core)
			if approved, err := client.Scan(context.Background(), []byte("synthetic private image"), "image/png"); approved || err == nil {
				t.Fatal("供应商错误不能批准图片")
			}
			entries := logs.All()
			if len(entries) != 1 {
				t.Fatal(entries)
			}
			fields := entries[0].ContextMap()
			if fields["stage"] != "http_status" || fields["http_status"] != int64(403) || fields["provider_code"] != tc.want {
				t.Fatal(fields)
			}
			if len(fields) != 8 {
				t.Fatal("日志不应增加响应正文等其它字段", fields)
			}
		})
	}
}
