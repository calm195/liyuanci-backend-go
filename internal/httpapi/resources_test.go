package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/calm195/liyuanci-backend-go/internal/resourceplaza"
	"go.uber.org/zap"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type resourceProbe struct{ calls int }

func (p *resourceProbe) Read(context.Context, string, string, string, string, resourceplaza.Query, bool) (any, error) {
	p.calls++
	return map[string]any{}, nil
}
func (p *resourceProbe) Write(context.Context, string, string, string, string, string, resourceplaza.Input) (any, error) {
	p.calls++
	return map[string]any{}, nil
}

// TestResourceProtocolVectors 将正式包全部原始协议向量送入真实Gin边界；服务探针仅验证是否允许进入领域层。
func TestResourceProtocolVectors(t *testing.T) {
	dir := os.Getenv("APP_RESOURCE_CONTRACT_DIR")
	if dir == "" {
		dir = "../../../docs/contracts/resource-plaza-v1"
	}
	b, e := os.ReadFile(filepath.Join(dir, "examples.json"))
	if e != nil {
		t.Skip("缺少共享契约")
	}
	type request struct {
		Operation string            `json:"operation_id"`
		Path      map[string]string `json:"path_params"`
		Query     map[string]any    `json:"query"`
		Pairs     [][]string        `json:"query_pairs"`
		Headers   map[string]string `json:"headers"`
		Body      json.RawMessage   `json:"request_body"`
		Raw       *string           `json:"raw_body"`
	}
	var doc struct {
		Vectors []struct {
			Name    string
			Valid   bool
			Request request
		} `json:"protocol_vectors"`
	}
	if e = json.Unmarshal(b, &doc); e != nil {
		t.Fatal(e)
	}
	b, e = os.ReadFile(filepath.Join(dir, "openapi.json"))
	if e != nil {
		t.Fatal(e)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			ID string `json:"operationId"`
		}
	}
	if e = json.Unmarshal(b, &spec); e != nil {
		t.Fatal(e)
	}
	type route struct{ method, path string }
	routes := map[string]route{}
	for path, methods := range spec.Paths {
		for method, op := range methods {
			routes[op.ID] = route{strings.ToUpper(method), path}
		}
	}
	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			p := &resourceProbe{}
			r := New(zap.NewNop(), nil)
			if e = RegisterResources(r, p, p); e != nil {
				t.Fatal(e)
			}
			rt := routes[v.Request.Operation]
			path := rt.path
			for k, val := range v.Request.Path {
				path = strings.ReplaceAll(path, "{"+k+"}", val)
			}
			q := url.Values{}
			for k, val := range v.Request.Query {
				switch val := val.(type) {
				case string:
					q.Set(k, val)
				default:
					b, _ := json.Marshal(val)
					q.Set(k, string(b))
				}
			}
			if v.Request.Pairs != nil {
				q = url.Values{}
				for _, pair := range v.Request.Pairs {
					q.Add(pair[0], pair[1])
				}
			}
			body := v.Request.Body
			if string(body) == "null" {
				body = nil
			}
			if v.Request.Raw != nil {
				body = []byte(*v.Request.Raw)
			}
			req := httptest.NewRequest(rt.method, "/api/v1"+path+"?"+q.Encode(), bytes.NewReader(body))
			for k, val := range v.Request.Headers {
				req.Header.Set(k, val)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if (p.calls == 1) != v.Valid {
				t.Fatalf("准入不同: %d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("缺少no-store")
			}
		})
	}
	if len(doc.Vectors) != 31 {
		t.Fatal("向量数量不符")
	}
	for _, header := range []string{"Authorization", "Content-Type", "Idempotency-Key"} {
		t.Run("duplicate-"+header, func(t *testing.T) {
			p := &resourceProbe{}
			r := New(zap.NewNop(), nil)
			_ = RegisterResources(r, p, p)
			req := httptest.NewRequest("POST", "/api/v1/admin/resource-categories", strings.NewReader(`{"name":"分类"}`))
			req.Header.Set("Authorization", "Bearer synthetic")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "synthetic")
			req.Header.Add(header, req.Header.Get(header))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != 400 || p.calls != 0 {
				t.Fatal("重复头放行")
			}
		})
	}
}
