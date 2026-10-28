// Package whisperrate 实现树洞账号滚动窗口，不将账号或请求原文写入 Redis 键。
package whisperrate

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/whisper"
)

// Limiter 使用注入客户端，不拥有连接生命周期；无 Redis 失败放行。
type Limiter struct {
	client  redis.UniversalClient
	key     []byte
	logger  *zap.Logger
	timeout time.Duration
}

// New 要求独立用途密钥及有界短时限，避免在数据库命令锁内长期等待。
func New(client redis.UniversalClient, key []byte, logger *zap.Logger, timeout time.Duration) (*Limiter, error) {
	if client == nil || len(key) < 32 || logger == nil || timeout <= 0 || timeout > time.Second {
		return nil, command.ErrInvalid
	}
	return &Limiter{client: client, key: append([]byte(nil), key...), logger: logger, timeout: timeout}, nil
}

type window struct {
	Milliseconds int64
	Limit        int
}
type lease struct {
	Keys  []string
	Token string
}

func windows(bucket string) []window {
	switch bucket {
	case "post":
		return []window{{600000, 5}, {86400000, 20}}
	case "comment":
		return []window{{600000, 20}, {86400000, 100}}
	case "hug":
		return []window{{60000, 60}}
	case "nickname":
		return []window{{86400000, 3}}
	case "maintenance":
		return []window{{60000, 60}}
	}
	return nil
}

const reserveScript = `
local clock=redis.call('TIME')
local now=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local retry=0
for i,key in ipairs(KEYS) do
 local window=tonumber(ARGV[i*2])
 local limit=tonumber(ARGV[i*2+1])
 redis.call('ZREMRANGEBYSCORE',key,'-inf',now-window)
 if redis.call('ZCARD',key)>=limit then
  local first=redis.call('ZRANGE',key,0,0,'WITHSCORES')
  retry=math.max(retry,math.ceil((tonumber(first[2])+window-now)/1000))
 end
end
if retry>0 then return retry end
for i,key in ipairs(KEYS) do
 redis.call('ZADD',key,now,ARGV[1])
 redis.call('PEXPIRE',key,tonumber(ARGV[i*2])+1000)
end
return 0`

func (l *Limiter) completed(operation string, start time.Time, err *error) {
	result := "success"
	if *err != nil {
		result = "failure"
	}
	l.logger.Info("树洞限频操作完成", zap.String("operation", operation), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}

// Reserve 原子检查所有滚动窗口并预留；只有新业务 Apply 会调用。
func (l *Limiter) Reserve(ctx context.Context, actor, bucket string) (ref string, err error) {
	defer l.completed("reserve", time.Now(), &err)
	if !whisper.ValidID(actor) || windows(bucket) == nil {
		return "", command.ErrInvalid
	}
	mac := hmac.New(sha256.New, l.key)
	mac.Write([]byte("whisper.rate." + bucket + "\x00" + actor))
	digest := hex.EncodeToString(mac.Sum(nil))
	value := lease{Token: uuid.NewString()}
	args := []any{value.Token}
	for i, w := range windows(bucket) {
		value.Keys = append(value.Keys, "whisper-rate:{"+digest+"}:"+string(rune('a'+i)))
		args = append(args, w.Milliseconds, w.Limit)
	}
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	seconds, e := l.client.Eval(ctx, reserveScript, value.Keys, args...).Int64()
	if e != nil {
		return "", command.ErrUnavailable
	}
	if seconds > 0 {
		return "", &whisper.RateError{Seconds: int(seconds)}
	}
	b, _ := json.Marshal(value)
	return string(b), nil
}

// Release 只移除本次预留，不能撤销同账号其他已接受命令。
func (l *Limiter) Release(ctx context.Context, ref string) (err error) {
	defer l.completed("release", time.Now(), &err)
	var value lease
	if json.Unmarshal([]byte(ref), &value) != nil || !whisper.ValidID(value.Token) || len(value.Keys) < 1 || len(value.Keys) > 2 {
		return command.ErrInvalid
	}
	for _, key := range value.Keys {
		if !strings.HasPrefix(key, "whisper-rate:{") {
			return command.ErrInvalid
		}
	}
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	if e := l.client.Eval(ctx, `for _,key in ipairs(KEYS) do redis.call('ZREM',key,ARGV[1]) end;return 0`, value.Keys, value.Token).Err(); e != nil {
		return command.ErrUnavailable
	}
	return nil
}
