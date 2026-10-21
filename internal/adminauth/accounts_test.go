package adminauth

import (
	"strings"
	"testing"
)

// T008/REQ-018、019：游标签名绑定用途及主体，拒绝跨主体、篡改和非规范目标。
func TestAccountCursor(t *testing.T) {
	s := &Service{config: Config{ReceiptKey: []byte(strings.Repeat("synthetic", 4))}}
	actor := "00000000-0000-4000-8000-000000000001"
	after := "00000000-0000-4000-8000-000000000002"
	raw := s.encodeCursor(actor, after)
	if got, err := s.decodeCursor(raw, actor); err != nil || got != after {
		t.Fatal(got, err)
	}
	for _, tc := range []struct{ raw, actor string }{
		{raw, after}, {raw + "x", actor}, {"!", actor}, {strings.Repeat("a", 1025), actor},
		{s.encodeCursor(actor, "not-a-uuid"), actor},
	} {
		if _, err := s.decodeCursor(tc.raw, tc.actor); err != ErrInvalid {
			t.Fatal("无效游标未拒绝", err)
		}
	}
}
