package imageinput

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
)

// Display 从已规范化的图片生成等比展示图，不放大小图，也不改变学生卡的原始处理策略。
// PNG 保留透明像素；普通照片使用 JPEG，避免原尺寸无损图片占用列表下载带宽。
func Display(source []byte, edge int, preservePNG bool) ([]byte, string, error) {
	if edge < 1 || edge > 1600 {
		return nil, "", ErrInvalid
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(source))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 16<<20 {
		return nil, "", ErrInvalid
	}
	pixels, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, "", ErrInvalid
	}
	w, h := config.Width, config.Height
	if longest := max(w, h); longest > edge {
		w, h = max(1, w*edge/longest), max(1, h*edge/longest)
		resized := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(resized, resized.Bounds(), pixels, pixels.Bounds(), draw.Src, nil)
		pixels = resized
	}
	opaque, ok := pixels.(interface{ Opaque() bool })
	var encoded bytes.Buffer
	media := "image/jpeg"
	if preservePNG || !ok || !opaque.Opaque() {
		media = "image/png"
		err = png.Encode(&encoded, pixels)
	} else {
		err = jpeg.Encode(&encoded, pixels, &jpeg.Options{Quality: 80})
	}
	if err != nil {
		return nil, "", ErrInvalid
	}
	if w == config.Width && h == config.Height && encoded.Len() >= len(source) {
		return bytes.Clone(source), "image/" + format, nil
	}
	return encoded.Bytes(), media, nil
}
