// Package imageinput 规范化上传的 JPEG 与 PNG 图片。
package imageinput

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
)

// ErrImageInputTooLarge 表示上传原图字节数超出配置上限。
var ErrImageInputTooLarge = errors.New("原图超过字节上限")

// ErrImageNormalizedTooLarge 表示移除元数据并转为PNG后超出配置上限。
var ErrImageNormalizedTooLarge = errors.New("规范化图片超过字节上限")

// ErrImagePixelsExceeded 表示图片像素总数超出配置上限。
var ErrImagePixelsExceeded = errors.New("图片超过像素上限")

// ErrInvalid 表示无法验证输入图片。
var ErrInvalid = errors.New("图片无效")

// Validation 只保存图片校验的技术元数据，不保存图片内容、文件名或解码错误原文。
type Validation struct {
	Result          string
	InputBytes      int64
	InputTruncated  bool
	NormalizedBytes int64
	Width           int
	Height          int
}

// Normalize 先以字节/像素上限检查头部，再完整解码和重新编码，去除元数据及尾随载荷。
// 上限由装配方显式提供，不将测试参数固化为产品规则。
func Normalize(source io.Reader, mime string, maxBytes, maxPixels int64) (out []byte, validation Validation, err error) {
	validation.Result = "invalid_source_or_media_type"
	if source == nil || (mime != "image/png" && mime != "image/jpeg") {
		return nil, validation, ErrInvalid
	}
	validation.Result = "read_failed"
	raw, e := io.ReadAll(io.LimitReader(source, maxBytes+1))
	defer clear(raw)
	validation.InputBytes = int64(len(raw))
	validation.InputTruncated = validation.InputBytes > maxBytes
	if e != nil {
		return nil, validation, ErrInvalid
	}
	if validation.InputTruncated {
		validation.Result = "input_bytes_exceeded"
		return nil, validation, ErrImageInputTooLarge
	}
	validation.Result = "header_decode_failed"
	config, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil {
		return nil, validation, ErrInvalid
	}
	validation.Width, validation.Height = config.Width, config.Height
	validation.Result = "invalid_dimensions"
	if config.Width <= 0 || config.Height <= 0 {
		return nil, validation, ErrInvalid
	}
	validation.Result = "pixels_exceeded"
	if int64(config.Width) > maxPixels/int64(config.Height) {
		return nil, validation, ErrImagePixelsExceeded
	}
	validation.Result = "media_type_mismatch"
	if "image/"+format != mime {
		return nil, validation, ErrInvalid
	}
	validation.Result = "image_decode_failed"
	decoded, actual, e := image.Decode(bytes.NewReader(raw))
	if e != nil || actual != format {
		return nil, validation, ErrInvalid
	}
	var buffer bytes.Buffer
	// 统一无损编码已解码像素，避免再次JPEG量化损伤学生卡文字；只移除非像素元数据。
	validation.Result = "png_encode_failed"
	e = png.Encode(&buffer, decoded)
	validation.NormalizedBytes = int64(buffer.Len())
	if e != nil {
		clear(buffer.Bytes())
		return nil, validation, ErrInvalid
	}
	if validation.NormalizedBytes > maxBytes {
		validation.Result = "normalized_bytes_exceeded"
		clear(buffer.Bytes())
		return nil, validation, ErrImageNormalizedTooLarge
	}
	validation.Result = "accepted"
	return buffer.Bytes(), validation, nil
}
