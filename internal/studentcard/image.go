package studentcard

import (
	"bytes"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// normalizeImage 先以字节/像素上限检查头部，再完整解码和重新编码，去除元数据及尾随载荷。
// 上限由装配方显式提供，不将测试参数固化为产品规则。
func normalizeImage(source io.Reader, mime string, maxBytes, maxPixels int64) (out []byte, err error) {
	if source == nil || (mime != "image/png" && mime != "image/jpeg") {
		return nil, command.ErrInvalid
	}
	raw, e := io.ReadAll(io.LimitReader(source, maxBytes+1))
	defer clear(raw)
	if e != nil || int64(len(raw)) > maxBytes {
		return nil, command.ErrInvalid
	}
	config, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width) > maxPixels/int64(config.Height) || "image/"+format != mime {
		return nil, command.ErrInvalid
	}
	decoded, actual, e := image.Decode(bytes.NewReader(raw))
	if e != nil || actual != format {
		return nil, command.ErrInvalid
	}
	var buffer bytes.Buffer
	// 统一无损编码已解码像素，避免再次JPEG量化损伤学生卡文字；只移除非像素元数据。
	e = png.Encode(&buffer, decoded)
	if e != nil || int64(buffer.Len()) > maxBytes {
		clear(buffer.Bytes())
		return nil, command.ErrInvalid
	}
	return buffer.Bytes(), nil
}
