package whisper

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// PublicBlockSource 在同一授权事务中解析公开心愿或故事，不向客户端暴露账号。
type PublicBlockSource func(context.Context, *gorm.DB, time.Time, string, string, string, int64) (string, error)

// SetPublicBlockSource 由应用在开始监听前注入跨域读取边界。
func (s *Service) SetPublicBlockSource(resolve PublicBlockSource) { s.publicBlockSource = resolve }
