package verification

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

func TestQueueCursor(t *testing.T) {
	// T011/REQ-002、018：队列游标不能跨后台主体、跨密钥或被客户端修改边界。
	s := &Service{cfg: Config{ReceiptKey: bytes.Repeat([]byte{17}, 32)}}
	actor := "11111111-1111-4111-8111-111111111111"
	id := "22222222-2222-4222-8222-222222222222"
	cursor := s.encodeCursor(actor, id)
	if got, e := s.decodeCursor(actor, cursor); e != nil || got != id {
		t.Fatal("合法游标无法读取", e)
	}
	for _, item := range []struct{ actor, cursor string }{{id, cursor}, {actor, cursor[:len(cursor)-2] + "AA"}, {actor, "invalid"}, {actor, strings.Repeat("x", 256)}} {
		if _, e := s.decodeCursor(item.actor, item.cursor); !errors.Is(e, command.ErrInvalid) {
			t.Fatal("非法游标未拒绝", e)
		}
	}
	other := &Service{cfg: Config{ReceiptKey: bytes.Repeat([]byte{18}, 32)}}
	if _, e := other.decodeCursor(actor, cursor); !errors.Is(e, command.ErrInvalid) {
		t.Fatal("密钥变更后游标仍生效")
	}
}
func TestReceiptDigest(t *testing.T) {
	// T011/REQ-018：材料摘要使用独立HMAC，避免低熵姓名和学号被数据库离线猜测。
	a := &Service{cfg: Config{ReceiptKey: bytes.Repeat([]byte{17}, 32)}}
	b := &Service{cfg: Config{ReceiptKey: bytes.Repeat([]byte{18}, 32)}}
	in := Input{Name: "合成姓名", StudentNumber: "SYNTHETIC_NUMBER"}
	digest := a.digest(in)
	if digest != a.digest(in) || digest == b.digest(in) || strings.Contains(digest, in.Name) {
		t.Fatal("回执材料摘要未正确隔离")
	}
	in.Name = "另一合成姓名"
	if digest == a.digest(in) {
		t.Fatal("不同材料产生相同回执")
	}
}
