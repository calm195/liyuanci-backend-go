package privacy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

// TestMatchmakingAutoPreviewErasedOnDeletion 验证注销及恢复清理覆盖已应用的加密预览，且不影响其他活动。
func TestMatchmakingAutoPreviewErasedOnDeletion(t *testing.T) {
	dsn := testconfig.Value("matchmaking_test_dsn")
	if dsn == "" {
		t.Skip("需显式隔离 PostgreSQL 测试配置")
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
		pool, err := db.DB()
		must(err)
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	parent := open(dsn)
	var base string
	must(parent.Raw("SELECT current_database()").Scan(&base).Error)
	if base != "postgres" && base != "liyuanci" && base != "whisper_test" {
		t.Fatal("拒绝非测试父库")
	}
	name := "privacy_matchmaking_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(parent.Exec("CREATE DATABASE " + name).Error)
	t.Cleanup(func() { must(parent.Exec("DROP DATABASE " + name).Error) })
	child := strings.Replace(dsn, "dbname="+base+" ", "dbname="+name+" ", 1)
	if child == dsn {
		t.Fatal("需要显式 dbname 参数及后续空格")
	}
	db := open(child)
	must(migration.Run(context.Background(), db, zap.NewNop(), "up"))
	account := entity.Account{ID: uuid.NewString(), Nickname: "合成清理账号", Verification: "verified"}
	must(db.Create(&account).Error)
	now := time.Now().UTC()
	createActivity := func() string {
		a := entity.MatchmakingActivity{ID: uuid.NewString(), Version: 1, Title: "合成隐私活动", Description: "验证预览清理", Status: "draft", Campuses: json.RawMessage(`[]`), RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now, ResultsAt: now.Add(time.Hour), StartsAt: now.Add(2 * time.Hour), EndsAt: now.Add(48 * time.Hour), Days: 1, CreatedAt: now}
		must(db.Create(&a).Error)
		return a.ID
	}
	activity, other := createActivity(), createActivity()
	must(db.Create(&entity.MatchmakingEnrollment{ID: uuid.NewString(), ActivityID: activity, AccountID: account.ID, Version: 1, ProfileVersion: 1, ProfileCipher: json.RawMessage(`{"synthetic":"encrypted"}`), Status: "approved", CreatedAt: now}).Error)
	jobs := []entity.MatchmakingAutoJob{}
	for _, status := range []string{"pending", "ready", "applied", "failed"} {
		jobs = append(jobs, entity.MatchmakingAutoJob{ID: uuid.NewString(), ActivityID: activity, Version: 1, ActivityVersion: 1, Status: status, Algorithm: "synthetic", InputHash: "retained-hash", ResultCipher: json.RawMessage(`{"synthetic":"encrypted"}`), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	}
	unrelated := entity.MatchmakingAutoJob{ID: uuid.NewString(), ActivityID: other, Version: 1, ActivityVersion: 1, Status: "ready", Algorithm: "synthetic", InputHash: "other", ResultCipher: json.RawMessage(`{"synthetic":"encrypted"}`), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	must(db.Create(&jobs).Error)
	must(db.Create(&unrelated).Error)
	must(db.Transaction(func(tx *gorm.DB) error { return activateMatchmaking(context.Background(), tx, account.ID) }))
	must(db.Transaction(func(tx *gorm.DB) error { return activateMatchmaking(context.Background(), tx, account.ID) }))
	for _, before := range jobs {
		var after entity.MatchmakingAutoJob
		must(db.First(&after, "id=?", before.ID).Error)
		if len(after.ResultCipher) != 0 || after.Status != "stale" || after.Version != 2 || after.Error != "参与成员已注销" || after.InputHash != before.InputHash {
			t.Fatalf("预览未清理或重复恢复不幂等: %+v", after)
		}
	}
	var remaining entity.MatchmakingAutoJob
	must(db.First(&remaining, "id=?", unrelated.ID).Error)
	if remaining.Status != "ready" || remaining.Version != 1 || len(remaining.ResultCipher) == 0 {
		t.Fatal("清理误伤其他活动")
	}
}
