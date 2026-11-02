// Package securedata 提供敏感数据的信封加密，不负责业务授权或密钥持久化。
package securedata

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"
)

// KeyService 隔离密钥供应商；实现须在外部包裹数据密钥，禁止将主密钥存入业务库。
// Wrap 不得修改或保留输入切片；Unwrap 转交独立切片的所有权，调用方使用后清零。
// 实现须响应取消并校验允许的历史密钥引用，不从密文动态选择任意供应商。
type KeyService interface {
	Wrap(context.Context, []byte) (keyRef string, wrapped []byte, err error)
	Unwrap(context.Context, string, []byte) ([]byte, error)
}

// Binding 将密文绑定到用途、随机对象标识和内容版本，防止跨字段或对象替换。
type Binding struct {
	Purpose  string
	ObjectID string
	Version  uint64
}

// Envelope 仅含可持久化的密文与包裹密钥；不得附带明文数据密钥。
type Envelope struct {
	Algorithm  string
	KeyRef     string
	WrappedKey []byte
	Nonce      []byte
	Ciphertext []byte
}

// Codec 通过显式依赖使用密钥服务；调用方须先完成当前业务授权。
type Codec struct {
	keys   KeyService
	logger *zap.Logger
}

// New 构造失败关闭的加密器；不存在默认密钥或明文降级。
func New(keys KeyService, logger *zap.Logger) (*Codec, error) {
	if keys == nil || logger == nil {
		return nil, errors.New("必须注入密钥服务和日志器")
	}
	return &Codec{keys: keys, logger: logger}, nil
}

func aad(binding Binding) ([]byte, error) {
	if binding.Purpose == "" || binding.ObjectID == "" || binding.Version == 0 {
		return nil, errors.New("加密绑定无效")
	}
	// JSON 数组保留字段边界，避免简单拼接产生相同附加数据。
	return json.Marshal([]any{"AES-256-GCM/v1", binding.Purpose, binding.ObjectID, binding.Version})
}

func wipe(value []byte) { clear(value) }

func (c *Codec) completed(start time.Time, operation string, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	// 供应商错误可能含凭据，只记录结果，不输出错误原文或对象身份。
	c.logger.Info("敏感数据操作完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

// Encrypt 为每次写入生成独立数据密钥及 nonce；包裹失败时不返回可落库数据。
func (c *Codec) Encrypt(ctx context.Context, binding Binding, plaintext []byte) (out Envelope, err error) {
	defer c.completed(time.Now(), "encrypt", &err)
	additional, err := aad(binding)
	if err != nil {
		return Envelope{}, err
	}
	if err = ctx.Err(); err != nil {
		return Envelope{}, err
	}
	key := make([]byte, 32)
	defer wipe(key)
	if _, err = rand.Read(key); err != nil {
		return Envelope{}, errors.New("数据密钥生成失败")
	}
	ref, wrapped, err := c.keys.Wrap(ctx, key)
	if err != nil || ref == "" || len(wrapped) == 0 {
		return Envelope{}, errors.New("数据密钥包裹失败")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Envelope{}, errors.New("加密器初始化失败")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, errors.New("认证加密初始化失败")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return Envelope{}, errors.New("随机数生成失败")
	}
	out = Envelope{Algorithm: "AES-256-GCM/v1", KeyRef: ref, WrappedKey: append([]byte(nil), wrapped...), Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plaintext, additional)}
	if err = ctx.Err(); err != nil {
		return Envelope{}, err
	}
	return out, nil
}

// Decrypt 使用历史密钥引用读取密文；失钥、格式错误和绑定不匹配均拒绝读取。
func (c *Codec) Decrypt(ctx context.Context, binding Binding, envelope Envelope) (plaintext []byte, err error) {
	defer c.completed(time.Now(), "decrypt", &err)
	additional, err := aad(binding)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if envelope.Algorithm != "AES-256-GCM/v1" || envelope.KeyRef == "" || len(envelope.WrappedKey) == 0 || len(envelope.Nonce) != 12 || len(envelope.Ciphertext) < 16 {
		return nil, errors.New("密文格式无效")
	}
	key, err := c.keys.Unwrap(ctx, envelope.KeyRef, envelope.WrappedKey)
	defer wipe(key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("数据密钥解包失败")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("解密器初始化失败")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("认证解密初始化失败")
	}
	plaintext, err = gcm.Open(nil, envelope.Nonce, envelope.Ciphertext, additional)
	if err != nil {
		return nil, errors.New("密文认证失败")
	}
	if err = ctx.Err(); err != nil {
		wipe(plaintext)
		return nil, err
	}
	return plaintext, nil
}

// LookupDigest 使用独立检索密钥生成域隔离摘要；业务层须固定规范化规则并控制轮换唯一映射。
// 此纯计算函数不记录可用于关联身份的摘要，调用方负责所属操作的完成日志。
func LookupDigest(key []byte, purpose, value string) ([]byte, error) {
	if len(key) < 32 || purpose == "" || value == "" {
		return nil, errors.New("检索密钥或输入无效")
	}
	encoded, err := json.Marshal([]string{"lookup/v1", purpose, value})
	if err != nil {
		return nil, errors.New("检索输入编码失败")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return mac.Sum(nil), nil
}
