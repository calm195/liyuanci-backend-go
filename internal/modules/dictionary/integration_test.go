package dictionary_test

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
	"github.com/calm195/liyuanci-backend-go/internal/modules/dictionary"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

// TestPostgresDictionaries 在独立临时库验证字典权限、版本、幂等、停用与审计。
func TestPostgresDictionaries(t *testing.T) {
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
	name := "dictionary_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
	service, err := dictionary.New(db, log, command.Config{Timeout: time.Second * 10, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3})
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
	admin, reviewer, student := session(true, "super_admin"), session(true, "review_operator"), session(false, "")
	initial, err := service.Read(ctx, "test")
	must(err)
	if initial.Version != 1 || len(initial.Items) < 40 {
		t.Fatal("初始化展示字典缺失")
	}
	_, err = service.ReadAdmin(ctx, reviewer, "test")
	must(err)
	_, err = service.ReadAdmin(ctx, student, "test")
	if !errors.Is(err, command.ErrForbidden) {
		t.Fatalf("学生读取管理字典: %v", err)
	}
	enabled := true
	input := dictionary.SaveInput{ExpectedVersion: 1, Label: "有帮助", SortOrder: 9, Enabled: &enabled}
	_, err = service.Save(ctx, reviewer, uuid.NewString(), "test", "resource_feedback", "valid", input)
	if !errors.Is(err, command.ErrForbidden) {
		t.Fatalf("普通管理员写入: %v", err)
	}
	key := uuid.NewString()
	saved, err := service.Save(ctx, admin, key, "test", "resource_feedback", "valid", input)
	must(err)
	if saved.Version != 2 {
		t.Fatal("发布版本未递增")
	}
	replay, err := service.Save(ctx, admin, key, "test", "resource_feedback", "valid", input)
	must(err)
	if replay.Version != 2 {
		t.Fatal("重放再次发布")
	}
	_, err = service.Save(ctx, admin, uuid.NewString(), "test", "resource_feedback", "valid", input)
	if !errors.Is(err, command.ErrVersionConflict) {
		t.Fatalf("旧版本写入: %v", err)
	}
	var count int64
	must(db.Model(&entity.DisplayDictionaryAction{}).Count(&count).Error)
	if count != 1 {
		t.Fatal("审计不唯一")
	}
	input.ExpectedVersion = 2
	_, err = service.Save(ctx, admin, uuid.NewString(), "test", "resource_feedback", "unknown", input)
	if !errors.Is(err, command.ErrNotFound) {
		t.Fatalf("创建固定代码: %v", err)
	}
	disabled := false
	input.Enabled = &disabled
	_, err = service.Save(ctx, admin, uuid.NewString(), "test", "verification_status", "verified", input)
	if !errors.Is(err, command.ErrInvalid) {
		t.Fatalf("系统状态不应停用: %v", err)
	}
	saved, err = service.Save(ctx, admin, uuid.NewString(), "test", "resource_feedback", "valid", input)
	must(err)
	found := false
	for _, item := range saved.Items {
		if item.Type == "resource_feedback" && item.Code == "valid" {
			found = true
			if item.Enabled || item.Label != "有帮助" {
				t.Fatal("停用清除了历史展示")
			}
		}
	}
	if !found {
		t.Fatal("停用项消失")
	}
	replay, err = service.Save(ctx, admin, key, "test", "resource_feedback", "valid", dictionary.SaveInput{ExpectedVersion: 1, Label: "有帮助", SortOrder: 9, Enabled: &enabled})
	must(err)
	if replay.Version != 3 {
		t.Fatal("重放应读取最新字典快照")
	}
	reportInput := dictionary.SaveReportTypeInput{ExpectedVersion: 3, Label: "合成举报类型", Published: &enabled}
	reports, err := service.SaveReportType(ctx, admin, uuid.NewString(), "test", "synthetic", reportInput)
	must(err)
	if reports.Version != 4 || len(reports.Items) != 1 || !reports.Items[0].Published {
		t.Fatal("举报类型创建未发布")
	}
	reportInput.ExpectedVersion = 4
	reportInput.Published = &disabled
	reports, err = service.SaveReportType(ctx, admin, uuid.NewString(), "test", "synthetic", reportInput)
	must(err)
	if reports.Version != 5 || reports.Items[0].Published {
		t.Fatal("举报类型停用失败")
	}
	var published int64
	must(db.Model(&entity.ReportType{}).Where("published").Count(&published).Error)
	if published != 0 {
		t.Fatal("停用仍可用于新举报")
	}
	reports, err = service.ReadReportTypes(ctx, reviewer, "test")
	must(err)
	if len(reports.Items) != 1 || reports.Items[0].Label != "合成举报类型" {
		t.Fatal("停用历史标签丢失")
	}
	reportInput.ExpectedVersion = 5
	_, err = service.SaveReportType(ctx, reviewer, uuid.NewString(), "test", "synthetic", reportInput)
	if !errors.Is(err, command.ErrForbidden) {
		t.Fatalf("普通管理员可维护举报类型: %v", err)
	}
	// 举报类型受现有树洞契约32字符上限约束，超长代码不能写入。
	_, err = service.SaveReportType(ctx, admin, uuid.NewString(), "test", strings.Repeat("a", 33), reportInput)
	if !errors.Is(err, command.ErrInvalid) {
		t.Fatalf("超长举报类型被接受: %v", err)
	}
	boundary, err := service.SaveReportType(ctx, admin, uuid.NewString(), "test", strings.Repeat("a", 32), reportInput)
	must(err)
	if boundary.Version != 6 {
		t.Fatal("32字符举报类型未按契约创建")
	}
	// 同一发布版本的并发编辑只允许一个提交，另一个必须刷新。
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, e := service.Save(ctx, admin, uuid.NewString(), "test", "resource_feedback", "valid", dictionary.SaveInput{ExpectedVersion: 6, Label: "有帮助", SortOrder: 0, Enabled: &enabled})
			results <- e
		}()
	}
	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			successes++
		} else if errors.Is(e, command.ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("并发发布结果：成功%d，冲突%d", successes, conflicts)
	}
	// 会话撤销必须影响后续管理读取，公开展示无需账号信息。
	hash := sha256.Sum256([]byte(admin))
	must(db.Model(&entity.Session{}).Where("token_hash=?", hash[:]).Update("revoked_at", time.Now()).Error)
	_, err = service.ReadAdmin(ctx, admin, "test")
	if !errors.Is(err, command.ErrUnauthenticated) {
		t.Fatalf("撤销会话仍可读取: %v", err)
	}
	_, err = service.Read(ctx, "test")
	must(err)
}
