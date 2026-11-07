package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cos "github.com/tencentyun/cos-go-sdk-v5"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func testTencent(t *testing.T, handler http.HandlerFunc) *Tencent {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := NewTencent(TencentConfig{
		Bucket: "private-test-1234567890", Region: "ap-shanghai", SecretID: "synthetic-id", SecretKey: "synthetic-key", BizType: "synthetic-biz", Timeout: time.Second, MaxObjectBytes: 1024,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	client.objects, client.images = server.URL, server.URL
	client.client.Transport.(*cos.AuthorizationTransport).Transport = server.Client().Transport
	t.Cleanup(client.Close)
	return client
}

// TestTencentObjects 验证原生签名、禁止覆盖、并发重放、摘要版本和读取缓冲清理。
func TestTencentObjects(t *testing.T) {
	var mu sync.Mutex
	var stored []byte
	client := testTencent(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "q-sign-algorithm=sha1") {
			t.Error("缺少原生签名")
		}
		if r.URL.Query().Has("versioning") {
			fmt.Fprint(w, "<VersioningConfiguration/>")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPut {
			if r.Header.Get("x-cos-forbid-overwrite") != "true" || r.Header.Get("Content-MD5") == "" {
				t.Error("缺少写入保护")
			}
			if stored != nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
			stored, _ = io.ReadAll(r.Body)
			return
		}
		_, _ = w.Write(stored)
	})
	data := []byte("应用加密信封")
	version, err := client.PutOnce(context.Background(), "private/key", data)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			got, err := client.PutOnce(context.Background(), "private/key", data)
			if err != nil || got != version {
				t.Error("同键重放应返回同一版本", got, err)
			}
		})
	}
	wg.Wait()
	if _, err := client.PutOnce(context.Background(), "private/key", []byte("不同密文")); err == nil {
		t.Fatal("不同密文不能重放成功")
	}
	reader, err := client.Open(context.Background(), "private/key", version)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(reader)
	if !bytes.Equal(got, data) {
		t.Fatal("密文读取错误")
	}
	_ = reader.Close()
	if reader.(*privateObject).Len() != 0 {
		t.Fatal("关闭应清空缓冲")
	}
	mu.Lock()
	stored = []byte("被篡改")
	mu.Unlock()
	if _, err := client.Open(context.Background(), "private/key", version); err == nil {
		t.Fatal("摘要不符必须拒绝")
	}
}

// TestTencentObjectsFailClosed 拒绝版本桶、错误、重定向和超大响应。
func TestTencentObjectsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"已开启版本控制", "<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>", 200},
		{"已暂停版本控制", "<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>", 200},
		{"响应根错误", "<Response/>", 200},
		{"响应过大", strings.Repeat("x", 4097), 200},
		{"鉴权失败", "", 403},
		{"服务失败", "", 503},
		{"重定向", "", 302},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testTencent(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			if _, err := client.PutOnce(context.Background(), "key", []byte("密文")); err != command.ErrUnavailable {
				t.Fatal(err)
			}
		})
	}
}

// TestTencentImageResults 覆盖正常、疑似、违规、部分失败及缺失结果，防止零值放行。
func TestTencentImageResults(t *testing.T) {
	for _, tc := range []struct {
		name, fields      string
		approved, failure bool
	}{
		{"正常", "<Result>0</Result>", true, false},
		{"违规", "<Result>1</Result>", false, false},
		{"疑似", "<Result>2</Result>", false, false},
		{"重复通过结果", "<Result>1</Result><Result>0</Result>", false, true},
		{"场景结果缺失", "<Result>0</Result><PornInfo><Code>0</Code></PornInfo>", false, true},
		{"结果缺失", "", false, true},
		{"结果未知", "<Result>3</Result>", false, true},
		{"错误码", "<Code>InvalidArgument</Code><Result>0</Result>", false, true},
		{"图片不支持", "<Code>ResourceNotSupported</Code>", false, true},
		{"局部失败", "<Result>0</Result><PornInfo><Code>12</Code><HitFlag>0</HitFlag></PornInfo>", false, true},
		{"局部疑似", "<Result>0</Result><PornInfo><Code>0</Code><HitFlag>2</HitFlag></PornInfo>", false, false},
		{"未完成", "<State>Failed</State><Result>0</Result>", false, true},
		{"请求错配", "<DataId>other</DataId><Result>0</Result>", false, true},
		{"尾随文档", "<Result>0</Result></JobsDetail></Response><Response><JobsDetail>", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testTencent(t, func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Input struct {
						DataID  string `xml:"DataId"`
						Content string
					}
					Conf struct {
						BizType string
						Async   int
					}
				}
				if r.URL.Path != "/image/auditing" || xml.NewDecoder(r.Body).Decode(&input) != nil {
					t.Error("请求错误")
				}
				data, _ := base64.StdEncoding.DecodeString(input.Input.Content)
				if string(data) != "规范化图片" || input.Conf.BizType != "synthetic-biz" || input.Conf.Async != 0 {
					t.Error("审核输入错误")
				}
				fmt.Fprintf(w, "<Response><JobsDetail><DataId>%s</DataId><State>Success</State>%s</JobsDetail></Response>", input.Input.DataID, tc.fields)
			})
			approved, err := client.Scan(context.Background(), []byte("规范化图片"), "image/png")
			if tc.name == "图片不支持" && !errors.Is(err, command.ErrInvalid) {
				t.Fatal("图片不支持未映射输入错误", err)
			}
			if approved != tc.approved || (err != nil) != tc.failure {
				t.Fatalf("approved=%v err=%v", approved, err)
			}
		})
	}
}

// TestTencentCancellationAndFailure 网络失败和取消均不能批准图片。
func TestTencentCancellationAndFailure(t *testing.T) {
	client := testTencent(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	if approved, err := client.Scan(context.Background(), []byte("图片"), "image/png"); approved || err == nil {
		t.Fatal("供应商故障不能通过")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if approved, err := client.Scan(ctx, []byte("图片"), "image/png"); approved || err == nil {
		t.Fatal("取消不能通过")
	}
}

// TestTencentImageTimeout 真正耗尽HTTP预算时不能将无响应解释为通过。
func TestTencentImageTimeout(t *testing.T) {
	release := make(chan struct{})
	client := testTencent(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
	})
	defer close(release)
	client.client.Timeout = 20 * time.Millisecond
	if approved, err := client.Scan(context.Background(), []byte("图片"), "image/jpeg"); approved || err != command.ErrUnavailable {
		t.Fatal("超时应失败关闭", approved, err)
	}
}
