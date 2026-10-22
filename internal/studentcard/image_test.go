package studentcard

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// T011/REQ-002、015、018：有界解码、实际类型匹配及尾部清除，避免不可信图片越过入口。
func TestImageBoundaries(t *testing.T) {
	var encoded bytes.Buffer
	if e := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); e != nil {
		t.Fatal(e)
	}
	var jpegData bytes.Buffer
	if e := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); e != nil {
		t.Fatal(e)
	}
	if _, e := normalizeImage(bytes.NewReader(jpegData.Bytes()), "image/jpeg", 4096, 10); e != nil {
		t.Fatal("合法JPEG应接受", e)
	}
	withTail := append(bytes.Clone(encoded.Bytes()), []byte("SYNTHETIC_HIDDEN_DATA")...)
	normalized, e := normalizeImage(bytes.NewReader(withTail), "image/png", 4096, 10)
	if e != nil || bytes.Contains(normalized, []byte("SYNTHETIC_HIDDEN_DATA")) {
		t.Fatal("尾部载荷未清除", e)
	}
	for _, tc := range []struct {
		mime          string
		body          []byte
		limit, pixels int64
	}{
		{"image/jpeg", encoded.Bytes(), 4096, 10}, {"image/gif", encoded.Bytes(), 4096, 10},
		{"image/png", encoded.Bytes(), 4096, 1}, {"image/png", encoded.Bytes(), 1, 10},
		{"image/png", []byte("invalid"), 4096, 10}, {"image/png", encoded.Bytes()[:16], 4096, 10},
	} {
		if _, e := normalizeImage(bytes.NewReader(tc.body), tc.mime, tc.limit, tc.pixels); e != command.ErrInvalid {
			t.Fatal("无效图片未拒绝", tc.mime, e)
		}
	}
	if _, e := normalizeImage(strings.NewReader(""), "image/png", 4096, 10); e != command.ErrInvalid {
		t.Fatal(e)
	}
}
