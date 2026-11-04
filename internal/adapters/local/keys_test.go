package local

import (
	"bytes"
	"context"
	"go.uber.org/zap"
	"testing"
)

// TestLocalKeys 验证重启兼容既有开发引用，错误密钥和篡改均拒绝。
func TestLocalKeys(t *testing.T) {
	ctx := context.Background()
	key := bytes.Repeat([]byte{9}, 32)
	k := Keys{key, zap.NewNop()}
	ref, wrapped, e := k.Wrap(ctx, []byte("测试数据密钥"))
	if e != nil {
		t.Fatal(e)
	}
	out, e := (Keys{key, zap.NewNop()}).Unwrap(ctx, ref, wrapped)
	if e != nil || string(out) != "测试数据密钥" {
		t.Fatal("重启解密失败", e)
	}
	if _, e = (Keys{bytes.Repeat([]byte{8}, 32), zap.NewNop()}).Unwrap(ctx, ref, wrapped); e == nil {
		t.Fatal("接受错误密钥")
	}
	wrapped[len(wrapped)-1] ^= 1
	if _, e = k.Unwrap(ctx, ref, wrapped); e == nil {
		t.Fatal("接受篡改")
	}
}
