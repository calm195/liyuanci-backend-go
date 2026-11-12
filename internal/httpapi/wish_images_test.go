package httpapi

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

type wishImageBudgetSpy struct{ size int }

func (s *wishImageBudgetSpy) UploadImage(_ context.Context, _, _, _, _ string, r io.Reader) (wish.Image, error) {
	data, e := io.ReadAll(r)
	s.size = len(data)
	return wish.Image{ID: uuid.NewString(), ScanState: "approved"}, e
}
func (*wishImageBudgetSpy) ReadImage(context.Context, string, string, string, string) ([]byte, error) {
	return nil, command.ErrNotFound
}

// TestWishImageMultipartBudget 验证外层预算允许大于1MiB的合法PNG传到图片领域，超限仍拒绝。
func TestWishImageMultipartBudget(t *testing.T) {
	picture := image.NewNRGBA(image.Rect(0, 0, 800, 800))
	random := rand.New(rand.NewSource(19))
	if _, e := random.Read(picture.Pix); e != nil {
		t.Fatal(e)
	}
	var encoded bytes.Buffer
	if e := png.Encode(&encoded, picture); e != nil {
		t.Fatal(e)
	}
	if encoded.Len() <= 1<<20 || encoded.Len() > wish.ImageMaxBytes {
		t.Fatal("测试图片预算不正确", encoded.Len())
	}
	for _, size := range []int{encoded.Len(), wish.ImageMaxBytes + 1} {
		s := &wishImageBudgetSpy{}
		router := New(zap.NewNop(), func(context.Context) error { return nil })
		if e := RegisterWishImages(router, s); e != nil {
			t.Fatal(e)
		}
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		h := textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="sample.png"`}, "Content-Type": {"image/png"}}
		part, e := writer.CreatePart(h)
		if e != nil {
			t.Fatal(e)
		}
		if size == encoded.Len() {
			_, e = part.Write(encoded.Bytes())
		} else {
			_, e = part.Write(make([]byte, size))
		}
		if e != nil {
			t.Fatal(e)
		}
		if e = writer.Close(); e != nil {
			t.Fatal(e)
		}
		request := httptest.NewRequest("POST", "/api/v1/wish-images", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("Authorization", "Bearer synthetic")
		request.Header.Set("Idempotency-Key", uuid.NewString())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if size == encoded.Len() {
			if response.Code != 200 || s.size != size {
				t.Fatalf("合法大图被截断：status=%d size=%d", response.Code, s.size)
			}
		} else if response.Code != 400 || s.size != 0 {
			t.Fatalf("超限进入领域：status=%d size=%d", response.Code, s.size)
		}
	}
}
