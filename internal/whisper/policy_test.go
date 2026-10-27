package whisper

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestTextAndIdentityBounds 覆盖码点、空白、不规范 UTF-8 与互斥档位。
func TestTextAndIdentityBounds(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"上限", strings.Repeat("鲤", 500), true}, {"超限", strings.Repeat("鲤", 501), false}, {"保留空白", "  树洞 \n", true}, {"纯空白", "\u3000\n", false}, {"非法编码", string([]byte{255}), false}, {"空字符", "树\x00洞", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if validText(tc.text, 1, 500) != tc.valid {
				t.Fatal("文字边界不一致")
			}
		})
	}
	v := int64(1)
	for _, i := range []IdentityInput{{Mode: "named"}, {Mode: "semi", DictionaryVersion: &v}, {Mode: "anonymous", NicknameVersion: &v}, {Mode: "other"}} {
		if identityShape(i) {
			t.Fatal("接受互斥档位无效组合")
		}
	}
}

// TestCursorIsolation 验证篡改、跨主体与跨入口拒绝，游标正文无账号明文。
func TestCursorIsolation(t *testing.T) {
	s := &Service{config: Config{CursorKey: bytes.Repeat([]byte{7}, 32)}}
	owner := uuid.NewString()
	scope := s.scope("own", owner)
	value := s.encodeCursor(cursor{Scope: scope, ID: uuid.NewString(), At: time.Now().UTC()})
	raw, e := base64.RawURLEncoding.DecodeString(strings.Split(value, ".")[0])
	if e != nil || bytes.Contains(raw, []byte(owner)) {
		t.Fatal("游标泄漏主体")
	}
	var c cursor
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("游标格式无效")
	}
	if _, e = s.decodeCursor(value, scope); e != nil {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{value, s.scope("own", uuid.NewString())}, {value, s.scope("public", owner)}, {value + "x", scope}, {strings.Repeat("x", 4097), scope}} {
		if _, e = s.decodeCursor(pair[0], pair[1]); e == nil {
			t.Fatal("游标隔离失效")
		}
	}
}

// TestMissingCatalogResources 验证缺失资料返回 unavailable，不虚构联系渠道。
func TestMissingCatalogResources(t *testing.T) {
	c, e := normalizeCatalog(CatalogSnapshot{})
	if e != nil || c.Grades.Version != nil || len(c.Grades.Items) != 0 || c.Help.Status != "unavailable" || c.Help.Items == nil {
		t.Fatal("缺资料未安全降级", e)
	}
	for n := 0; n < 50; n++ {
		a, e := randomAlias()
		b, f := randomAlias()
		if e != nil || f != nil || a == b {
			t.Fatal("随机别名失败")
		}
	}
}
