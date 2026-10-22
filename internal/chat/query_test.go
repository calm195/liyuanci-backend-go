package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// TestOriginalParticipants 验证终结匹配可读、陌生人不可读及注销意图即时禁读。
func TestOriginalParticipants(t *testing.T) {
	for _, tc := range []struct {
		name, actor, lifecycle string
		deleted, want          bool
	}{{"原许愿人终结后", "owner", "active", false, true}, {"原应愿人终结后", "helper", "active", false, true}, {"新应愿人", "new", "active", false, false}, {"注销意图", "owner", "deleting", false, false}, {"目标删除", "helper", "active", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := Snapshot{Match: entity.Match{RequesterID: "owner", HelperID: "helper", Status: "ended"}, Wish: entity.Wish{OwnerID: "owner"}, Requester: entity.Account{ID: "owner", Lifecycle: "active"}, Helper: entity.Account{ID: "helper", Lifecycle: tc.lifecycle}}
			if tc.deleted {
				now := time.Now()
				s.Wish.DraftDeletedAt = &now
			}
			if s.CanRead(tc.actor) != tc.want {
				t.Fatal("原参与者读取资格错误")
			}
		})
	}
}

// TestBoundCursor 验证签名、主体、用途及会话绑定，避免串读增量位置。
func TestBoundCursor(t *testing.T) {
	s := &Service{cfg: Config{Key: []byte(strings.Repeat("k", 32))}}
	scope := "messages/actor/conversation"
	token := s.encode(cursor{Scope: scope, Seq: 2})
	c, e := s.decode(token, scope)
	if e != nil || c.Seq != 2 {
		t.Fatal(c, e)
	}
	for _, tc := range []struct{ token, scope string }{{token, "messages/other/conversation"}, {token, "messages/actor/other"}, {token, "notifications/actor"}, {token + "x", scope}, {strings.Repeat("x", 2049), scope}, {s.encode(cursor{Scope: scope, Seq: -1}), scope}} {
		if _, e = s.decode(tc.token, tc.scope); e != command.ErrInvalid {
			t.Fatal("无效游标未拒绝", e)
		}
	}
}
