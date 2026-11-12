package contentinteraction

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestFavoriteCursorBinding 验证游标不暴露账号且不能跨主体、分类或篡改重用。
func TestFavoriteCursorBinding(t *testing.T) {
	s := &Service{key: bytes.Repeat([]byte{7}, 32)}
	actor := uuid.NewString()
	cur := s.cursor(favoriteCursor{Actor: s.actorBinding(actor), Kind: "wish", At: time.Now().UTC(), TargetKind: "wish", ID: uuid.NewString()})
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(cur, ".")[0])
	if bytes.Contains(raw, []byte(actor)) {
		t.Fatal("游标泄漏主体")
	}
	if _, e := s.decode(cur, actor, "wish"); e != nil {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{uuid.NewString(), "wish"}, {actor, "resource"}} {
		if _, e := s.decode(cur, pair[0], pair[1]); e == nil {
			t.Fatal("跨范围游标可用")
		}
	}
	if _, e := s.decode(cur+"x", actor, "wish"); e == nil {
		t.Fatal("篡改游标可用")
	}
}
