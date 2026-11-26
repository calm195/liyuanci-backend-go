package app

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/adapters/local"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

// TestRedisWeChatMediaCheck 覆盖实际暂存、已验签回调、结果早到与可重复读。
func TestRedisWeChatMediaCheck(t *testing.T) {
	address := testconfig.Value("whisper_redis_test_address")
	if address == "" {
		t.Skip("需显式隔离 Redis")
	}
	ctx := context.Background()
	r := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = r.Close() })
	log := zap.NewNop()
	codec, err := securedata.New(local.Keys{Key: bytes.Repeat([]byte{89}, 32), Logger: log}, log)
	if err != nil {
		t.Fatal(err)
	}
	const appID, token = "wxsynthetic", "SyntheticToken"
	aesKey := bytes.Repeat([]byte{22}, 32)
	push, err := gateway.NewMediaPush(token, appID, base64.RawStdEncoding.EncodeToString(aesKey))
	if err != nil {
		t.Fatal(err)
	}
	active := true
	m := &wechatMedia{
		store: r, prefix: uuid.NewString() + ":", origin: "https://synthetic.invalid", push: push, codec: codec, log: log,
		identity: func(context.Context, string) ([]byte, error) { return []byte("synthetic-openid"), nil },
		active:   func(context.Context, string) (bool, error) { return active, nil },
	}
	router := gin.New()
	m.register(router)
	image := []byte("synthetic-image")
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)))
		return w
	}
	callback := func(trace, decision, recipient string, sign bool) int {
		q, body := encryptedMediaEvent(t, aesKey, token, recipient, appID, trace, decision)
		if !sign {
			q.Set("msg_signature", strings.Repeat("0", 40))
		}
		return request(http.MethodPost, mediaCallbackPath+"?"+q.Encode(), body).Code
	}
	var trace, source string
	var early string
	calls := 0
	m.start = func(_ context.Context, openID string, scene int, address string) (string, error) {
		calls++
		var err error
		u, err := url.Parse(address)
		if err != nil || u.Host != "synthetic.invalid" || u.Path != mediaSourcePath || openID != "synthetic-openid" || (scene != 1 && scene != 3) {
			t.Fatal("送检上传参数错误", err)
		}
		trace, source = uuid.NewString(), u.RequestURI()
		got := request(http.MethodGet, source, nil)
		if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), image) || got.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("微信不能读取本次规范化的图片", got.Code)
		}
		if early != "" && callback(trace, early, appID, true) != 200 {
			t.Fatal("提交回执前的回调未接收")
		}
		return trace, nil
	}
	check := func(id string, scene int, want error) {
		t.Helper()
		if err := m.check(ctx, "actor", id, scene, image); !errors.Is(err, want) {
			t.Fatalf("图片门禁结果 = %v，需要 %v", err, want)
		}
	}
	reservation := uuid.NewString()
	check(reservation, 3, command.ErrMediaReviewPending)
	if callback(trace, "pass", appID, false) != 403 || callback(trace, "pass", "other-app", true) != 403 {
		t.Fatal("伪造或其他小程序的回调未拒绝")
	}
	check(reservation, 3, command.ErrMediaReviewPending)
	if calls != 1 {
		t.Fatal("待审核的原键重试重新送检")
	}
	active = false
	if request(http.MethodGet, source, nil).Code != 404 {
		t.Fatal("已失效账号的临时图片仍可读取")
	}
	active = true
	if callback(trace, "pass", appID, true) != 200 {
		t.Fatal("有效回调未接收")
	}
	if request(http.MethodGet, source, nil).Code != 404 {
		t.Fatal("处理终态后仍可读取临时图片")
	}
	check(reservation, 3, nil)
	newReservation := uuid.NewString()
	check(newReservation, 1, command.ErrMediaReviewPending)
	if callback(trace, "risky", appID, true) != 200 || callback(trace, "pass", appID, true) != 200 {
		t.Fatal("回调重放未应答")
	}
	check(newReservation, 1, command.ErrContentViolation)
	early = "review"
	check(uuid.NewString(), 3, command.ErrContentReview)
	early = "pass"
	check(uuid.NewString(), 1, nil)
}

func encryptedMediaEvent(t *testing.T, key []byte, token, recipient, eventAppID, trace, decision string) (url.Values, []byte) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"Event": "wxa_media_check", "appid": eventAppID, "version": 2, "trace_id": trace,
		"errcode": 0, "result": map[string]string{"suggest": decision},
	})
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 20, 20+len(payload)+len(recipient)+32)
	binary.BigEndian.PutUint32(plain[16:20], uint32(len(payload)))
	plain = append(plain, payload...)
	plain = append(plain, recipient...)
	padding := 32 - len(plain)%32
	plain = append(plain, bytes.Repeat([]byte{byte(padding)}, padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(plain, plain)
	encrypted := base64.StdEncoding.EncodeToString(plain)
	stamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), "synthetic-nonce"
	parts := []string{token, stamp, nonce, encrypted}
	slices.Sort(parts)
	signature := sha1.Sum([]byte(strings.Join(parts, "")))
	wrapper, err := json.Marshal(map[string]string{"Encrypt": encrypted})
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{"timestamp": {stamp}, "nonce": {nonce}, "msg_signature": {hex.EncodeToString(signature[:])}}, wrapper
}
