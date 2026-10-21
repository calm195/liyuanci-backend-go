package studentcard_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/identity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/transportmq"
)

// T007/T011：编译期确保实际订阅器可直接注入领域消费者。
var _ transportmq.Handler = (*studentcard.Service)(nil)

type keys struct{}

func (keys) Wrap(_ context.Context, data []byte) (string, []byte, error) {
	block, _ := aes.NewCipher(bytes.Repeat([]byte{38}, 32))
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	if _, e := rand.Read(nonce); e != nil {
		return "", nil, e
	}
	return "synthetic-card-wrap", append(nonce, gcm.Seal(nil, nonce, data, nil)...), nil
}
func (keys) Unwrap(_ context.Context, ref string, data []byte) ([]byte, error) {
	if ref != "synthetic-card-wrap" || len(data) < 28 {
		return nil, errors.New("SYNTHETIC_KMS_SECRET")
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{38}, 32))
	gcm, _ := cipher.NewGCM(block)
	return gcm.Open(nil, data[:12], data[12:], nil)
}
func (keys) Digest(_ context.Context, ref, purpose string, data []byte) ([]byte, error) {
	return securedata.LookupDigest(bytes.Repeat([]byte{81}, 32), purpose, string(data))
}

type exchanger struct{}

func (exchanger) Exchange(_ context.Context, code string) ([]byte, error) {
	return []byte("SYNTHETIC_CARD_OPENID_" + code), nil
}

type objects struct {
	mu      sync.Mutex
	data    map[string][]byte
	calls   int
	failure bool
}

func (o *objects) PutOnce(_ context.Context, key string, data []byte) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.failure {
		return "", errors.New("SYNTHETIC_STORAGE_SECRET")
	}
	if existing, ok := o.data[key]; ok && !bytes.Equal(existing, data) {
		return "", errors.New("不可覆盖")
	}
	o.data[key] = bytes.Clone(data)
	return "synthetic-version-1", nil
}

type scanner struct {
	mu        sync.Mutex
	calls     int
	approved  bool
	failure   bool
	panicScan bool
	hook      func()
}

func (s *scanner) Scan(_ context.Context, data []byte, mime string) (bool, error) {
	s.mu.Lock()
	s.calls++
	approved, failure, panicScan, hook := s.approved, s.failure, s.panicScan, s.hook
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if panicScan {
		panic("SYNTHETIC_SCANNER_SECRET")
	}
	if failure {
		return false, errors.New("SYNTHETIC_SCANNER_SECRET")
	}
	if _, _, e := image.Decode(bytes.NewReader(data)); e != nil {
		return false, e
	}
	if mime != "image/png" {
		return false, errors.New("媒体不符")
	}
	return approved, nil
}

func picture(t *testing.T, c color.Color) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			im.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if e := png.Encode(&b, im); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

