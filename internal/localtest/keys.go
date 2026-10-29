// Package localtest 提供显式本地业务联调适配，不实现正式外部审核或灾备能力。
package localtest

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// Keys 使用持久随机本机密钥，兼容既有开发信封引用；重启不替换历史密钥。
type Keys struct {
	Key    []byte
	Logger *zap.Logger
}

func done(log *zap.Logger, operation string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	log.Info("本地测试操作完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

// Wrap 为数据密钥增加随机nonce及认证包裹，不把主密钥写入数据库。
func (k Keys) Wrap(ctx context.Context, key []byte) (ref string, out []byte, err error) {
	defer done(k.Logger, "key_wrap", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	b, e := aes.NewCipher(k.Key)
	if e != nil {
		return "", nil, command.ErrInvalid
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", nil, e
	}
	n := make([]byte, g.NonceSize())
	if _, err = rand.Read(n); err != nil {
		return
	}
	ref = "local-wrap-v1"
	return ref, append(n, g.Seal(nil, n, key, []byte(ref))...), nil
}

// Unwrap 只读取本地已知引用，错误密钥或篡改信封不能解密。
func (k Keys) Unwrap(ctx context.Context, ref string, data []byte) (out []byte, err error) {
	defer done(k.Logger, "key_unwrap", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if ref != "local-wrap-v1" || len(data) < 28 {
		return nil, command.ErrUnavailable
	}
	b, e := aes.NewCipher(k.Key)
	if e != nil {
		return nil, command.ErrInvalid
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	return g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(ref))
}
