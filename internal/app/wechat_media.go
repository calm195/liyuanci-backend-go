package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/adapters/gateway"
	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/identity"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

const (
	mediaSourcePath   = "/api/v1/wechat/media/source"
	mediaCallbackPath = "/api/v1/wechat/media/callback"
	mediaReviewTTL    = 35 * time.Minute
	mediaResultTTL    = 45 * time.Minute
)

// wechatMedia 协调受控取图与回调。缓存丢失时重新送检，已有上传预约仍须取得明确通过。
type wechatMedia struct {
	store          redis.UniversalClient
	origin, prefix string
	push           *gateway.MediaPush
	start          func(context.Context, string, int, string) (string, error)
	identity       func(context.Context, string) ([]byte, error)
	active         func(context.Context, string) (bool, error)
	codec          *securedata.Codec
	log            *zap.Logger
}

type mediaInput struct {
	Owner    string
	Envelope securedata.Envelope
}

type mediaSubmission struct {
	Trace  string
	Source string
}

func newWeChatMedia(cfg config.WeChatMedia, appID string, provider *gateway.WeChat, db *gorm.DB,
	store redis.UniversalClient, codec *securedata.Codec, log *zap.Logger) (*wechatMedia, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled() {
		return nil, nil
	}
	push, err := gateway.NewMediaPush(cfg.Token, appID, cfg.EncodingAESKey)
	if err != nil {
		return nil, err
	}
	namespace := sha256.Sum256([]byte(appID + "\x00" + cfg.Token))
	return &wechatMedia{
		store: store, origin: cfg.Origin, prefix: "liyuanci:wechat-media:" + hex.EncodeToString(namespace[:]) + ":",
		push: push, start: provider.StartImageCheck, codec: codec, log: log,
		identity: func(ctx context.Context, account string) ([]byte, error) {
			return identity.ReadOpenID(ctx, db, codec, account)
		},
		active: func(ctx context.Context, account string) (bool, error) {
			var row entity.Account
			err := db.WithContext(ctx).Select("id").Where("id=? AND lifecycle='active'", account).Take(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return err == nil, err
		},
	}, nil
}

// check 在业务模块持有上传租约时调用。预约编号和内容摘要共同绑定最终可安装的字节。
func (m *wechatMedia) check(ctx context.Context, account, reservation string, scene int, image []byte) (err error) {
	if m == nil {
		return command.ErrUnavailable
	}
	start := time.Now()
	defer func() {
		result := "pass"
		if err != nil {
			result = err.Error()
		}
		m.log.Info("微信图片检测检查完成", zap.Int("scene", scene), zap.String("result", result), zap.Duration("duration", time.Since(start)))
	}()
	digest := sha256.Sum256(image)
	key := m.prefix + "job:" + strconv.Itoa(scene) + ":" + reservation + ":" + hex.EncodeToString(digest[:])
	value, err := m.store.Get(ctx, key).Bytes()
	if err == nil {
		var submitted mediaSubmission
		if json.Unmarshal(value, &submitted) != nil || submitted.Trace == "" || submitted.Source == "" {
			return command.ErrUnavailable
		}
		return m.readResult(ctx, key, submitted)
	}
	if !errors.Is(err, redis.Nil) {
		return command.ErrUnavailable
	}
	return m.submit(ctx, key, account, scene, image)
}

func (m *wechatMedia) submit(ctx context.Context, key, account string, scene int, image []byte) error {
	openID, err := m.identity(ctx, account)
	if err != nil {
		return command.ErrUnavailable
	}
	defer clear(openID)
	source := uuid.NewString()
	envelope, err := m.codec.Encrypt(ctx, mediaBinding(source), image)
	if err != nil {
		return command.ErrUnavailable
	}
	encoded, err := json.Marshal(mediaInput{Owner: account, Envelope: envelope})
	if err != nil || m.store.Set(ctx, m.prefix+"input:"+source, encoded, mediaReviewTTL).Err() != nil {
		return command.ErrUnavailable
	}
	query := url.Values{"id": {source}}
	trace, err := m.start(ctx, string(openID), scene, m.origin+mediaSourcePath+"?"+query.Encode())
	if err != nil {
		// 超时可能已被微信受理，临时密文也会按有效期自动删除。
		return err
	}
	submitted := mediaSubmission{Trace: trace, Source: source}
	encodedJob, err := json.Marshal(submitted)
	if err != nil {
		return command.ErrUnavailable
	}
	stored, err := m.store.SetNX(ctx, key, encodedJob, mediaReviewTTL).Result()
	if err != nil {
		return command.ErrUnavailable
	}
	if !stored {
		return command.ErrMediaReviewPending
	}
	// 建立回调至临时取图的索引，确认终态后可立即清理密文。
	_ = m.store.Set(ctx, m.prefix+"source:"+trace, source, mediaReviewTTL).Err()
	// 回调可能早于提交响应到达，先读取当前 trace 的结果。
	return m.readResult(ctx, key, submitted)
}

func (m *wechatMedia) readResult(ctx context.Context, jobKey string, submitted mediaSubmission) error {
	result, err := m.store.Get(ctx, m.prefix+"result:"+submitted.Trace).Result()
	if errors.Is(err, redis.Nil) {
		return command.ErrMediaReviewPending
	}
	if err != nil {
		return command.ErrUnavailable
	}
	_ = m.store.Del(ctx, m.prefix+"input:"+submitted.Source, m.prefix+"source:"+submitted.Trace).Err()
	switch result {
	case "pass":
		return nil
	case "risky":
		return command.ErrContentViolation
	case "review":
		return command.ErrContentReview
	default:
		// 供应商技术故障仅释放本次任务引用，后续重试重新送检。
		_ = m.store.Del(ctx, jobKey).Err()
		return command.ErrUnavailable
	}
}

func mediaBinding(id string) securedata.Binding {
	return securedata.Binding{Purpose: "wechat_media_input", ObjectID: id, Version: 1}
}

func (m *wechatMedia) source(ctx context.Context, id string) ([]byte, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.Version() != 4 || parsed.String() != id {
		return nil, command.ErrNotFound
	}
	raw, err := m.store.Get(ctx, m.prefix+"input:"+id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, command.ErrNotFound
	}
	if err != nil {
		return nil, command.ErrUnavailable
	}
	var input mediaInput
	if json.Unmarshal(raw, &input) != nil {
		return nil, command.ErrUnavailable
	}
	active, err := m.active(ctx, input.Owner)
	if err != nil {
		return nil, command.ErrUnavailable
	}
	if !active {
		return nil, command.ErrNotFound
	}
	return m.codec.Decrypt(ctx, mediaBinding(id), input.Envelope)
}

func (m *wechatMedia) record(ctx context.Context, trace, decision string) error {
	// 同一 trace 的终态只写一次，重复回调不能改变已确认的结论。
	if err := m.store.SetNX(ctx, m.prefix+"result:"+trace, decision, mediaResultTTL).Err(); err != nil {
		return err
	}
	index := m.prefix + "source:" + trace
	if source, err := m.store.Get(ctx, index).Result(); err == nil {
		_ = m.store.Del(ctx, m.prefix+"input:"+source, index).Err()
	}
	return nil
}
