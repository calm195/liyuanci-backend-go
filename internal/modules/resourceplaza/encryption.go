package resourceplaza

import (
	"context"
	"encoding/json"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

type download struct {
	URL  string  `json:"download_url"`
	Code *string `json:"extraction_code"`
}

// encrypt 用用途、对象和版本绑定密文；临时明文字节在返回前清除，不写入日志。
func (s *Service) encrypt(ctx context.Context, purpose, id string, version int64, value any) ([]byte, error) {
	b, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	defer clear(b)
	env, e := s.codec.Encrypt(ctx, securedata.Binding{Purpose: purpose, ObjectID: id, Version: uint64(version)}, b)
	if e != nil {
		return nil, e
	}
	return json.Marshal(env)
}

// download 使用内容版本解密；发布、下架等仅改变资源版本，不能破坏已有下载密文。
func (s *Service) download(ctx context.Context, r entity.ResourceMaterial) (d download, err error) {
	var env securedata.Envelope
	if json.Unmarshal(r.DownloadCipher, &env) != nil {
		return d, command.ErrUnavailable
	}
	b, e := s.codec.Decrypt(
		ctx,
		securedata.Binding{Purpose: "resource_download", ObjectID: r.ID, Version: uint64(r.ContentVersion)},
		env,
	)
	if e != nil {
		return d, e
	}
	defer clear(b)
	err = json.Unmarshal(b, &d)
	return
}
