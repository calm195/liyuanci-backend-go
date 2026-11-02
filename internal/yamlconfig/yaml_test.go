package yamlconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRoundTripPreservesSecretsAndDurations 防止格式迁移改变密钥、带特殊字符口令或纳秒精度。
func TestRoundTripPreservesSecretsAndDurations(t *testing.T) {
	type profile struct {
		Password string
		Key      []byte
		Timeout  time.Duration
	}
	original := profile{Password: "literal:$HOME:#:'\\value", Key: bytes.Repeat([]byte{79}, 32), Timeout: 1500000001}
	raw, err := Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var actual profile
	if err = Unmarshal(raw, &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Password != original.Password || !bytes.Equal(actual.Key, original.Key) || actual.Timeout != original.Timeout {
		t.Fatal("配置值在往返中改变")
	}
	if !bytes.Contains(raw, []byte("1.500000001s")) {
		t.Fatal("时长未携带可读单位")
	}
}

// TestStrictInputRejectsAmbiguity 拒绝旧格式、字段拼写错误及可能掩盖配置的重复/别名。
func TestStrictInputRejectsAmbiguity(t *testing.T) {
	for _, raw := range []string{
		"Key: first\nKey: second\n", "Key: first\nkey: second\n", "Unknown: sensitive-marker\n",
		"Key: first\n---\nKey: second\n", "Key: &secret value\nOther: *secret\n", "Timeout: 1000000000\n",
		"Key: [sensitive-marker]\n", "{\"Key\":\"old-json\"}", strings.Repeat("x", limit+1),
	} {
		var target struct {
			Key     string
			Timeout time.Duration
		}
		err := Unmarshal([]byte(raw), &target)
		if err == nil {
			t.Fatal("接受歧义输入")
		}
		if strings.Contains(err.Error(), "sensitive-marker") {
			t.Fatal("解析错误泄露配置内容")
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "old.json")
	if os.WriteFile(path, []byte("Key: value\n"), 0600) != nil {
		t.Fatal("写入失败")
	}
	var value struct{ Key string }
	if Read(path, &value) == nil {
		t.Fatal("接受非YAML扩展名")
	}
}

// TestViperLayersPreserveExplicitOverrides 防止Viper默认值、弱类型或数组合并改变环境覆盖的含义。
func TestViperLayersPreserveExplicitOverrides(t *testing.T) {
	type profile struct {
		Database struct {
			DSN              string
			MaxOpen, MaxIdle int
		}
		Names    []string
		Enabled  bool
		Password string
		Internal string `json:"-"`
	}
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "common.yaml"), filepath.Join(dir, "development.yaml")}
	inputs := []string{
		"database:\n  dsn: common\n  maxopen: 8\n  maxidle: 2\nnames: [one, two]\nenabled: true\npassword: common\n",
		"DATABASE:\n  DSN: 'literal-$TOKEN'\n  maxidle: 0\nnames: []\nenabled: false\npassword: ''\n",
	}
	for i, path := range paths {
		if err := os.WriteFile(path, []byte(inputs[i]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DATABASE_DSN", "environment-must-not-win")
	var result profile
	if err := ReadLayers(paths, &result); err != nil {
		t.Fatal(err)
	}
	if result.Database.DSN != "literal-$TOKEN" || result.Database.MaxOpen != 8 || result.Database.MaxIdle != 0 || len(result.Names) != 0 || result.Enabled || result.Password != "" {
		t.Fatal("环境覆盖的空值、零值或数组含义改变")
	}
	// 后一层不能掩盖前一层未知字段，空映射也不能被Viper忽略。
	for _, extra := range []string{"unknown: {}\n", "internal: forbidden\n", "database.dsn: forbidden\n"} {
		if err := os.WriteFile(paths[0], []byte(inputs[0]+extra), 0600); err != nil {
			t.Fatal(err)
		}
		if ReadLayers(paths, &result) == nil {
			t.Fatal("公共层非法字段被忽略")
		}
	}
}

// TestViperStrictTypes 拒绝Viper默认支持的弱类型转换，避免口令、时长或数值静默变更。
func TestViperStrictTypes(t *testing.T) {
	for _, raw := range []string{
		"password: 123456\n", "count: '3'\n", "count: 1.5\n", "timeout: 1000\n",
		"key: [1, 2]\n", "key: invalid-base64\n", "password: null\n", "unknown: {}\n",
		"password: &secret value\n", "password: *secret\n", "nested: {A: 1, a: 2}\n",
		"nested:\n  <<: {value: one}\n", `nested: {"a":&secret value, "b":*secret}` + "\n", "# comment\n{password: value}\n",
	} {
		var result struct {
			Password string
			Count    int
			Timeout  time.Duration
			Key      []byte
			Nested   map[string]any
		}
		if Unmarshal([]byte(raw), &result) == nil {
			t.Fatal("接受非法配置")
		}
	}
}

// TestViperLiteralSecretsAndPEM 保留引号、块文本与大整数，不把秘密内容误判为YAML结构。
func TestViperLiteralSecretsAndPEM(t *testing.T) {
	raw := "password: \"literal &anchor *alias <<: !tag $HOME # text\"\nca: |\n  ---\n  &anchor *alias <<: !tag\n  ...\nnumber: 18014398509481985\n"
	var result struct {
		Password, CA string
		Number       uint64
	}
	if err := Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.Password != "literal &anchor *alias <<: !tag $HOME # text" || result.CA != "---\n&anchor *alias <<: !tag\n...\n" || result.Number != 18014398509481985 {
		t.Fatal("字面值改变")
	}
	encoded, err := Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip struct {
		Password, CA string
		Number       uint64
	}
	if err := Unmarshal(encoded, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip != result {
		t.Fatal("Viper生成配置改变字面值或整数精度")
	}
}
