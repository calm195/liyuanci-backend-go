package securedata

import (
	"crypto/hmac"
	"crypto/sha256"
)

// PrivacyReceiptKey 派生既有隐私回执密钥，供配置装配与隐私领域共同使用。
// privacy.receipt.v1属于持久协议，搬移实现不能修改标签、算法或输入主域。
func PrivacyReceiptKey(root []byte) []byte {
	h := hmac.New(sha256.New, root)
	_, _ = h.Write([]byte("privacy.receipt.v1"))
	return h.Sum(nil)
}

// SafetyScopeKey 派生既有安全用途密钥，保持本地组合API与安全领域的数据兼容。
// safety.scope.v1属于持久协议，不随目录名、Go包名或运行进程改变。
func SafetyScopeKey(root []byte) []byte {
	h := hmac.New(sha256.New, root)
	_, _ = h.Write([]byte("safety.scope.v1"))
	return h.Sum(nil)
}
