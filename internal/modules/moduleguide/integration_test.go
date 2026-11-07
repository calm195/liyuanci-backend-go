package moduleguide_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moduleguide"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

// TestPostgresGuide 在独立临时库验证发布隔离、双账号确认、并发版本与幂等重放。
func TestPostgresGuide(t *testing.T) {
	dsn := testconfig.Value("resource_test_dsn")
	if dsn == "" {
		t.Skip("需要隔离PostgreSQL测试配置")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	open := func(value string) *gorm.DB {
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		must(err)
		return db
	}
	parent := open(dsn)
	parentPool, err := parent.DB()
	must(err)
	defer parentPool.Close()
	var base string
	must(parent.Raw("SELECT current_database()").Scan(&base).Error)
	if base != "postgres" && base != "liyuanci" && base != "whisper_test" {
		t.Fatal("拒绝非测试父库")
	}
	name := "guide_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(parent.Exec("CREATE DATABASE " + name).Error)
	defer func() { must(parent.Exec("DROP DATABASE " + name).Error) }()
	dbdsn := strings.Replace(dsn, "dbname="+base+" ", "dbname="+name+" ", 1)
	if dbdsn == dsn {
		t.Fatal("需要明确的dbname及后续空格")
	}
	db := open(dbdsn)
	pool, err := db.DB()
	must(err)
	defer pool.Close()
	ctx, log := context.Background(), zap.NewNop()
	must(migration.Run(ctx, db, log, "up"))
	service, err := moduleguide.New(db, log, command.Config{Timeout: time.Second * 10, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3})
	must(err)
	session := func(admin bool, role string) string {
		id, token := uuid.NewString(), uuid.NewString()
		ref := uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		value := entity.Session{ID: uuid.NewString(), TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}
		if admin {
			must(db.Create(&entity.AdminAccount{ID: id, CredentialRef: &ref}).Error)
			must(db.Create(&entity.AdminRoleBinding{AdminID: id, Role: role}).Error)
			value.AdminID = &id
		} else {
			must(db.Create(&entity.Account{ID: id, Nickname: "合成学生"}).Error)
			value.AccountID = &id
		}
		must(db.Create(&value).Error)
		return token
	}
	admin, reviewer, a, b := session(true, "super_admin"), session(true, "review_operator"), session(false, ""), session(false, "")
	_, err = service.ReadAdmin(ctx, reviewer, "test")
	if !errors.Is(err, command.ErrForbidden) {
		t.Fatalf("普通审核读取草稿错误=%v", err)
	}
	input := moduleguide.SaveInput{ExpectedVersion: 1, Modules: []moduleguide.Module{{Module: "wishes", Title: "许愿池", Body: "填写心愿"}}}
	key := uuid.NewString()
	draft, err := service.Save(ctx, admin, key, "test", input)
	must(err)
	replayed, err := service.Save(ctx, admin, key, "test", input)
	must(err)
	if draft.Version != 2 || replayed.Version != 2 {
		t.Fatal("重复保存改变版本")
	}
	before, err := service.Read(ctx, a, "test")
	must(err)
	if before.Version != 0 || len(before.Modules) != 0 {
		t.Fatal("草稿提前公开")
	}
	_, err = service.Publish(ctx, admin, uuid.NewString(), "test", moduleguide.PublishInput{ExpectedVersion: 1})
	if !errors.Is(err, command.ErrVersionConflict) {
		t.Fatalf("过期草稿发布错误=%v", err)
	}
	published, err := service.Publish(ctx, admin, uuid.NewString(), "test", moduleguide.PublishInput{ExpectedVersion: 2})
	must(err)
	if published.Version != 3 || published.PublishedVersion != 2 {
		t.Fatal("发布版本异常")
	}
	ack, err := service.Acknowledge(ctx, a, uuid.NewString(), "test", moduleguide.AckInput{Version: 2})
	must(err)
	if ack.AcknowledgedVersion != 2 {
		t.Fatal("确认未保存")
	}
	other, err := service.Read(ctx, b, "test")
	must(err)
	if other.AcknowledgedVersion != 0 || other.Version != 2 || len(other.Modules) != 1 {
		t.Fatal("双账号说明确认未隔离")
	}
	_, err = service.Acknowledge(ctx, b, uuid.NewString(), "test", moduleguide.AckInput{Version: 1})
	if !errors.Is(err, command.ErrVersionConflict) {
		t.Fatalf("过期发布确认错误=%v", err)
	}
	_, err = service.Save(ctx, admin, uuid.NewString(), "test", moduleguide.SaveInput{ExpectedVersion: 3, Modules: []moduleguide.Module{{Module: "wishes", Title: "新标题", Body: "未发布的草稿"}}})
	must(err)
	after, err := service.Read(ctx, a, "test")
	must(err)
	if after.Version != 2 || after.Modules[0].Title != "许愿池" {
		t.Fatal("修改草稿污染发布内容")
	}
}
