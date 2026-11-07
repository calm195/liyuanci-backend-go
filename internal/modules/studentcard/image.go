package studentcard

import (
	"errors"
	"fmt"
	"io"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/imageinput"
)

// ErrImageInputTooLarge 表示上传原图字节数超出配置上限。
var ErrImageInputTooLarge = fmt.Errorf("学生卡原图超过字节上限: %w", command.ErrInvalid)

// ErrImageNormalizedTooLarge 表示移除元数据并转为PNG后超出配置上限。
var ErrImageNormalizedTooLarge = fmt.Errorf("学生卡规范化图片超过字节上限: %w", command.ErrInvalid)

// ErrImagePixelsExceeded 表示图片像素总数超出配置上限。
var ErrImagePixelsExceeded = fmt.Errorf("学生卡图片超过像素上限: %w", command.ErrInvalid)

// imageValidation 只保存图片校验的技术元数据，不保存图片内容、文件名或解码错误原文。
type imageValidation struct {
	result          string
	inputBytes      int64
	inputTruncated  bool
	normalizedBytes int64
	width           int
	height          int
}

// normalizeImage 复用通用图片规范化并保留学生卡已有错误与日志分类。
func normalizeImage(source io.Reader, mime string, maxBytes, maxPixels int64) ([]byte, imageValidation, error) {
	data, result, err := imageinput.Normalize(source, mime, maxBytes, maxPixels)
	validation := imageValidation{
		result: result.Result, inputBytes: result.InputBytes, inputTruncated: result.InputTruncated,
		normalizedBytes: result.NormalizedBytes, width: result.Width, height: result.Height,
	}
	switch {
	case err == nil:
	case errors.Is(err, imageinput.ErrImageInputTooLarge):
		err = ErrImageInputTooLarge
	case errors.Is(err, imageinput.ErrImageNormalizedTooLarge):
		err = ErrImageNormalizedTooLarge
	case errors.Is(err, imageinput.ErrImagePixelsExceeded):
		err = ErrImagePixelsExceeded
	default:
		err = command.ErrInvalid
	}
	return data, validation, err
}
