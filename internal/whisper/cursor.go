package whisper

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

type cursor struct {
	Scope string    `json:"scope"`
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
}

func keyed(key []byte, purpose string, value []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(purpose + "\x00"))
	mac.Write(value)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) scope(kind, actor string) string {
	// 游标正文也不能编码明文主体；用途隔离摘要不可跨公开／本人入口关联。
	b, _ := json.Marshal([]string{kind, actor})
	return keyed(s.config.CursorKey, "whisper.cursor.scope", b)
}

func (s *Service) encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b) + "." + keyed(s.config.CursorKey, "whisper.cursor.signature", b)
}

func (s *Service) decodeCursor(value, scope string) (c cursor, err error) {
	if value == "" {
		return c, nil
	}
	if len(value) > 4096 {
		return c, command.ErrInvalid
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return c, command.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return c, command.ErrInvalid
	}
	expected := keyed(s.config.CursorKey, "whisper.cursor.signature", b)
	if !hmac.Equal([]byte(expected), []byte(parts[1])) || json.Unmarshal(b, &c) != nil || c.Scope != scope || !ValidID(c.ID) || c.At.IsZero() {
		return cursor{}, command.ErrInvalid
	}
	return c, nil
}
