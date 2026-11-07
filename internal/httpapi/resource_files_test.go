package httpapi

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
)

type fileUploadProbe struct {
	resourceProbe
	bytes int
}

func (p *fileUploadProbe) UploadFile(_ context.Context, _, _, _, name string, r io.Reader) (*resourceplaza.FileInfo, error) {
	data, err := io.ReadAll(r)
	p.bytes = len(data)
	return &resourceplaza.FileInfo{Name: name, Size: int64(len(data))}, err
}

// TestResourceUploadBodyBudget 验证专属上传预算越过默认1MiB，超出20MiB仍在HTTP边界拒绝。
func TestResourceUploadBodyBudget(t *testing.T) {
	for _, size := range []int{2 << 20, resourceplaza.FileMaxBytes + 1} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "课程.pdf")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(bytes.Repeat([]byte("x"), size)); err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		probe := &fileUploadProbe{}
		r := New(zap.NewNop(), func(context.Context) error { return nil })
		if err = RegisterResources(r, probe, probe); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/v1/admin/resource-files", &body)
		req.Header.Set("Authorization", "Bearer synthetic")
		req.Header.Set("Idempotency-Key", "file")
		req.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if size <= resourceplaza.FileMaxBytes {
			if w.Code != 201 || probe.bytes != size {
				t.Fatalf("正常文件被默认预算截断：%d %d", w.Code, probe.bytes)
			}
		} else if w.Code != 413 || probe.bytes != 0 {
			t.Fatalf("超限文件未拒绝：%d", w.Code)
		}
	}
}

type avatarBodyProbe struct{ bytes int }

func (p *avatarBodyProbe) UploadAvatar(_ context.Context, _, _, _ string, _ int64, _ string, r io.Reader) (studentauth.Me, error) {
	b, e := io.ReadAll(r)
	p.bytes = len(b)
	return studentauth.Me{}, e
}
func (p *avatarBodyProbe) DeleteAvatar(context.Context, string, string, string, int64) (studentauth.Me, error) {
	return studentauth.Me{}, nil
}

// TestAvatarBodyBudget 防止外层默认1MiB限制截断允许的2MiB头像。
func TestAvatarBodyBudget(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "avatar.png")
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("x"), 1500000)...)
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.WriteField("expected_version", "1"); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	p := &avatarBodyProbe{}
	r := New(zap.NewNop(), func(context.Context) error { return nil })
	if err = RegisterAvatar(r, p); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/me/avatar", &body)
	req.Header.Set("Authorization", "Bearer synthetic")
	req.Header.Set("Idempotency-Key", "avatar")
	req.Header.Set("Content-Type", w.FormDataContentType())
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	if response.Code != 200 || p.bytes != len(data) {
		t.Fatalf("允许的头像被默认预算截断：%d %d", response.Code, p.bytes)
	}
}
