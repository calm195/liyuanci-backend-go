package studentcard

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
)

type imageLogSessions struct{}

func (imageLogSessions) Current(context.Context, string, string) (studentauth.Me, error) {
	return studentauth.Me{}, nil
}

type failedImageReader struct{}

func (failedImageReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic private reader detail")
}

// TestImageValidationLogs 验证每种可复现拒绝分支的诊断字段及完成日志，不外泄令牌或图片内容。
func TestImageValidationLogs(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	jpegData := syntheticPhotoJPEG(t)
	_, normalized, err := normalizeImage(bytes.NewReader(jpegData), "image/jpeg", 1<<20, 1<<20)
	if err != nil || normalized.normalizedBytes <= int64(len(jpegData)) {
		t.Fatal("合成JPEG必须能复现PNG编码后膨胀", err, normalized)
	}
	for _, tc := range []struct {
		name, mime, result  string
		reader              func() io.Reader
		maxBytes, maxPixels int64
	}{
		{"原始大小", "image/png", "input_bytes_exceeded", func() io.Reader { return bytes.NewReader(pngData.Bytes()) }, 1, 10},
		{"像素上限", "image/png", "pixels_exceeded", func() io.Reader { return bytes.NewReader(pngData.Bytes()) }, 4096, 1},
		{"声明类型", "image/jpeg", "media_type_mismatch", func() io.Reader { return bytes.NewReader(pngData.Bytes()) }, 4096, 10},
		{"不支持类型", "image/gif", "invalid_source_or_media_type", func() io.Reader { return bytes.NewReader(pngData.Bytes()) }, 4096, 10},
		{"读取失败", "image/png", "read_failed", func() io.Reader { return failedImageReader{} }, 4096, 10},
		{"头部损坏", "image/png", "header_decode_failed", func() io.Reader { return bytes.NewReader([]byte("synthetic private image")) }, 4096, 10},
		{"像素损坏", "image/png", "image_decode_failed", func() io.Reader { return bytes.NewReader(pngData.Bytes()[:33]) }, 4096, 10},
		{"编码后大小", "image/jpeg", "normalized_bytes_exceeded", func() io.Reader { return bytes.NewReader(jpegData) }, int64(len(jpegData)), 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			service := &Service{
				logger:   zap.New(core),
				sessions: imageLogSessions{},
				config: Config{
					MaxBytes:  tc.maxBytes,
					MaxPixels: tc.maxPixels,
					Command:   command.Config{Timeout: time.Second},
				},
				slots: make(chan struct{}, 1),
			}
			_, err := service.Upload(context.Background(), "synthetic-private-token", "synthetic-key", "synthetic_request", tc.mime, tc.reader())
			if !errors.Is(err, command.ErrInvalid) {
				t.Fatal("校验错误分类改变", err)
			}
			entries := logs.All()
			if len(entries) != 1 || entries[0].Message != "学生卡操作完成" {
				t.Fatal("应保留唯一完成日志", entries)
			}
			fields := entries[0].ContextMap()
			if fields["image_validation_result"] != tc.result || fields["result"] != "failure" || fields["request_id"] != "synthetic_request" {
				t.Fatal("诊断原因或关联编号错误", fields)
			}
			allowed := map[string]bool{
				"operation": true, "request_id": true, "result": true, "duration": true,
				"image_validation_result": true, "input_bytes": true, "input_bytes_truncated": true,
				"normalized_bytes": true, "image_width": true, "image_height": true,
				"max_bytes": true, "max_pixels": true,
			}
			for key := range fields {
				if !allowed[key] {
					t.Fatal("日志出现允许范围外字段", key)
				}
			}
			if tc.result == "input_bytes_exceeded" && (fields["input_bytes_truncated"] != true || fields["input_bytes"] != int64(2)) {
				t.Fatal("限量读取只能记录已读字节下界", fields)
			}
			if tc.result == "normalized_bytes_exceeded" {
				if fields["input_bytes"] != int64(len(jpegData)) || fields["normalized_bytes"] != normalized.normalizedBytes || fields["image_width"] != int64(256) || fields["image_height"] != int64(256) {
					t.Fatal("未正确记录原始大小、编码后大小及尺寸", fields)
				}
			}
		})
	}
}

// syntheticPhotoJPEG 生成确定性的合成像素，验证JPEG字节合规但转为PNG后超限的情况。
func syntheticPhotoJPEG(t *testing.T) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 256, 256))
	var state uint32 = 12345
	for i := 0; i < len(picture.Pix); i += 4 {
		for channel := 0; channel < 3; channel++ {
			state = state*1664525 + 1013904223
			picture.Pix[i+channel] = byte(state >> 24)
		}
		picture.Pix[i+3] = 255
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 35}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}
