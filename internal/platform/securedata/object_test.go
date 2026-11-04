package securedata

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type shortObjectWriter struct{}

func (shortObjectWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

type failingObjectWriter struct{}

func (failingObjectWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic-storage-secret")
}

type idleObjectReader struct{}

func (idleObjectReader) Read([]byte) (int, error) { return 0, nil }

func TestBoundedObjectEncryption(t *testing.T) {
	// T005/REQ-002、018：合成学生卡字节，不对接真实桶或图片安全，不定义产品大小门槛。
	core, observed := observer.New(zap.InfoLevel)
	keys := &testKeys{current: "synthetic-object-key", keys: make(map[string][]byte)}
	codec, err := New(keys, zap.New(core))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	binding := Binding{Purpose: "student_card", ObjectID: "synthetic-object", Version: 1}
	input := []byte("synthetic-private-image-bytes")
	envelope, err := codec.EncryptObject(ctx, binding, bytes.NewReader(input), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	var restored bytes.Buffer
	if err = codec.DecryptObject(ctx, binding, envelope, &restored, int64(len(input))); err != nil || !bytes.Equal(restored.Bytes(), input) {
		t.Fatal("对象往返失败", err)
	}
	// 整体认证通过之前绝不能向存储/响应输出任何前缀。
	damaged := envelope
	damaged.Ciphertext = append([]byte(nil), envelope.Ciphertext...)
	damaged.Ciphertext[len(damaged.Ciphertext)-1] ^= 1
	restored.Reset()
	if err = codec.DecryptObject(ctx, binding, damaged, &restored, 1024); err == nil || restored.Len() != 0 {
		t.Fatal("认证失败输出了明文")
	}
	if _, err = codec.EncryptObject(ctx, binding, bytes.NewReader(input), int64(len(input)-1)); err == nil {
		t.Fatal("超限输入未拒绝")
	}
	if err = codec.DecryptObject(ctx, binding, envelope, &restored, int64(len(input)-1)); err == nil || restored.Len() != 0 {
		t.Fatal("超限输出未拒绝")
	}
	if err = codec.DecryptObject(ctx, binding, envelope, shortObjectWriter{}, 1024); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("短写未报告失败")
	}
	if err = codec.DecryptObject(ctx, binding, envelope, failingObjectWriter{}, 1024); err == nil {
		t.Fatal("存储失败不能成功")
	}
	if _, err = codec.EncryptObject(ctx, binding, idleObjectReader{}, 1024); err == nil {
		t.Fatal("空转输入必须终止")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = codec.EncryptObject(canceled, binding, bytes.NewReader(input), 1024); err == nil {
		t.Fatal("取消输入不能继续")
	}
	if err = codec.DecryptObject(canceled, binding, envelope, &restored, 1024); err == nil || restored.Len() != 0 {
		t.Fatal("取消输出不能继续")
	}
	keys.fail = true
	if _, err = codec.EncryptObject(ctx, binding, bytes.NewReader(input), 1024); err == nil {
		t.Fatal("失钥必须拒绝")
	}
	if err = codec.DecryptObject(ctx, binding, envelope, &restored, 1024); err == nil || restored.Len() != 0 {
		t.Fatal("失钥不能输出明文")
	}
	for _, limit := range []int64{0, -1, 1<<63 - 1} {
		if _, err = codec.EncryptObject(ctx, binding, bytes.NewReader(input), limit); err == nil {
			t.Fatal("无效上限必须拒绝")
		}
	}
	if observed.FilterField(zap.String("operation", "object_decrypt")).FilterField(zap.String("result", "failure")).Len() == 0 {
		t.Fatal("对象失败缺少完成日志")
	}
	for _, entry := range observed.All() {
		for _, value := range entry.ContextMap() {
			if value == string(input) || value == "synthetic-storage-secret" {
				t.Fatal("对象完成日志泄漏输入")
			}
		}
	}
}