// T011/REQ-002、015、018：PG/Redis/HTTP真实，微信、KMS、私有对象和扫描均显式合成；不使用真实学生卡。
func TestPostgresStudentCards(t *testing.T) {
	dsn, address := os.Getenv("APP_STUDENTCARD_TEST_DSN"), os.Getenv("APP_STUDENTCARD_TEST_REDIS")
	if dsn == "" || address == "" {
		t.Skip("需真实隔离PG/Redis：python3 scripts/check-adminauth.py")
	}
	ctx := context.Background()
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	open := func(suffix string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: dsn + suffix, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		if e != nil {
			t.Fatal("数据库连接失败")
		}
		pool, _ := db.DB()
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	root := open("")
	if e := migration.Run(ctx, root, logger, "up"); e != nil {
		t.Fatal(e)
	}
	if e := migration.ApplyPermissions(ctx, root, logger); e != nil {
		t.Fatal(e)
	}
	if e := migration.ApplyPermissions(ctx, root, logger); e != nil {
		t.Fatal(e)
	}
	apiDB, workerDB := open(" options='-c role=liyuanci_identity_maintenance'"), open(" options='-c role=liyuanci_domain_worker'")
	client := redis.NewClient(&redis.Options{Addr: address})
	defer client.Close()
	limiter, _ := adminauth.NewRedisLimiter(client, time.Minute, 1000)
	codec, _ := securedata.New(keys{}, logger)
	store, _ := identity.New(apiDB, codec, keys{}, logger)
	if e := store.Configure(ctx, "openid", "synthetic-card-lookup"); e != nil {
		t.Fatal(e)
	}
	commandConfig := command.Config{Timeout: 5 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}
	sessions, e := studentauth.New(apiDB, logger, exchanger{}, store, limiter, studentauth.Config{StudentCardAssets: true, SessionTTL: time.Hour, ExchangeTTL: time.Minute, SessionKey: bytes.Repeat([]byte{73}, 32), Command: commandConfig})
	if e != nil {
		t.Fatal(e)
	}
	first, e := sessions.Login(ctx, "first", "first", "source", "card_login")
	if e != nil {
		t.Fatal(e)
	}
	second, e := sessions.Login(ctx, "second", "second", "source", "card_login_other")
	if e != nil {
		t.Fatal(e)
	}
	if !contains(first.Me.AllowedActions, "postMeStudentCardAssets") {
		t.Fatal("缺已装配动作")
	}
	bucket := &objects{data: map[string][]byte{}}
	scan := &scanner{approved: true}
	cfg := studentcard.Config{DecodeSlots: 2, MaxBytes: 4096, MaxPixels: 100, ReceiptKey: bytes.Repeat([]byte{42}, 32), Command: commandConfig, Lease: 10 * time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3}
	api, e := studentcard.New(apiDB, logger, sessions, codec, bucket, scan, cfg)
	if e != nil {
		t.Fatal(e)
	}
	worker, e := studentcard.New(workerDB, logger, sessions, codec, bucket, scan, cfg)
	if e != nil {
		t.Fatal(e)
	}
	imageA, imageB := picture(t, color.RGBA{R: 200, A: 255}), picture(t, color.RGBA{B: 200, A: 255})
	upload := func(key string) studentcard.Asset {
		t.Helper()
		out, e := api.Upload(ctx, first.Token, key, "card_upload", "image/png", bytes.NewReader(imageA))
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	work := func(id string) entity.WorkItem {
		t.Helper()
		var row entity.WorkItem
		if e := root.Where("target_id=? AND kind=?", id, "student_card.scan").Take(&row).Error; e != nil {
			t.Fatal(e)
		}
		return row
	}
	execute := func(sql string, args ...any) {
		t.Helper()
		if e := root.Exec(sql, args...).Error; e != nil {
			t.Fatal(e)
		}
	}
	initial := upload("first-card")
	if initial.ScanStatus != "pending" {
		t.Fatal("上传不能自动扫描通过")
	}
	repeated, e := api.Upload(ctx, first.Token, "first-card", "card_replay", "image/png", bytes.NewReader(imageA))
	if e != nil || repeated.AssetID != initial.AssetID {
		t.Fatal("上传重放创建重复对象", e)
	}
	if _, e = api.Upload(ctx, first.Token, "first-card", "card_digest", "image/png", bytes.NewReader(imageB)); e != command.ErrIdempotencyConflict {
		t.Fatal("异文件必须冲突", e)
	}
	if _, e = api.Get(ctx, second.Token, initial.AssetID, "card_other"); e != command.ErrNotFound {
		t.Fatal("跨主体状态读取", e)
	}
	if _, e = api.Get(ctx, first.Token, "00000000-0000-4000-8000-000000000001", "card_unknown"); e != command.ErrNotFound {
		t.Fatal("未知对象不应泄漏", e)
	}
	if _, e = api.Upload(ctx, first.Token, "invalid-mime", "card_mime", "image/jpeg", bytes.NewReader(imageA)); e != command.ErrInvalid {
		t.Fatal("伪造MIME应拒绝", e)
	}
	if _, e = api.Upload(ctx, first.Token, "oversize", "card_size", "image/png", strings.NewReader(strings.Repeat("x", 4097))); e != command.ErrInvalid {
		t.Fatal("超限应拒绝", e)
	}
	var stored entity.StudentCardPayload
	if e = root.Where("asset_id=?", initial.AssetID).Take(&stored).Error; e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(stored.Envelope, imageA) || bytes.Contains(stored.Envelope, []byte("SYNTHETIC_CARD_OPENID")) {
		t.Fatal("暂存含明文")
	}
	firstWork := work(initial.AssetID)
	// 外部调用期间持锁事务必须已经结束，独立连接可立即获得账号及资产锁。
	scan.hook = func() {
		if e := root.Transaction(func(db *gorm.DB) error {
			var a entity.Account
			if e := db.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("id=?", first.Me.ID).Take(&a).Error; e != nil {
				return e
			}
			var asset entity.Asset
			return db.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).Where("id=?", initial.AssetID).Take(&asset).Error
		}); e != nil {
			t.Error("外部扫描持有数据库锁", e)
		}
	}
	result, e := worker.Process(ctx, firstWork.ID, "card_process")
	scan.hook = nil
	if e != nil || !result.Applied {
		t.Fatal("扫描完成失败", e)
	}
	current, e := api.Get(ctx, first.Token, initial.AssetID, "card_state")
	if e != nil || current.ScanStatus != "approved" {
		t.Fatal("扫描未更新", e)
	}
	me, e := sessions.Current(ctx, first.Token, "card_not_verified")
	if e != nil || me.Verification != "unverified" {
		t.Fatal("图片通过不能授予学生认证", e)
	}
	var count int64
	if e = root.Model(&entity.StudentCardPayload{}).Where("asset_id=?", initial.AssetID).Count(&count).Error; e != nil || count != 0 {
		t.Fatal("暂存密文未清除")
	}
	result, e = worker.Process(ctx, firstWork.ID, "card_repeat")
	if e != nil || !result.Replayed || scan.calls != 1 || bucket.calls != 1 {
		t.Fatal("终态工作重复外发", e)
	}
	var event durable.Event
	if e = root.Model(&entity.OutboxEvent{}).Select("event_id,work_id AS correlation_id,aggregate_id,aggregate_version,event_type,created_at").Where("work_id=?", firstWork.ID).Take(&event).Error; e != nil {
		t.Fatal(e)
	}
	forged := event
	forged.AggregateID = "00000000-0000-4000-8000-000000000001"
	if got, e := worker.Consume(ctx, forged); e != nil || !got.Missing {
		t.Fatal("伪造消息未拒绝", e)
	}
	if got, e := worker.Consume(ctx, event); e != nil || !got.Replayed {
		t.Fatal("真实关联消息重放失败", e)
	}
	execute("UPDATE public.outbox_events SET superseded_at=clock_timestamp() WHERE event_id=?", event.EventID)
	if got, e := worker.Consume(ctx, event); e != nil || !got.Missing {
		t.Fatal("已替换事件仍可执行", e)
	}
	rejected := upload("reject-card")
	scan.approved = false
	if _, e = worker.Process(ctx, work(rejected.AssetID).ID, "card_reject"); e != nil {
		t.Fatal(e)
	}
	scan.approved = true
	if got, e := api.Get(ctx, first.Token, rejected.AssetID, "card_rejected"); e != nil || got.ScanStatus != "rejected" {
		t.Fatal("扫描拒绝未持久化", e)
	}
	retry := upload("retry-card")
	scan.failure = true
	retryWork := work(retry.AssetID)
	if _, e = worker.Process(ctx, retryWork.ID, "card_retry_fail"); e != command.ErrUnavailable {
		t.Fatal("扫描故障应失败关闭", e)
	}
	if got := work(retry.AssetID); got.Status != "retry_wait" {
		t.Fatal("临时失败未持久重试", got.Status)
	}
	scan.failure = false
	execute("UPDATE public.work_items SET next_at=clock_timestamp()-interval '1 second' WHERE id=?", retryWork.ID)
	if got, e := worker.ProcessNext(ctx, "card_compensate"); e != nil || !got.Applied {
		t.Fatal("无MQ唤醒补偿失败", e)
	}
	// 两次外部Put使用相同key/密文，不产生新的存储对象。
	bucket.mu.Lock()
	if len(bucket.data) != 3 {
		t.Fatal("重试产生重复存储对象", len(bucket.data))
	}
	bucket.mu.Unlock()
	var jpegImage bytes.Buffer
	if e := jpeg.Encode(&jpegImage, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); e != nil {
		t.Fatal(e)
	}
	jpegAsset, e := api.Upload(ctx, first.Token, "jpeg-card", "card_jpeg", "image/jpeg", bytes.NewReader(jpegImage.Bytes()))
	if e != nil {
		t.Fatal("JPEG上传失败", e)
	}
	if got, e := worker.Process(ctx, work(jpegAsset.AssetID).ID, "card_jpeg_scan"); e != nil || !got.Applied {
		t.Fatal("JPEG无损规范化扫描失败", e)
	}
	storageFailure := upload("storage-failure-card")
	storageWork := work(storageFailure.AssetID)
	bucket.failure = true
	if _, e = worker.Process(ctx, storageWork.ID, "card_storage_failure"); e != command.ErrUnavailable {
		t.Fatal("对象故障未失败关闭", e)
	}
	bucket.failure = false
	if got, e := api.Get(ctx, first.Token, storageFailure.AssetID, "card_storage_pending"); e != nil || got.ScanStatus != "pending" {
		t.Fatal("存储故障不得通过扫描", e)
	}
	execute("UPDATE public.work_items SET status='cancelled',lease_until=NULL WHERE id=?", storageWork.ID)
	panicAsset := upload("panic-card")
	scan.panicScan = true
	if _, e = worker.Process(ctx, work(panicAsset.AssetID).ID, "card_panic"); e != command.ErrUnavailable {
		t.Fatal("供应商panic未失败关闭", e)
	}
	scan.panicScan = false
	attention := upload("attention-card")
	attentionWork := work(attention.AssetID)
	scan.failure = true
	execute("UPDATE public.work_items SET attempts=2 WHERE id=?", attentionWork.ID)
	if _, e = worker.Process(ctx, attentionWork.ID, "card_attention"); e != command.ErrUnavailable {
		t.Fatal(e)
	}
	scan.failure = false
	if got := work(attention.AssetID); got.Status != "attention" {
		t.Fatal("预算耗尽未转待办")
	}
	stale := upload("stale-card")
	staleWork := work(stale.AssetID)
	scan.hook = func() {
		execute("UPDATE public.work_items SET version=version+1,lease_until=clock_timestamp()-interval '1 second' WHERE id=?", staleWork.ID)
	}
	if _, e = worker.Process(ctx, staleWork.ID, "card_stale"); e != command.ErrConflict {
		t.Fatal("旧租约不得确认", e)
	}
	scan.hook = nil
	if got, e := api.Get(ctx, first.Token, stale.AssetID, "card_stale_state"); e != nil || got.ScanStatus != "pending" {
		t.Fatal("旧持有者写入通过", e)
	}
	execute("UPDATE public.work_items SET next_at=clock_timestamp()-interval '1 second' WHERE id=?", staleWork.ID)
	if got, e := worker.Process(ctx, staleWork.ID, "card_reclaim"); e != nil || !got.Applied {
		t.Fatal("过期租约未恢复", e)
	}
	t.Run("并发领取与缺失源恢复", func(t *testing.T) {
		concurrent := upload("concurrent-card")
		concurrentWork := work(concurrent.AssetID)
		started, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		scan.hook = func() { close(started); <-release }
		finished := make(chan error, 1)
		go func() {
			got, e := worker.Process(ctx, concurrentWork.ID, "card_parallel_first")
			if e == nil && !got.Applied {
				e = errors.New("首个工作未完成")
			}
			finished <- e
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("未开始外部扫描")
		}
		if got, e := worker.Process(ctx, concurrentWork.ID, "card_parallel_second"); e != nil || !got.Deferred {
			t.Fatal("有效租约未阻止第二次外发", e)
		}
		unblock()
		if e := <-finished; e != nil {
			t.Fatal(e)
		}
		scan.hook = nil
		missingPayload := upload("missing-payload")
		missingWork := work(missingPayload.AssetID)
		execute("DELETE FROM public.student_card_payloads WHERE asset_id=?", missingPayload.AssetID)
		if got, e := worker.Process(ctx, missingWork.ID, "card_missing_payload"); e != nil || !got.Deferred {
			t.Fatal("缺密文应持久待办", e)
		}
		if current := work(missingPayload.AssetID); current.Status != "attention" {
			t.Fatal("缺源仍循环重试")
		}
		missingAsset := upload("missing-asset")
		orphanWork := work(missingAsset.AssetID)
		execute("DELETE FROM public.assets WHERE id=?", missingAsset.AssetID)
		if got, e := worker.Process(ctx, orphanWork.ID, "card_missing_asset"); e != nil || !got.Cancelled {
			t.Fatal("缺失目标不能重建学生卡", e)
		}
	})
	beforeDelete := upload("before-delete-card")
	beforeWork := work(beforeDelete.AssetID)
	execute("UPDATE public.accounts SET lifecycle='deleting' WHERE id=?", first.Me.ID)
	if got, e := worker.Process(ctx, beforeWork.ID, "card_before_delete"); e != nil || !got.Cancelled {
		t.Fatal("领取前注销未取消", e)
	}
	execute("UPDATE public.accounts SET lifecycle='active' WHERE id=?", first.Me.ID)
	if _, e = api.Get(ctx, first.Token, beforeDelete.AssetID, "card_before_delete_state"); e != command.ErrNotFound {
		t.Fatal("取消对象不可再读取", e)
	}
	// 外部扫描成功后账号进入注销，也不能发布通过状态；外部版本仍可供后续清理。
	cancelled := upload("cancel-card")
	cancelledWork := work(cancelled.AssetID)
	scan.hook = func() { execute("UPDATE public.accounts SET lifecycle='deleting' WHERE id=?", first.Me.ID) }
	if got, e := worker.Process(ctx, cancelledWork.ID, "card_cancel"); e != nil || !got.Cancelled {
		t.Fatal("注销晚到结果未取消", e)
	}
	scan.hook = nil
	execute("UPDATE public.accounts SET lifecycle='active' WHERE id=?", first.Me.ID)
	if _, e = api.Get(ctx, first.Token, cancelled.AssetID, "card_cancel_state"); e != command.ErrNotFound {
		t.Fatal("取消后卡片仍可读取", e)
	}
	// HTTP实际multipart边界验证，不向临时目录写明文图片。
	router := httpapi.New(logger, func(context.Context) error { return nil })
	if e = httpapi.RegisterStudentCards(router, api); e != nil {
		t.Fatal(e)
	}
	request := func(key string, extra bool) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="file"; filename="student.png"`)
		header.Set("Content-Type", "image/png")
		part, _ := writer.CreatePart(header)
		_, _ = part.Write(imageA)
		if extra {
			_ = writer.WriteField("openid", "forged")
		}
		_ = writer.Close()
		req := httptest.NewRequest("POST", "/api/v1/me/student-card-assets", &body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+first.Token)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("http-card", false); rec.Code != 202 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("上传HTTP失败", rec.Code)
	}
	if rec := request("http-extra", true); rec.Code != 400 {
		t.Fatal("额外multipart字段须拒绝", rec.Code)
	}
	// 失败的持久工作写入必须回滚资产与暂存，不能创建没有消费者的孤立上传。
	var before, after int64
	_ = root.Model(&entity.Asset{}).Count(&before).Error
	execute("CREATE FUNCTION public.fail_card_work() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$")
	execute("CREATE TRIGGER fail_card_work BEFORE INSERT ON public.work_items FOR EACH ROW EXECUTE FUNCTION public.fail_card_work()")
	_, failure := api.Upload(ctx, first.Token, "atomic-card", "card_atomic", "image/png", bytes.NewReader(imageA))
	execute("DROP TRIGGER fail_card_work ON public.work_items")
	execute("DROP FUNCTION public.fail_card_work()")
	_ = root.Model(&entity.Asset{}).Count(&after).Error
	if failure != command.ErrUnavailable || before != after {
		t.Fatal("上传失败未原子回滚", failure)
	}
	for _, entry := range logs.All() {
		data, _ := json.Marshal(entry.ContextMap())
		for _, secret := range []string{first.Token, "SYNTHETIC_KMS_SECRET", "SYNTHETIC_STORAGE_SECRET", "SYNTHETIC_SCANNER_SECRET", "SYNTHETIC_CARD_OPENID"} {
			if strings.Contains(entry.Message+string(data), secret) {
				t.Fatal("日志泄漏敏感信息")
			}
		}
	}
	seenSuccess, seenFailure, seenCancel, seenDeferred := false, false, false, false
	for _, entry := range logs.FilterMessage("学生卡操作完成").All() {
		fields := entry.ContextMap()
		if _, ok := fields["duration"]; !ok {
			t.Fatal("完成日志缺耗时")
		}
		if fields["result"] == "success" {
			seenSuccess = true
		}
		if fields["result"] == "failure" {
			seenFailure = true
		}
		if fields["cancelled"] == true {
			seenCancel = true
		}
		if fields["deferred"] == true {
			seenDeferred = true
		}
	}
	if !seenSuccess || !seenFailure || !seenCancel || !seenDeferred {
		t.Fatal("成功/失败/取消/租约等待日志不完整")
	}

	t.Log("学生卡真实PG/Redis/HTTP、加密暂存、租约/重试/取消/幂等通过；外部供应商为合成适配器，人工认证由独立模块测试")
}
func contains(values []string, value string) bool {
	for _, entry := range values {
		if entry == value {
			return true
		}
	}
	return false
}
