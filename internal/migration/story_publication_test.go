package migration

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

// TestPostgresStoryPublication 验证公开时间由业务保存，迁移重跑不刷新排序位置。
func TestPostgresStoryPublication(t *testing.T) {
	db := schemaDB(t, "story_publication_test_dsn")
	ctx, logger := context.Background(), zap.NewNop()
	if e := Run(ctx, db, logger, "up"); e != nil {
		t.Fatal(e)
	}
	// 当前结构直接保存审核公开时间，迁移重跑不得编造或刷新业务时间。
	published := time.Now().UTC().Truncate(time.Microsecond)
	if e := db.Transaction(func(tx *gorm.DB) error {
		for _, sql := range []string{
			"INSERT INTO public.accounts(id,nickname,verification) VALUES ('60000000-0000-4000-8000-000000000001','合成作者','verified')",
			"INSERT INTO public.wishes(id,owner_id,state,current_content_version,category,campus) VALUES ('61000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000001','fulfilled',1,'other','wushan')",
			"INSERT INTO public.wish_versions(wish_id,content_version,body_cipher,school_detection_version) VALUES ('61000000-0000-4000-8000-000000000001',1,'{}','pending')",
			`INSERT INTO public.story_versions(wish_id,story_version,author_id,body_cipher,public_body,moderation_status,school_related,school_detection_state,school_detection_version) VALUES ('61000000-0000-4000-8000-000000000001',1,'60000000-0000-4000-8000-000000000001','{}','合成已审正文','approved',false,'completed','synthetic'),('61000000-0000-4000-8000-000000000001',2,'60000000-0000-4000-8000-000000000001','{}',NULL,'pending',NULL,'pending','pending')`,
		} {
			if e := tx.Exec(sql).Error; e != nil {
				return e
			}
		}
		return tx.Exec("UPDATE public.story_versions SET published_at=? WHERE story_version=1", published).Error
	}); e != nil {
		t.Fatal(e)
	}
	if e := Run(ctx, db, logger, "up"); e != nil {
		t.Fatal(e)
	}
	var rows []struct {
		StoryVersion int64
		PublishedAt  *time.Time
	}
	if e := db.Table("public.story_versions").Select("story_version,published_at").Order("story_version").Scan(&rows).Error; e != nil {
		t.Fatal(e)
	}
	if len(rows) != 2 || rows[0].PublishedAt == nil || !rows[0].PublishedAt.Equal(published) || rows[1].PublishedAt != nil {
		t.Fatal("公开时间存储错误", rows)
	}
	first := *rows[0].PublishedAt
	if e := Run(ctx, db, logger, "up"); e != nil {
		t.Fatal(e)
	}
	var again time.Time
	if e := db.Raw("SELECT published_at FROM public.story_versions WHERE story_version=1").Scan(&again).Error; e != nil || !again.Equal(first) {
		t.Fatal("重跑刷新了公开位置", e)
	}
	if e := testconfig.ApplyDatabaseRoles(ctx, db, logger); e != nil {
		t.Fatal(e)
	}
	var canPublish bool
	if e := db.Raw("SELECT has_column_privilege('liyuanci_admin_api','public.story_versions','published_at','UPDATE')").Scan(&canPublish).Error; e != nil || !canPublish {
		t.Fatal("审核身份缺少公开时间写入权限", e)
	}
	assertComments(t, db)
}
