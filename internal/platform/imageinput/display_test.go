package imageinput

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"testing"
)

// TestDisplayPhotoBudget 用有细节的合成照片验证实际像素和传输体积缩减，不依赖业务图片。
func TestDisplayPhotoBudget(t *testing.T) {
	pixels := image.NewRGBA(image.Rect(0, 0, 1200, 900))
	random := rand.New(rand.NewSource(12))
	for i := 0; i < len(pixels.Pix); i += 4 {
		pixels.Pix[i], pixels.Pix[i+1], pixels.Pix[i+2], pixels.Pix[i+3] = uint8(random.Intn(256)), uint8(random.Intn(256)), uint8(random.Intn(256)), 255
	}
	var original bytes.Buffer
	if err := png.Encode(&original, pixels); err != nil {
		t.Fatal(err)
	}
	data, media, err := Display(original.Bytes(), 720, false)
	if err != nil || media != "image/jpeg" {
		t.Fatal(media, err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 720 || config.Height != 540 || len(data) >= original.Len()/3 {
		t.Fatalf("缩略图未缩减：%+v bytes=%d original=%d err=%v", config, len(data), original.Len(), err)
	}
	t.Logf("合成照片原图 %d 字节，720px 缩略图 %d 字节", original.Len(), len(data))
	avatar, media, err := Display(original.Bytes(), 256, true)
	config, _, decodeErr := image.DecodeConfig(bytes.NewReader(avatar))
	if err != nil || decodeErr != nil || media != "image/png" || config.Width != 256 || config.Height != 192 {
		t.Fatal("头像尺寸或编码错误", config, media, err, decodeErr)
	}
}

// TestDisplayPreservesTransparencyAndSmallImages 覆盖透明像素、不放大、错误输入及学生卡规范化保持原尺寸。
func TestDisplayPreservesTransparencyAndSmallImages(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(0, 0, 40, 30))
	pixels.Set(0, 0, color.NRGBA{R: 255, A: 100})
	var original bytes.Buffer
	_ = png.Encode(&original, pixels)
	data, media, err := Display(original.Bytes(), 720, false)
	decoded, _, decodeErr := image.Decode(bytes.NewReader(data))
	if err != nil || decodeErr != nil || media != "image/png" || decoded.Bounds() != pixels.Bounds() {
		t.Fatal(media, err, decodeErr)
	}
	if _, _, _, alpha := decoded.At(0, 0).RGBA(); alpha != 100*257 {
		t.Fatal("透明度改变", alpha)
	}
	_, validation, err := Normalize(bytes.NewReader(original.Bytes()), "image/png", 4<<20, 16<<20)
	if err != nil || validation.Width != 40 || validation.Height != 30 {
		t.Fatal("原始规范化策略改变", validation, err)
	}
	for _, input := range [][]byte{nil, []byte("invalid")} {
		if _, _, err := Display(input, 720, false); err == nil {
			t.Fatal("无效图片被接受")
		}
	}
	if _, _, err := Display(original.Bytes(), 9999, false); err == nil {
		t.Fatal("无界尺寸被接受")
	}
}
