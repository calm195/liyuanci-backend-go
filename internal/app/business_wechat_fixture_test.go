package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	businessFixtureAddress = "LIYUANCI_TEST_WECHAT_FIXTURE_ADDRESS"
	businessFixtureConfig  = "LIYUANCI_TEST_WECHAT_FIXTURE_CONFIG"
	businessFixtureToken   = "isolated-wechat-access-token"
	businessFixtureAuthor  = "isolated-wechat-code-author"
	businessFixturePeer    = "isolated-wechat-code-peer"
)

func fixtureOpenID(code string) string { return "openid-" + code }

type businessTextCheck struct {
	Version int    `json:"version"`
	Scene   int    `json:"scene"`
	OpenID  string `json:"openid"`
	Content string `json:"content"`
}

type businessWeChatFixture struct {
	*httptest.Server
	mu     sync.Mutex
	checks []businessTextCheck
}

func newBusinessWeChatFixture(t *testing.T, appID, secret string) *businessWeChatFixture {
	t.Helper()
	f := &businessWeChatFixture{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sns/jscode2session":
			q := r.URL.Query()
			code := q.Get("js_code")
			if q.Get("appid") != appID || q.Get("secret") != secret || q.Get("grant_type") != "authorization_code" ||
				(code != businessFixtureAuthor && code != businessFixturePeer) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			result = map[string]any{"openid": fixtureOpenID(code), "errcode": 0}
		case r.Method == http.MethodPost && r.URL.Path == "/cgi-bin/stable_token":
			var body struct {
				AppID     string `json:"appid"`
				Secret    string `json:"secret"`
				GrantType string `json:"grant_type"`
			}
			if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body) != nil ||
				body.AppID != appID || body.Secret != secret || body.GrantType != "client_credential" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			result = map[string]any{"access_token": businessFixtureToken, "expires_in": 7200}
		case r.Method == http.MethodPost && r.URL.Path == "/wxa/msg_sec_check":
			var body businessTextCheck
			if r.URL.Query().Get("access_token") != businessFixtureToken ||
				json.NewDecoder(io.LimitReader(r.Body, 32<<10)).Decode(&body) != nil ||
				body.Version != 2 || body.Scene < 1 || body.Scene > 4 || body.Content == "" ||
				(body.OpenID != fixtureOpenID(businessFixtureAuthor) && body.OpenID != fixtureOpenID(businessFixturePeer)) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.checks = append(f.checks, body)
			f.mu.Unlock()
			result = map[string]any{"errcode": 0, "result": map[string]string{"suggest": "pass"}}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *businessWeChatFixture) sawText(openID string, scene int, content string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, check := range f.checks {
		if check.OpenID == openID && check.Scene == scene && check.Content == content {
			return true
		}
	}
	return false
}

// TestBusinessWeChatFixtureServer 仅在隔离测试子进程里启动应用，并把微信请求交给回环夹具。
func TestBusinessWeChatFixtureServer(t *testing.T) {
	rawURL, configFile := os.Getenv(businessFixtureAddress), os.Getenv(businessFixtureConfig)
	if rawURL == "" || configFile == "" {
		t.Skip("仅供运行时专项的隔离子进程启动")
	}
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.User != nil {
		t.Fatal("微信夹具必须监听 IPv4 回环地址")
	}
	original := http.DefaultTransport
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: time.Second}).DialContext}
	defer transport.CloseIdleConnections()
	// 保留标准 Transport 类型，腾讯云适配器可以照常克隆连接池。
	isolated := original.(*http.Transport).Clone()
	isolated.RegisterProtocol("https", fixtureWeChatTransport{endpoint: endpoint, fixture: transport})
	http.DefaultTransport = isolated
	defer func() {
		isolated.CloseIdleConnections()
		http.DefaultTransport = original
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := RunServer(ctx, ServerOptions{ConfigFile: configFile}); err != nil {
		t.Fatal(err)
	}
}

type fixtureWeChatTransport struct {
	endpoint *url.URL
	fixture  http.RoundTripper
}

func (f fixtureWeChatTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.Host != "api.weixin.qq.com" {
		return nil, http.ErrSkipAltProtocol
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = f.endpoint.Scheme, f.endpoint.Host
	clone.Host = f.endpoint.Host
	return f.fixture.RoundTrip(clone)
}
