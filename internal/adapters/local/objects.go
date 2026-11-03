// Package local 实现本地开发供应商，不能证明正式身份、外部审核或灾备能力。
// 应用宿主负责环境准入；本包不读取配置、不创建数据库、不装配业务服务。
package local

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

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// IdentityKeys 只接受固定开发引用，包裹密钥和身份检索密钥分别保存。
type IdentityKeys struct {
	WrapKey, LookupKey []byte
	Logger             *zap.Logger
}

func completed(log *zap.Logger, operation string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	log.Info(
		"开发资源操作完成",
		zap.String("operation", operation),
		zap.String("result", result),
		zap.Duration("duration", time.Since(start)),
	)
}

// Wrap 使用固定开发用途封装数据密钥，随机nonce不改变持久引用。
func (k *IdentityKeys) Wrap(ctx context.Context, key []byte) (ref string, out []byte, err error) {
	defer completed(k.Logger, "wrap", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	block, err := aes.NewCipher(k.WrapKey)
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

// Unwrap 只解包固定开发引用，错误引用或认证失败不会返回明文。
func (k *IdentityKeys) Unwrap(ctx context.Context, ref string, data []byte) (out []byte, err error) {
	defer completed(k.Logger, "unwrap", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if ref != "local-wrap-v1" || len(data) < 28 {
		return nil, command.ErrUnavailable
	}
	block, err := aes.NewCipher(k.WrapKey)
	if err != nil {
		return
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return
	}
	return gcm.Open(nil, data[:12], data[12:], []byte(ref))
}

// Digest 按身份用途隔离检索摘要，开发引用不得替代正式KMS。
func (k *IdentityKeys) Digest(ctx context.Context, ref, purpose string, data []byte) (out []byte, err error) {
	defer completed(k.Logger, "digest", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if ref != "local-lookup-v1" || (purpose != "openid" && purpose != "student_number") {
		return nil, command.ErrUnavailable
	}
	return securedata.LookupDigest(k.LookupKey, purpose, string(data))
}

// Objects 用密文摘要命名不可变版本，随机业务键经哈希后不能形成路径穿越。
// 同目录临时文件写完并同步后才以硬链接发布，读者永远看不到半写文件。
type Objects struct {
	Root   string
	Logger *zap.Logger
}

func (o *Objects) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(o.Root, hex.EncodeToString(sum[:]))
}

// PutOnce 用临时文件、同步和原子硬链接发布密文；同键不同内容拒绝覆盖。
func (o *Objects) PutOnce(ctx context.Context, key string, data []byte) (version string, err error) {
	defer completed(o.Logger, "object_put", time.Now(), &err)
	if err = ctx.Err(); err != nil {
		return
	}
	if key == "" {
		return "", command.ErrInvalid
	}
	sum := sha256.Sum256(data)
	version = hex.EncodeToString(sum[:])
	file, err := os.CreateTemp(o.Root, ".pending-")
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
	directory, err := os.Open(o.Root)
	if err != nil {
		return "", command.ErrUnavailable
	}
	defer directory.Close()
	if err = directory.Sync(); err != nil {
		return "", command.ErrUnavailable
	}
	return version, nil
}

// Open 只打开摘要匹配的不可变密文版本；返回文件由调用方关闭。
func (o *Objects) Open(ctx context.Context, key, version string) (out io.ReadCloser, err error) {
	defer completed(o.Logger, "object_open", time.Now(), &err)
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

// Unavailable 明确表示尚未配置供应商，不制造微信身份、学校规则或图片审核成功。
type Unavailable struct{ Logger *zap.Logger }

// Exchange 明确返回微信适配未配置，不制造登录身份。
func (u Unavailable) Exchange(context.Context, string) (out []byte, err error) {
	defer completed(u.Logger, "wechat_unconfigured", time.Now(), &err)
	return nil, command.ErrUnavailable
}

// Scan 在缺少供应商时拒绝给出图片审核通过结论。
func (u Unavailable) Scan(context.Context, []byte, string) (ok bool, err error) {
	defer completed(u.Logger, "scan_unconfigured", time.Now(), &err)
	return false, command.ErrUnavailable
}

// Validate 在缺少权威学校规则时保持不可用。
func (u Unavailable) Validate(context.Context, string, string) (err error) {
	defer completed(u.Logger, "school_rules_unconfigured", time.Now(), &err)
	return command.ErrUnavailable
}
