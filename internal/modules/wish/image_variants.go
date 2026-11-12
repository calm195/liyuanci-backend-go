package wish

import (
	"context"
	"encoding/json"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/imageinput"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

func validImageVariant(variant string) bool {
	return variant == "" || variant == "thumbnail" || variant == "preview"
}

func imageCacheKey(row entity.WishImage, variant string) string {
	key, _ := json.Marshal([]string{row.ID, row.ObjectKey, row.ObjectVersion, variant})
	return string(key)
}

func (s *Service) readImageVariant(ctx context.Context, row entity.WishImage, variant string) ([]byte, error) {
	if variant == "" {
		return s.readImageObject(ctx, row)
	}
	key := imageCacheKey(row, variant)
	binding := securedata.Binding{Purpose: "wish_image_display", ObjectID: key, Version: 1}
	raw, err := s.imageReads.load(ctx, key, func() ([]byte, error) {
		// 限制并发解码的大图数量，避免滚动列表使服务端内存和CPU无界增长。
		select {
		case s.imageRenderSlots <- struct{}{}:
			defer func() { <-s.imageRenderSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		original, err := s.readImageObject(ctx, row)
		if err != nil {
			return nil, err
		}
		defer clear(original)
		edge := 720
		if variant == "preview" {
			edge = 1600
		}
		data, _, err := imageinput.Display(original, edge, false)
		if err != nil {
			return nil, command.ErrUnavailable
		}
		defer clear(data)
		envelope, err := s.codec.Encrypt(ctx, binding, data)
		if err != nil {
			return nil, command.ErrUnavailable
		}
		return json.Marshal(envelope)
	})
	defer clear(raw)
	if err != nil {
		return nil, err
	}
	var envelope securedata.Envelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, command.ErrUnavailable
	}
	data, err := s.codec.Decrypt(ctx, binding, envelope)
	if err != nil {
		return nil, command.ErrUnavailable
	}
	return data, nil
}
