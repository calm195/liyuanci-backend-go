package config

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/safety"
	"github.com/calm195/liyuanci-backend-go/internal/platform/yamlconfig"
)

// TestMasterKeyDerivation 验证重启/进程一致性、环境隔离及领域共用数据的兼容规则。
func TestMasterKeyDerivation(t *testing.T) {
	master := bytes.Repeat([]byte{7}, 32)
	load := func(environment, worker string) Config {
		t.Helper()
		path := fixture(
			t,
			"mode: foundation\n",
			"masterkey: "+base64.StdEncoding.EncodeToString(master)+"\nenvironment: "+environment+"\nworker:\n  kind: "+worker+"\nwhispers:\n  localtest: true\n",
		)
		c, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first, restarted := load("development", "card"), load("development", "account")
	if !reflect.DeepEqual(first.Development, restarted.Development) || !reflect.DeepEqual(first.Business, restarted.Business) ||
		!reflect.DeepEqual(first.Whispers, restarted.Whispers) {
		t.Fatal("不同进程派生结果不一致")
	}
	// 固定公开测试向量，防止用途标签重命名导致历史数据静默换钥。
	if hex.EncodeToString(first.Development.WrapKey) != "30ffb83a8137317b6646425f0d81417dda9032c47560d52d49ecae01f4059bfa" {
		t.Fatal("派生协议测试向量不符")
	}
	other := load("integration", "card")
	if bytes.Equal(first.Development.WrapKey, other.Development.WrapKey) {
		t.Fatal("环境未隔离")
	}
	master[0]++
	changed := load("development", "card")
	if bytes.Equal(first.Development.WrapKey, changed.Development.WrapKey) {
		t.Fatal("主密钥未影响派生值")
	}
	w, d := first.Whispers, first.Development
	if !bytes.Equal(w.WrapKey, d.WrapKey) || !bytes.Equal(w.RecoveryWrapKey, d.RecoveryWrapKey) ||
		!bytes.Equal(w.PrivacyKey, privacy.DeriveKey(d.WishKey)) ||
		!bytes.Equal(w.SafetyKey, safety.DeriveKey(d.WishKey)) {
		t.Fatal("组合应用未共享同领域密钥")
	}
	seen := map[string]bool{}
	for _, group := range []any{first.Development, first.Business, first.Business.Runtime} {
		v := reflect.ValueOf(group)
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).Type() != reflect.TypeOf([]byte{}) {
				continue
			}
			key := v.Field(i).Bytes()
			if len(key) == 0 {
				continue
			}
			if len(key) != 32 || bytes.Equal(key, first.MasterKey) || seen[string(key)] {
				t.Fatal("不同用途密钥未隔离")
			}
			seen[string(key)] = true
		}
	}
	if len(seen) != 25 {
		t.Fatal("遗漏领域密钥")
	}
	raw, err := yamlconfig.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wrapkey:", "sessionkey:", "receiptkey:", "privacykey:"} {
		if bytes.Contains(raw, []byte(name)) {
			t.Fatal("派生密钥被写回配置")
		}
	}
}

// TestMasterKeyValidation 拒绝缺失/无效主密钥及旧字段，错误中不能泄漏输入。
func TestMasterKeyValidation(t *testing.T) {
	base := "mode: development\ndatabase:\n  driver: postgres\n  host: 127.0.0.1\n  name: synthetic\n  user: application\n  password: synthetic\nredis:\n  enabled: true\n"
	for _, value := range []string{
		"",
		"not-base64",
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
		base64.StdEncoding.EncodeToString(make([]byte, 33)),
	} {
		_, err := Load(fixture(t, base, "masterkey: '"+value+"'\n"))
		if err == nil {
			t.Fatal("接受缺失或无效主密钥")
		}
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatal("错误泄漏密钥")
		}
	}
	// 每种原密钥字段均已成为只读派生值，包含空值也不能重新覆盖。
	for _, group := range []struct {
		name  string
		value any
	}{
		{"development", Development{}}, {"business", Business{}}, {"whispers", Whispers{}},
	} {
		typeOf := reflect.TypeOf(group.value)
		for i := 0; i < typeOf.NumField(); i++ {
			field := typeOf.Field(i)
			if field.Type != reflect.TypeOf([]byte{}) {
				continue
			}
			legacy := group.name + ":\n  " + strings.ToLower(field.Name) + ": ''\n"
			if _, err := Load(fixture(t, "mode: foundation\n", legacy)); err == nil {
				t.Fatal("接受旧密钥配置")
			}
			if group.name == "whispers" {
				if _, err := Load(fixture(t, "mode: foundation\n", "business:\n  runtime:\n    "+strings.ToLower(field.Name)+": ''\n")); err == nil {
					t.Fatal("正式运行块接受旧密钥配置")
				}
			}
		}
	}
	if _, err := Load(fixture(t, "mode: foundation\n", "environment: development\n")); err != nil {
		t.Fatal("健康模式错误要求密钥")
	}
}
