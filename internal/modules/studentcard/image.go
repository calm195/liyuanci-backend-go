package studentcard

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
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

// normalizeImage 先以字节/像素上限检查头部，再完整解码和重新编码，去除元数据及尾随载荷。
// 上限由装配方显式提供，不将测试参数固化为产品规则。
func normalizeImage(source io.Reader, mime string, maxBytes, maxPixels int64) (out []byte, validation imageValidation, err error) {
	validation.result = "invalid_source_or_media_type"
	if source == nil || (mime != "image/png" && mime != "image/jpeg") {
		return nil, validation, command.ErrInvalid
	}
	validation.result = "read_failed"
	raw, e := io.ReadAll(io.LimitReader(source, maxBytes+1))
	defer clear(raw)
	validation.inputBytes = int64(len(raw))
	validation.inputTruncated = validation.inputBytes > maxBytes
	if e != nil {
		return nil, validation, command.ErrInvalid
	}
	if validation.inputTruncated {
		validation.result = "input_bytes_exceeded"
		return nil, validation, ErrImageInputTooLarge
	}
	validation.result = "header_decode_failed"
	config, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil {
		return nil, validation, command.ErrInvalid
	}
	validation.width, validation.height = config.Width, config.Height
	validation.result = "invalid_dimensions"
	if config.Width <= 0 || config.Height <= 0 {
		return nil, validation, command.ErrInvalid
	}
	validation.result = "pixels_exceeded"
	if int64(config.Width) > maxPixels/int64(config.Height) {
		return nil, validation, ErrImagePixelsExceeded
	}
	validation.result = "media_type_mismatch"
	if "image/"+format != mime {
		return nil, validation, command.ErrInvalid
	}
	validation.result = "image_decode_failed"
	decoded, actual, e := image.Decode(bytes.NewReader(raw))
	if e != nil || actual != format {
		return nil, validation, command.ErrInvalid
	}
	var buffer bytes.Buffer
	// 统一无损编码已解码像素，避免再次JPEG量化损伤学生卡文字；只移除非像素元数据。
	validation.result = "png_encode_failed"
	e = png.Encode(&buffer, decoded)
	validation.normalizedBytes = int64(buffer.Len())
	if e != nil {
		clear(buffer.Bytes())
		return nil, validation, command.ErrInvalid
	}
	if validation.normalizedBytes > maxBytes {
		validation.result = "normalized_bytes_exceeded"
		clear(buffer.Bytes())
		return nil, validation, ErrImageNormalizedTooLarge
	}
	validation.result = "accepted"
	return buffer.Bytes(), validation, nil
}
