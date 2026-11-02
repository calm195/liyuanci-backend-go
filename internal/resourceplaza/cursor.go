package resourceplaza

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// DeriveKey 将根密钥隔离到资料游标用途。
// 用途标签属于兼容协议，整理文件时不能替换或复用为其他模块密钥。
func DeriveKey(root []byte) []byte {
	m := hmac.New(sha256.New, root)
	_, _ = m.Write([]byte("resource-plaza.cursor.v1"))
	return m.Sum(nil)
}

type cursor struct {
	Binding, ID string
	At, Expires time.Time
	Sort        int
}

func (s *Service) aead() (cipher.AEAD, error) {
	b, e := aes.NewCipher(s.cfg.Key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(b)
}

// binding 将游标限定于当前主体、身份类型、操作和筛选条件，游标本身不参与绑定。
func binding(a actor, op string, q Query) string {
	q.Cursor = ""
	b, _ := json.Marshal([]any{a.ID, a.Admin, op, q})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// after 同时验证密文、绑定和有效期；AAD与next保持兼容，分页不得跨主体重放。
func (s *Service) after(a actor, op string, q Query) (c cursor, err error) {
	if q.Cursor == "" {
		return
	}
	g, e := s.aead()
	if e != nil {
		return c, e
	}
	b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
	if e != nil || len(b) < g.NonceSize()+g.Overhead() || len(q.Cursor) > 2048 {
		return c, command.ErrInvalid
	}
	raw, e := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte("resource-plaza/1"))
	if e != nil {
		return c, command.ErrInvalid
	}
	defer clear(raw)
	if json.Unmarshal(raw, &c) != nil || c.Binding != binding(a, op, q) || !c.Expires.After(a.Now) || !uuidPattern.MatchString(c.ID) {
		return cursor{}, command.ErrInvalid
	}
	return
}

func (s *Service) next(a actor, op string, q Query, c cursor) (*string, error) {
	c.Binding = binding(a, op, q)
	c.Expires = a.Now.Add(30 * time.Minute)
	raw, e := json.Marshal(c)
	if e != nil {
		return nil, e
	}
	defer clear(raw)
	g, e := s.aead()
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	v := base64.RawURLEncoding.EncodeToString(g.Seal(nonce, nonce, raw, []byte("resource-plaza/1")))
	return &v, nil
}
