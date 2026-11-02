package safety

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestUsersNormalization 覆盖REQ-022的Unicode空白、ASCII大小写、字面字符及长度边界。
func TestUsersNormalization(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"\u3000 LiFish%_\\' \u0085", "lifish%_\\'", true},
		{"\u200bA\ufeff", "\u200ba\ufeff", true},
		{"ÄＡA", "ÄＡa", true},
		{strings.Repeat("鲤", 50), strings.Repeat("鲤", 50), true},
		{strings.Repeat("鲤", 51), "", false},
		{"\xff", "", false},
	} {
		q, e := NormalizeUsersQuery(UsersQuery{Nickname: tc.input, Limit: 20})
		if (e == nil) != tc.valid || (e == nil && q.Nickname != tc.want) {
			t.Fatalf("规范化不符合边界: %v", e)
		}
	}
	id := uuid.NewString()
	if got, e := NormalizeUserID(strings.ToUpper(id)); e != nil || got != id {
		t.Fatal("标准UUID大小写未规范化")
	}
	for _, v := range []string{"", strings.ReplaceAll(id, "-", ""), "urn:uuid:" + id, "{" + id + "}"} {
		if _, e := NormalizeUserID(v); e == nil {
			t.Fatal("非标准UUID通过")
		}
	}
}

// TestUsersCursorProtection 验证用途、主体、筛选、目标、页大小、密钥及绝对过期边界。
func TestUsersCursorProtection(t *testing.T) {
	s := &Service{cfg: Config{Key: []byte(strings.Repeat("k", 32))}}
	now := time.Now().UTC()
	id := uuid.NewString()
	q := UsersQuery{Nickname: "private-keyword", Limit: 20}
	bind := s.usersBinding("actor", "users", "", q)
	c, e := s.usersNext(bind, now, id, now)
	if e != nil {
		t.Fatal(e)
	}
	if got, e := s.usersAfter(bind, *c, now.Add(29*time.Minute)); e != nil || got.ID != id {
		t.Fatal("合法游标失败", e)
	}
	if _, e := s.usersAfter(bind, *c, now.Add(30*time.Minute)); e == nil {
		t.Fatal("到期游标通过")
	}
	for _, b := range []string{s.usersBinding("other", "users", "", q), s.usersBinding("actor", "penalties", id, q), s.usersBinding("actor", "users", id, q), s.usersBinding("actor", "users", "", UsersQuery{Nickname: q.Nickname, Limit: 1}), s.usersBinding("actor", "users", "", UsersQuery{Nickname: "other", Limit: 20})} {
		if _, e := s.usersAfter(b, *c, now); e == nil {
			t.Fatal("错配游标通过")
		}
	}
	raw, _ := base64.RawURLEncoding.DecodeString(*c)
	if strings.Contains(string(raw), q.Nickname) || strings.Contains(string(raw), id) {
		t.Fatal("游标泄漏可解码标识")
	}
	raw[len(raw)-1] ^= 1
	for _, bad := range []string{base64.RawURLEncoding.EncodeToString(raw), "synthetic", strings.Repeat("a", 4097)} {
		if _, e := s.usersAfter(bind, bad, now); e == nil {
			t.Fatal("损坏游标通过")
		}
	}
	other := &Service{cfg: Config{Key: []byte(strings.Repeat("x", 32))}}
	if _, e := other.usersAfter(bind, *c, now); e == nil {
		t.Fatal("跨密钥游标通过")
	}
}

// TestUserPenaltyBoundary 覆盖到期即失效、撤销优先及固定安全文案，不依赖worker补写。
func TestUserPenaltyBoundary(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		ends, revoked *time.Time
		status        string
	}{{nil, nil, "active"}, {&now, nil, "expired"}, {&now, &now, "revoked"}} {
		p := userPenaltyRow{ID: uuid.NewString(), Level: "general", Kind: "mute", StartsAt: now.Add(-7 * 24 * time.Hour), EndsAt: tc.ends, RevokedAt: tc.revoked}
		out := p.projection(now)
		if out.Status != tc.status || out.Reason == nil || *out.Reason != "一般违规处置" {
			t.Fatal("处罚边界投影错误")
		}
	}
}
