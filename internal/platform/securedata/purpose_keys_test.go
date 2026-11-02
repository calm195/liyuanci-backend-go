package securedata

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestPurposeKeyCompatibility 固定公开测试向量，防止包移动改变历史回执和安全索引密钥。
func TestPurposeKeyCompatibility(t *testing.T) {
	root := bytes.Repeat([]byte{7}, 32)
	for _, tc := range []struct {
		name   string
		derive func([]byte) []byte
		want   string
	}{
		{"privacy", PrivacyReceiptKey, "0691007a28ea5b882d031a839bdbd6ee4a592f0454b14eb090f42450d9755a1d"},
		{"safety", SafetyScopeKey, "73a79cd0f241661bccc3049a55eb070d4417bc61b848dff65cf6976b1ca8a153"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hex.EncodeToString(tc.derive(root)); got != tc.want {
				t.Fatal("持久密钥派生协议已改变")
			}
		})
	}
}
