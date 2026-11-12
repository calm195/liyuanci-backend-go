package privacy

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

type cleanupImages struct {
	fail bool
	keys []string
}

func (objects *cleanupImages) RemoveWishImage(_ context.Context, key string) error {
	objects.keys = append(objects.keys, key)
	if objects.fail {
		return errors.New("合成外部删除失败")
	}
	return nil
}

// TestPostgresWishImageCleanup 验证真实行锁与清理状态；外部对象删除使用可控失败适配器。
func TestPostgresWishImageCleanup(t *testing.T) {
	dsn := testconfig.Value("wish_lifecycle_test_dsn")
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
	ctx, log := context.Background(), zap.NewNop()
	root := open(dsn)
	must(migration.Run(ctx, root, log, "up"))
	must(testconfig.ApplyDatabaseRoles(ctx, root, log))
	objects := &cleanupImages{fail: true}
	service := &Service{db: open(dsn + " options='-c role=liyuanci_privacy_maintenance'"), logger: log, cfg: Config{Command: command.Config{Timeout: 5 * time.Second}, RetryDelay: time.Millisecond, WishImageObjects: objects}}
	owner, wish := uuid.NewString(), uuid.NewString()
	must(root.Create(&entity.Account{ID: owner, Nickname: "合成图片清理账号", Verification: "verified"}).Error)
	must(root.Transaction(func(db *gorm.DB) error {
		if err := db.Create(&entity.Wish{ID: wish, OwnerID: owner, CurrentContentVersion: 1, Category: "help", Campus: "any"}).Error; err != nil {
			return err
		}
		return db.Create(&entity.WishVersion{WishID: wish, ContentVersion: 1, BodyCipher: json.RawMessage(`{}`), SchoolDetectionVersion: "pending"}).Error
	}))
	now, lease := time.Now().UTC(), time.Now().Add(time.Hour).UTC()
	newImage := func(uploading bool) entity.WishImage {
		id := uuid.NewString()
		row := entity.WishImage{ID: id, OwnerID: &owner, ObjectKey: "wish-images/" + id, ByteSize: 128, Width: 32, Height: 32, ScanState: "approved", InstalledAt: &now, RemovableAfter: now.Add(-time.Hour)}
		if uploading {
			row.ScanState, row.InstalledAt, row.UploadUntil = "pending", nil, &lease
		}
		must(root.Create(&row).Error)
		return row
	}
	bound, uploading, unused := newImage(false), newImage(true), newImage(false)
	must(root.Create(&entity.WishVersionImage{WishID: wish, ContentVersion: 1, ImageID: bound.ID}).Error)
	if err := service.cleanNextWishImage(ctx); err == nil {
		t.Fatal("外部删除失败未保留失败状态")
	}
	var failed entity.WishImage
	must(root.Where("id=?", unused.ID).Take(&failed).Error)
	if len(objects.keys) != 1 || objects.keys[0] != unused.ObjectKey || failed.CleanupStartedAt == nil || failed.RemovedAt != nil {
		t.Fatal("清理误选仍引用/上传中的图片或提前完成")
	}
	objects.fail = false
	must(root.Model(&entity.WishImage{}).Where("id=?", unused.ID).Update("removable_after", now.Add(-time.Hour)).Error)
	must(service.cleanNextWishImage(ctx))
	must(root.Where("id=?", unused.ID).Take(&failed).Error)
	if failed.RemovedAt == nil || len(objects.keys) != 2 || objects.keys[1] != unused.ObjectKey {
		t.Fatal("外部删除失败不能幂等重试")
	}
	// 注销清理先删除普通来源，再清空资源作者；上传中的租约仍应保护未结束的外部写入。
	must(root.Transaction(func(db *gorm.DB) error {
		if err := db.Where("wish_id=?", wish).Delete(&entity.WishVersion{}).Error; err != nil {
			return err
		}
		return db.Where("id=?", wish).Delete(&entity.Wish{}).Error
	}))
	must(root.Where("id=?", owner).Delete(&entity.Account{}).Error)
	for range 3 {
		must(root.Model(&entity.WishImage{}).Where("id IN ?", []string{bound.ID, unused.ID}).Update("removable_after", now.Add(-time.Hour)).Error)
		must(service.cleanNextWishImage(ctx))
	}
	var remaining int64
	must(root.Model(&entity.WishImage{}).Where("id IN ?", []string{bound.ID, unused.ID}).Count(&remaining).Error)
	if remaining != 0 {
		t.Fatal("注销后对象清理引用未最终释放", remaining)
	}
	must(root.Model(&entity.WishImage{}).Where("id=?", uploading.ID).Count(&remaining).Error)
	if remaining != 1 {
		t.Fatal("注销清理越过仍有效上传租约")
	}
	must(root.Model(&entity.WishImage{}).Where("id=?", uploading.ID).Update("upload_until", now.Add(-time.Minute)).Error)
	must(service.cleanNextWishImage(ctx))
	must(root.Model(&entity.WishImage{}).Where("id=?", uploading.ID).Count(&remaining).Error)
	if remaining != 0 {
		t.Fatal("租约到期后遗留上传未清理")
	}
}
