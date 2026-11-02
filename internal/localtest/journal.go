package localtest

import (
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"go.uber.org/zap"
)

// Journal 复用正式文件账本，保留本地测试调用方式。
type Journal = privacy.FileJournal

// NewJournal 不生成或替换已有密钥。
func NewJournal(path string, codec *securedata.Codec, log *zap.Logger) (*Journal, error) {
	return privacy.NewFileJournal(path, codec, log)
}
