package securedata

import (
	"context"
	"errors"
	"io"
	"time"
)

// objectReader 在每次底层读取前检查取消；底层网络读取自身仍须遵循请求超时。
type objectReader struct {
	ctx    context.Context
	source io.Reader
	idle   int
}

func (r *objectReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.source.Read(p)
	if n == 0 && err == nil {
		r.idle++
		if r.idle >= 100 {
			return 0, io.ErrNoProgress
		}
	} else {
		r.idle = 0
	}
	return n, err
}

func validObjectLimit(limit int64) bool {
	// 为读入超限探测字节及认证标签保留空间，避免整数溢出。
	return limit > 0 && limit <= int64(int(^uint(0)>>1))-16
}

// EncryptObject 在显式字节上限内读取对象后进行认证加密，不使用明文临时文件。
// 本接口不决定产品上传大小或图片类型；超限在访问密钥服务前拒绝，调用方保有输入所有权。
func (c *Codec) EncryptObject(ctx context.Context, binding Binding, source io.Reader, maxBytes int64) (envelope Envelope, err error) {
	defer c.completed(time.Now(), "object_encrypt", &err)
	if source == nil || !validObjectLimit(maxBytes) {
		return Envelope{}, errors.New("对象输入或大小上限无效")
	}
	if _, err = aad(binding); err != nil {
		return Envelope{}, err
	}
	plaintext, err := io.ReadAll(io.LimitReader(&objectReader{ctx: ctx, source: source}, maxBytes+1))
	defer wipe(plaintext)
	if err != nil {
		return Envelope{}, errors.New("对象读取失败")
	}
	if int64(len(plaintext)) > maxBytes {
		return Envelope{}, errors.New("对象超过显式大小上限")
	}
	return c.Encrypt(ctx, binding, plaintext)
}

// DecryptObject 先验证整个对象的认证标签再写出明文，篡改或超限不能向下游输出前缀。
// 使用受控内存而非无界流式协议；不关闭调用方的 writer，写出失败不能当作对象完成。
func (c *Codec) DecryptObject(ctx context.Context, binding Binding, envelope Envelope, destination io.Writer, maxBytes int64) (err error) {
	defer c.completed(time.Now(), "object_decrypt", &err)
	if destination == nil || !validObjectLimit(maxBytes) {
		return errors.New("对象输出或大小上限无效")
	}
	if int64(len(envelope.Ciphertext)) > maxBytes+16 {
		return errors.New("对象超过显式大小上限")
	}
	plaintext, err := c.Decrypt(ctx, binding, envelope)
	defer wipe(plaintext)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	n, err := destination.Write(plaintext)
	if err != nil {
		return errors.New("对象输出失败")
	}
	if n != len(plaintext) {
		return io.ErrShortWrite
	}
	return nil
}
