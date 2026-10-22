// Package devapp 装配明确隔离的本地开发资源，禁止用于生产。
package devapp

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// localKeys 只接受固定开发引用，包裹密钥和身份检索密钥分别保存。
type localKeys struct {
	wrap, lookup []byte
	logger       *zap.Logger
}

func completed(log *zap.Logger, operation string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	log.Info("开发资源操作完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

func (k *localKeys) Wrap(ctx context.Context, key []byte) (ref string, out []byte, err error) {
	defer completed(k.logger, "wrap", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	block, err := aes.NewCipher(k.wrap)
	if err != nil {
		return
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return
	}
	return "local-wrap-v1", append(nonce, gcm.Seal(nil, nonce, key, []byte("local-wrap-v1"))...), nil
}

func (k *localKeys) Unwrap(ctx context.Context, ref string, data []byte) (out []byte, err error) {
	defer completed(k.logger, "unwrap", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if ref != "local-wrap-v1" || len(data) < 28 {
		return nil, command.ErrUnavailable
	}
	block, err := aes.NewCipher(k.wrap)
	if err != nil {
		return
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return
	}
	return gcm.Open(nil, data[:12], data[12:], []byte(ref))
}

func (k *localKeys) Digest(ctx context.Context, ref, purpose string, data []byte) (out []byte, err error) {
	defer completed(k.logger, "digest", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if ref != "local-lookup-v1" || (purpose != "openid" && purpose != "student_number") {
		return nil, command.ErrUnavailable
	}
	return securedata.LookupDigest(k.lookup, purpose, string(data))
}

// localObjects 用密文摘要命名不可变版本，随机业务键经哈希后不能形成路径穿越。
// 同目录临时文件写完并同步后才以硬链接发布，读者永远看不到半写文件。
type localObjects struct {
	root   string
	logger *zap.Logger
}

func (o *localObjects) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(o.root, hex.EncodeToString(sum[:]))
}

func (o *localObjects) PutOnce(ctx context.Context, key string, data []byte) (version string, err error) {
	defer completed(o.logger, "object_put", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if key == "" {
		return "", command.ErrInvalid
	}
	sum := sha256.Sum256(data)
	version = hex.EncodeToString(sum[:])
	file, err := os.CreateTemp(o.root, ".pending-")
	if err != nil {
		return "", command.ErrUnavailable
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return "", command.ErrUnavailable
	}
	if err = file.Sync(); err != nil {
		return "", command.ErrUnavailable
	}
	if err = file.Close(); err != nil {
		return "", command.ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = os.Link(file.Name(), o.path(key)); err != nil {
		if !os.IsExist(err) {
			return "", command.ErrUnavailable
		}
		old, failure := os.ReadFile(o.path(key))
		if failure != nil || !bytes.Equal(old, data) {
			return "", command.ErrConflict
		}
	}
	directory, err := os.Open(o.root)
	if err != nil {
		return "", command.ErrUnavailable
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return "", command.ErrUnavailable
	}
	return version, nil
}

func (o *localObjects) Open(ctx context.Context, key, version string) (out io.ReadCloser, err error) {
	defer completed(o.logger, "object_open", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if key == "" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(version) {
		return nil, command.ErrUnavailable
	}
	file, err := os.Open(o.path(key))
	if err != nil {
		return nil, command.ErrUnavailable
	}
	// 只把匹配的不可变密文版本交给领域层；领域层仍负责上限与认证解密。
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		file.Close()
		return nil, command.ErrUnavailable
	}
	if hex.EncodeToString(digest.Sum(nil)) != version {
		file.Close()
		return nil, command.ErrUnavailable
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, command.ErrUnavailable
	}
	return file, nil
}

// unavailable 明确表示尚未配置供应商，不制造微信身份、学校规则或图片审核成功。
type unavailable struct{ logger *zap.Logger }

func (u unavailable) Exchange(context.Context, string) (out []byte, err error) {
	defer completed(u.logger, "wechat_unconfigured", time.Now(), &err)
	return nil, command.ErrUnavailable
}

func (u unavailable) Scan(context.Context, []byte, string) (ok bool, err error) {
	defer completed(u.logger, "scan_unconfigured", time.Now(), &err)
	return false, command.ErrUnavailable
}

func (u unavailable) Validate(context.Context, string, string) (err error) {
	defer completed(u.logger, "school_rules_unconfigured", time.Now(), &err)
	return command.ErrUnavailable
}
