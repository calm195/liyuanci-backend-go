package migration

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// TestPostgresSequentialVersions 覆盖0032连续升级、旧原型校验和拒绝与本机同内容账本修正。
func TestPostgresSequentialVersions(t *testing.T) {
	_, db := applicationSchemaDB(t)
	ctx, log := context.Background(), zap.NewNop()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := loadVersions()
	must(err)
	ledger, err := files.ReadFile("metadata/ledger.sql")
	must(err)
	must(db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec(string(ledger)).Error; e != nil {
			return e
		}
		for _, entry := range entries {
			if entry.version > 32 {
				break
			}
			if e := tx.Exec(string(entry.up)).Error; e != nil {
				return e
			}
			if e := tx.Create(&entity.SchemaMigration{Version: entry.version, Checksum: entry.checksum}).Error; e != nil {
				return e
			}
		}
		return nil
	}))
	state := func(want int64) {
		t.Helper()
		var version int64
		must(db.Raw("SELECT COALESCE(max(version),0) FROM public.schema_migrations").Scan(&version).Error)
		if version != want {
			t.Fatalf("实际版本=%d 期望%d", version, want)
		}
	}
	state(32)
	if requireCurrentVersion(db) == nil {
		t.Fatal("0032误判为当前版本")
	}
	must(RunApplication(ctx, db, log, "up"))
	state(33)
	must(requireCurrentVersion(db))
	must(ApplyApplicationPermissions(ctx, db, log))
	last := entries[len(entries)-1]
	// 使用公开down同一个内部版本回退函数，在独立空审计表验证只删除33账本。
	must(db.Transaction(func(tx *gorm.DB) error { return rollbackVersion(tx, last) }))
	state(32)
	if db.Migrator().HasTable(&entity.MatchmakingTraitMaintenanceAudit{}) {
		t.Fatal("0033回退未删除自身表")
	}
	if !db.Migrator().HasColumn(&entity.MatchmakingTrait{}, "category") {
		t.Fatal("0033回退误删0032结构")
	}
	for _, versions := range [][]int64{{33}, {34}, {33, 34}, {35}} {
		for _, version := range versions {
			must(db.Create(&entity.SchemaMigration{Version: version, Checksum: "retired-prototype"}).Error)
		}
		for _, direction := range []string{"up", "down"} {
			err := RunApplication(ctx, db, log, direction)
			if err == nil {
				t.Fatalf("旧账本%v被%s接纳", versions, direction)
			}
			if len(versions) == 1 && versions[0] == 33 && !strings.Contains(err.Error(), "校验和") {
				t.Fatalf("旧0033未按SQL校验和拒绝：%v", err)
			}
		}
		if requireCurrentVersion(db) == nil || ApplyApplicationPermissions(ctx, db, log) == nil {
			t.Fatal("旧账本通过当前状态/权限校验")
		}
		if db.Migrator().HasTable(&entity.MatchmakingTraitMaintenanceAudit{}) {
			t.Fatal("拒绝旧账本时却创建0033结构")
		}
		must(db.Where("version IN ?", versions).Delete(&entity.SchemaMigration{}).Error)
		state(32)
	}
	must(RunApplication(ctx, db, log, "up"))
	state(33)
	var before, after entity.SchemaMigration
	must(db.Where("version=?", 33).Take(&before).Error)
	// 本机未交付构建曾以35记录完全相同SQL；仅核实内容后可显式修正编号。
	// 应用迁移本身仍拒绝跳号，不能替运维自动改写账本或接纳旧原型结构。
	must(db.Model(&entity.SchemaMigration{}).Where("version=? AND checksum=?", 33, last.checksum).Update("version", 35).Error)
	state(35)
	if RunApplication(ctx, db, log, "up") == nil {
		t.Fatal("同内容跳号账本未经显式修正却被接纳")
	}
	must(db.Model(&entity.SchemaMigration{}).Where("version=? AND checksum=?", 35, last.checksum).Update("version", 33).Error)
	state(33)
	must(RunApplication(ctx, db, log, "up"))
	must(requireCurrentVersion(db))
	must(db.Where("version=?", 33).Take(&after).Error)
	if before.Checksum != after.Checksum || !before.AppliedAt.Equal(after.AppliedAt) {
		t.Fatal("仅修正编号却改变校验和或原应用时间")
	}
	// 公开down仍只支持整体空库回退，确保连续账本与结构全部回退。
	must(RunApplication(ctx, db, log, "down"))
	state(0)
}
