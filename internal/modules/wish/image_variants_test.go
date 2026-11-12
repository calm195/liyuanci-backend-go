package wish

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// TestImageVariantsKeepOnlyCiphertext 验证展示图可还原、缓存不保存明文且对象版本改变后不会返回旧图。
func TestImageVariantsKeepOnlyCiphertext(t *testing.T) {
	ctx := context.Background()
	codec, _ := securedata.New(&testKeys{}, zap.NewNop())
	objects := &wishImageMemory{data: make(map[string][]byte)}
	service := &Service{
		codec:            codec,
		imageObjects:     objects,
		imageReads:       newImageCache(),
		imageRenderSlots: make(chan struct{}, 2),
	}
	row := entity.WishImage{ID: "synthetic", ObjectKey: "wish-images/synthetic"}
	var original bytes.Buffer
	if err := png.Encode(&original, image.NewRGBA(image.Rect(0, 0, 800, 400))); err != nil {
		t.Fatal(err)
	}
	envelope, err := codec.Encrypt(ctx, securedata.Binding{Purpose: "wish_image", ObjectID: row.ID, Version: 1}, original.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(envelope)
	row.ObjectVersion, err = objects.PutOnce(ctx, row.ObjectKey, raw)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		data, err := service.readImageVariant(ctx, row, "thumbnail")
		if err != nil {
			t.Fatal(err)
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width != 720 || config.Height != 360 {
			t.Fatal(config, err)
		}
		clear(data)
	}
	if objects.opens != 1 {
		t.Fatal("缓存未复用", objects.opens)
	}
	for _, element := range service.imageReads.entries {
		data := element.Value.(imageCacheEntry).data
		var stored securedata.Envelope
		if json.Unmarshal(data, &stored) != nil || stored.Algorithm != "AES-256-GCM/v1" || bytes.Contains(data, original.Bytes()) {
			t.Fatal("缓存中存在非信封数据")
		}
	}
	row.ObjectVersion = "changed"
	if _, err := service.readImageVariant(ctx, row, "thumbnail"); err == nil {
		t.Fatal("新版本错误复用旧图")
	}
}
