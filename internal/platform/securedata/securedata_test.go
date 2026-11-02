package securedata

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// 测试密钥服务只用于 T005/REQ-002、007、018 的合成数据，不是生产适配器。
type testKeys struct {
	fail    bool
	current string
	keys    map[string][]byte
}

func (k *testKeys) Wrap(_ context.Context, key []byte) (string, []byte, error) {
	if k.fail {
		return "", nil, errors.New("secret-provider-token")
	}
	k.keys[k.current] = append([]byte(nil), key...)
	return k.current, []byte(k.current), nil
}
func (k *testKeys) Unwrap(_ context.Context, ref string, wrapped []byte) ([]byte, error) {
	if k.fail || string(wrapped) != ref || k.keys[ref] == nil {
		return nil, errors.New("secret-provider-token")
	}
	return append([]byte(nil), k.keys[ref]...), nil
}

func TestEnvelopeBindingAndFailures(t *testing.T) {
	logs, observed := observer.New(zap.InfoLevel)
	keys := &testKeys{current: "synthetic-old", keys: make(map[string][]byte)}
	codec, err := New(keys, zap.New(logs))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	binding := Binding{Purpose: "student_card", ObjectID: "synthetic-object", Version: 1}
	plain := []byte("synthetic-sensitive-payload")
	old, err := codec.Encrypt(ctx, binding, plain)
	if err != nil {
		t.Fatal(err)
	}
	keys.current = "synthetic-new"
	next, err := codec.Encrypt(ctx, binding, plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(old.Nonce, next.Nonce) || bytes.Equal(old.Ciphertext, next.Ciphertext) {
		t.Fatal("每次加密必须随机化")
	}
	for _, envelope := range []Envelope{old, next} {
		got, e := codec.Decrypt(ctx, binding, envelope)
		if e != nil || !bytes.Equal(got, plain) {
			t.Fatalf("历史/当前密钥读取失败：%v", e)
		}
	}
	for _, altered := range []Binding{{Purpose: "other", ObjectID: binding.ObjectID, Version: 1}, {Purpose: binding.Purpose, ObjectID: "other", Version: 1}, {Purpose: binding.Purpose, ObjectID: binding.ObjectID, Version: 2}} {
		if got, e := codec.Decrypt(ctx, altered, old); e == nil || got != nil {
			t.Fatal("绑定替换应拒绝")
		}
	}
	for _, mutate := range []func(*Envelope){
		func(e *Envelope) { e.Ciphertext = append([]byte(nil), e.Ciphertext...); e.Ciphertext[0] ^= 1 },
		func(e *Envelope) { e.Nonce = nil },
		func(e *Envelope) { e.Algorithm = "unknown" },
		func(e *Envelope) { e.WrappedKey = []byte("other") },
		func(e *Envelope) { e.KeyRef = "missing" },
	} {
		damaged := old
		mutate(&damaged)
		if got, e := codec.Decrypt(ctx, binding, damaged); e == nil || got != nil {
			t.Fatal("损坏密文应拒绝")
		}
	}
	keys.fail = true
	if got, e := codec.Encrypt(ctx, binding, plain); e == nil || got.Ciphertext != nil {
		t.Fatal("失钥不能返回可持久化密文")
	}
	if got, e := codec.Decrypt(ctx, binding, old); e == nil || got != nil {
		t.Fatal("失钥不能返回明文")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := codec.Encrypt(canceled, binding, plain); e == nil {
		t.Fatal("取消应终止操作")
	}
	if _, e := codec.Encrypt(ctx, Binding{}, plain); e == nil {
		t.Fatal("空绑定应拒绝")
	}
	for _, entry := range observed.All() {
		if entry.ContextMap()["duration"] == nil || entry.ContextMap()["result"] == nil {
			t.Fatal("缺少完成日志字段")
		}
		for _, value := range entry.ContextMap() {
			text, _ := value.(string)
			if strings.Contains(text, "sensitive") || strings.Contains(text, "token") || strings.Contains(text, binding.ObjectID) {
				t.Fatal("日志泄漏敏感输入")
			}
		}
	}
	if observed.FilterField(zap.String("result", "failure")).Len() == 0 {
		t.Fatal("缺少失败日志")
	}
}

func TestLookupDigestDomainSeparation(t *testing.T) {
	// T005/REQ-018：独立检索密钥和用途必须改变摘要；这里只验证算法，不冒称轮换唯一映射完成。
	key := bytes.Repeat([]byte{7}, 32)
	digest, err := LookupDigest(key, "student_number", "synthetic-001")
	if err != nil || len(digest) != 32 {
		t.Fatal(err)
	}
	again, _ := LookupDigest(key, "student_number", "synthetic-001")
	other, _ := LookupDigest(key, "openid", "synthetic-001")
	rotated, _ := LookupDigest(bytes.Repeat([]byte{8}, 32), "student_number", "synthetic-001")
	if !bytes.Equal(digest, again) || bytes.Equal(digest, other) || bytes.Equal(digest, rotated) {
		t.Fatal("检索摘要域隔离失败")
	}
	if _, err = LookupDigest(nil, "student_number", "synthetic-001"); err == nil {
		t.Fatal("失钥必须拒绝")
	}
	if _, err = New(nil, zap.NewNop()); err == nil {
		t.Fatal("缺少密钥服务应拒绝")
	}
}
