package migration

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// verifyMatchmakingUpgrade 验证已有账本升级及已发布配对的会话补建。
func verifyMatchmakingUpgrade(t *testing.T, baseline int) {
	t.Helper()
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
		if err := tx.Exec(string(ledger)).Error; err != nil {
			return err
		}
		for i, entry := range entries[:baseline] {
			if err := tx.Exec(string(entry.up)).Error; err != nil {
				return err
			}
			if err := tx.Create(&entity.SchemaMigration{Version: int64(i + 1), Checksum: entry.checksum}).Error; err != nil {
				return err
			}
		}
		return nil
	}))
	if baseline == 19 {
		must(db.Exec(`
INSERT INTO public.matchmaking_activities
    (id,title,description,status,published_at,campuses,registration_opens_at,registration_closes_at,results_at,starts_at,ends_at,days,created_at)
VALUES ('00000000-0000-4000-8000-000000000001','升级测试','合成活动','published',now(),'["wushan"]',
    now()-interval '2 days',now()-interval '1 day',now()-interval '1 hour',now(),now()+interval '1 day',1,now());
INSERT INTO public.matchmaking_pairs (id,activity_id,status,score,score_details,algorithm,published_at,created_at)
VALUES ('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000001','published',80,'{}','legacy',now(),now()),
       ('00000000-0000-4000-8000-000000000003','00000000-0000-4000-8000-000000000001','draft',80,'{}','legacy',NULL,now());
`).Error)
	}
	if baseline == 25 {
		must(db.Exec(`
INSERT INTO public.accounts(id,nickname) VALUES
('00000000-0000-4000-8000-000000000011','合成选用者'),
('00000000-0000-4000-8000-000000000012','合成未受影响者');
INSERT INTO public.matchmaking_traits(id,name,status,version,evidence,reason,created_at) VALUES
('00000000-0000-4000-8000-000000000021','合成拒绝词','rejected',2,'{}','保留审核依据',now()),
('00000000-0000-4000-8000-000000000022','合成通过词','approved',1,'{}','',now());
INSERT INTO public.matchmaking_trait_selections(account_id,trait_id,created_at) VALUES
('00000000-0000-4000-8000-000000000011','00000000-0000-4000-8000-000000000021',now()),
('00000000-0000-4000-8000-000000000011','00000000-0000-4000-8000-000000000022',now()),
('00000000-0000-4000-8000-000000000012','00000000-0000-4000-8000-000000000022',now());
`).Error)
	}
	must(RunApplication(ctx, db, log, "up"))
	if baseline == 25 {
		var rejectedCount, approvedCount, version int64
		must(db.Model(&entity.MatchmakingTraitSelection{}).Where("trait_id=?", "00000000-0000-4000-8000-000000000021").Count(&rejectedCount).Error)
		must(db.Model(&entity.MatchmakingTraitSelection{}).Where("trait_id=?", "00000000-0000-4000-8000-000000000022").Count(&approvedCount).Error)
		must(db.Model(&entity.Account{}).Select("version").Where("id=?", "00000000-0000-4000-8000-000000000011").Scan(&version).Error)
		if rejectedCount != 0 || approvedCount != 2 || version != 2 {
			t.Fatal("历史驳回清理错误", rejectedCount, approvedCount, version)
		}
		must(db.Model(&entity.Account{}).Select("version").Where("id=?", "00000000-0000-4000-8000-000000000012").Scan(&version).Error)
		if version != 1 {
			t.Fatal("未选用驳回词的账号版本被修改")
		}
		var trait entity.MatchmakingTrait
		must(db.Where("id=?", "00000000-0000-4000-8000-000000000021").Take(&trait).Error)
		if trait.Status != "rejected" || trait.Reason != "保留审核依据" {
			t.Fatal("清理删除了审核依据")
		}
	}

	if baseline == 19 {
		var pairs []string
		must(db.Table("public.conversations").Where("matchmaking_pair_id IS NOT NULL").Pluck("matchmaking_pair_id", &pairs).Error)
		if len(pairs) != 1 || pairs[0] != "00000000-0000-4000-8000-000000000002" {
			t.Fatalf("迁移只应为已发布配对补建会话：%v", pairs)
		}
	}
	if !db.Migrator().HasColumn(&entity.MatchmakingActivity{}, "tasks_published_at") {
		t.Fatal("0019未创建题库冻结字段")
	}
	var versions []entity.SchemaMigration
	must(db.Order("version").Find(&versions).Error)
	if len(versions) != len(entries) {
		t.Fatalf("升级后迁移账本版本数错误：%d", len(versions))
	}
	for i, row := range versions {
		if row.Version != int64(i+1) || row.Checksum != entries[i].checksum {
			t.Fatalf("升级后迁移账本记录不符：%d", row.Version)
		}
	}
	must(RunApplication(ctx, db, log, "up"))
	assertComments(t, db)
}
