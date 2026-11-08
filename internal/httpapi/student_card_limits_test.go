package httpapi

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
)

type readingCards struct {
	cardsFixture
	limit, received int64
}

// Upload 模拟服务的文件字节校验，必须完整读到EOF，以覆盖multipart尾部和请求总上限。
func (f *readingCards) Upload(_ context.Context, _, _, _, _ string, source io.Reader) (studentcard.Asset, error) {
	f.called = true
	size, err := io.Copy(io.Discard, source)
	f.received = size
	if err != nil || size > f.limit {
		return studentcard.Asset{}, command.ErrInvalid
	}
	return studentcard.Asset{}, nil
}

// TestStudentCardBodyBudget 验证文件预算和multipart总预算分离，普通请求不会借Content-Type绕过上限。
func TestStudentCardBodyBudget(t *testing.T) {
	for _, tc := range []struct {
		name               string
		limit, size        int64
		largeHeader, extra bool
		status             int
	}{
		{name: "文件小于上限", limit: 1 << 20, size: 1<<20 - 1, status: 202},
		{name: "文件精确上限", limit: 1 << 20, size: 1 << 20, status: 202},
		{name: "文件超过上限", limit: 1 << 20, size: 1<<20 + 1, status: 400},
		{name: "四MiB精确上限", limit: 4 << 20, size: 4 << 20, status: 202},
		{name: "超过四MiB一字节", limit: 4 << 20, size: 4<<20 + 1, status: 400},
		{name: "协议开销超过预算", limit: 1 << 20, size: 1 << 20, largeHeader: true, status: 400},
		{name: "不接受额外字段", limit: 1 << 20, size: 1 << 20, extra: true, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &readingCards{limit: tc.limit}
			router := New(zap.NewNop(), nil)
			if err := RegisterStudentCards(router, fixture, tc.limit); err != nil {
				t.Fatal(err)
			}
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", `form-data; name="file"; filename="card.png"`)
			header.Set("Content-Type", "image/png")
			if tc.largeHeader {
				header.Set("X-Padding", strings.Repeat("x", int(studentCardMultipartOverhead)))
			}
			part, err := writer.CreatePart(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.CopyN(part, strings.NewReader(strings.Repeat("x", int(tc.size))), tc.size); err != nil {
				t.Fatal(err)
			}
			if tc.extra {
				if err = writer.WriteField("forged", "value"); err != nil {
					t.Fatal(err)
				}
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", studentCardUploadPath, &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			req.Header.Set("Authorization", "Bearer token")
			req.Header.Set("Idempotency-Key", "boundary")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d received=%d", rec.Code, tc.status, fixture.received)
			}
			if tc.status == 202 && fixture.received != tc.size {
				t.Fatal("合法图片被提前截断")
			}
		})
	}
	for _, contentType := range []string{"application/json", "multipart/form-data; boundary=synthetic"} {
		router := New(zap.NewNop(), nil)
		router.POST("/ordinary", func(c *gin.Context) {
			if _, err := io.Copy(io.Discard, c.Request.Body); err != nil {
				c.Status(400)
				return
			}
			c.Status(204)
		})
		req := httptest.NewRequest("POST", "/ordinary", strings.NewReader(strings.Repeat("x", 1<<20+1)))
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 400 {
			t.Fatal("普通请求绕过上限", contentType)
		}
	}
}

// TestStudentCardRegistrationBudget 拒绝没有明确预算或超过支持范围的上传装配。
func TestStudentCardRegistrationBudget(t *testing.T) {
	for _, limit := range []int64{0, -1, 8<<20 + 1} {
		if RegisterStudentCards(New(zap.NewNop(), nil), &cardsFixture{}, limit) == nil {
			t.Fatal("接受无效预算", limit)
		}
	}
}
