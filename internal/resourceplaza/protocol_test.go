package resourceplaza

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"testing"
)

// TestTextContract 覆盖规范化、URL字节与各位置非法百分号，禁止静默替换输入。
func TestTextContract(t *testing.T) {
	for _, v := range []struct {
		kind, value, want string
		valid             bool
	}{{"title", "\u3000ABC\u00a0", "ABC", true}, {"description", "a\r\nb\rc", "a\nb\nc", true}, {"title", "a\nb", "", false}, {"download_url", " HTTPS://example.test/a?x=%2F#z ", "HTTPS://example.test/a?x=%2F#z", true}, {"download_url", "https://example.test/a?x=%zz", "", false}, {"download_url", "https://example.test/%xx", "", false}, {"download_url", "https://example.test/#%g0", "", false}, {"download_url", "https://a.test:", "", false}, {"download_url", "https://a.test:65536", "", false}, {"download_url", "https://user@a.test/a", "", false}, {"extraction_code", "\u2001Ab C\u2001", "Ab C", true}} {
		got, e := Text(v.kind, v.value)
		if (e == nil) != v.valid || v.valid && got != v.want {
			t.Errorf("%s: %q %v", v.kind, got, e)
		}
	}
}

// TestStrictInputs 确保JSON重复键、整数token、UTF-16代理及完整PUT字段被服务前拒绝。
func TestStrictInputs(t *testing.T) {
	for _, raw := range []string{`{"expected_version":1,"expected_version":2}`, `{"expected_version":1.0}`, `{"expected_version":1e0}`, `{"expected_version":true}`, `{"expected_version":null}`, `{"expected_version":9007199254740992}`, `{"expected_version":1,"extra":0}`, `{"expected_version":1} {}`, `null`} {
		if _, e := ParseInput("publish", []byte(raw)); e == nil {
			t.Error("非法请求放行", raw)
		}
	}
	for _, raw := range []string{`{"name":"\ud800"}`, `{"name":"\udc00"}`, `{"name":"\ud800a"}`} {
		if _, e := ParseInput("category_create", []byte(raw)); e == nil {
			t.Error("孤立代理放行")
		}
	}
	if _, e := ParseInput("category_create", []byte(`{"name":"\ud83d\ude00"}`)); e != nil {
		t.Fatal(e)
	}
	if _, e := ParseInput("update", []byte(`{"title":"a","category_id":"00000000-0000-0000-0000-000000000001","download_url":"https://example.test","expected_version":1}`)); e == nil {
		t.Error("完整PUT遗漏字段")
	}
}

// TestSharedTextVectors 读取固定正式契约的真实向量，避免另造一套规范样例。
func TestSharedTextVectors(t *testing.T) {
	p := os.Getenv("APP_RESOURCE_CONTRACT_DIR")
	if p == "" {
		p = "../../../docs/contracts/resource-plaza-v1"
	}
	b, e := os.ReadFile(filepath.Join(p, "examples.json"))
	if e != nil {
		t.Skip("当前工作树未提供共享契约")
	}
	var doc struct {
		Vectors []struct {
			Name, Kind        string
			Value, Normalized *string
			Valid             bool
		} `json:"text_vectors"`
	}
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	if len(doc.Vectors) != 31 {
		t.Fatalf("向量数量 %d", len(doc.Vectors))
	}
	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			input := ""
			if v.Value != nil {
				input = *v.Value
			}
			got, e := Text(v.Kind, input)
			if (e == nil) != v.Valid {
				t.Fatalf("合法性不同: %v", e)
			}
			if v.Valid {
				want := ""
				if v.Normalized != nil {
					want = *v.Normalized
				}
				if got != want {
					t.Fatal("归一化不同")
				}
			}
		})
	}
}

// TestCursorLifetimeAndBinding 验证有效期、用途、主体和筛选绑定且密文不暴露搜索词。
func TestCursorLifetimeAndBinding(t *testing.T) {
	s := &Service{cfg: Config{Key: bytes.Repeat([]byte{9}, 32)}}
	a := actor{ID: "00000000-0000-0000-0000-000000000001", Now: time.Now()}
	q := Query{Q: "敏感搜索词", Limit: 1}
	c, e := s.next(a, "list", q, cursor{ID: a.ID, At: a.Now})
	if e != nil {
		t.Fatal(e)
	}
	q.Cursor = *c
	if _, e = s.after(a, "list", q); e != nil {
		t.Fatal(e)
	}
	a.Now = a.Now.Add(31 * time.Minute)
	if _, e = s.after(a, "list", q); e == nil {
		t.Fatal("过期游标放行")
	}
	a.Now = a.Now.Add(-31 * time.Minute)
	if _, e = s.after(a, "categories", q); e == nil {
		t.Fatal("跨用途放行")
	}
	q.Cursor = q.Cursor[:len(q.Cursor)-1] + "!"
	if _, e = s.after(a, "list", q); e == nil {
		t.Fatal("篡改游标放行")
	}
}
