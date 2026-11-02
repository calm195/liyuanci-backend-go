package adminauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// 摘要格式版本固定计算代价，拒绝让数据库中的自由参数触发无限内存分配。
// 口令不截断、不规范化；随机盐让相同初始口令在各账号中产生不同摘要。
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", ErrUnavailable
	}
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return "", ErrUnavailable
	}
	defer clear(key)
	return "scrypt1$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key), nil
}

func verifyPassword(encoded, password string) bool {
	fields := strings.Split(encoded, "$")
	if len(fields) != 3 || fields[0] != "scrypt1" {
		return false
	}
	salt, err := hex.DecodeString(fields[1])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := hex.DecodeString(fields[2])
	if err != nil || len(want) != 32 {
		return false
	}
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return false
	}
	defer clear(key)
	return hmac.Equal(key, want)
}

func keyedDigest(secret []byte, value string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}
