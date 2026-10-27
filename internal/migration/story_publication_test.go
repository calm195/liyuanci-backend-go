package migration

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// TestPostgresStoryPublication 验证REQ-008/018的旧版升级：仅已审故事建立切换位置，重跑不刷新历史位置。
func TestPostgresStoryPublication(t *testing.T) {
	db := schemaDB(t, "APP_STORY_PUBLICATION_TEST_DSN")
	ctx, logger := context.Background(), zap.NewNop()
	if e := Run(ctx, db, logger, "up"); e != nil {
		t.Fatal(e)
	}
	// 构造仅缺新增版本的已部署结构，不改写任何历史迁移输入。
	if e := db.Transaction(func(tx *gorm.DB) error {
		for _, sql := range []string{
			"ALTER TABLE public.story_versions DROP COLUMN published_at",
			"DELETE FROM public.schema_migrations WHERE version=20",
			"INSERT INTO public.accounts(id,nickname,verification) VALUES ('60000000-0000-4000-8000-000000000001','合成作者','verified')",
			"INSERT INTO public.wishes(id,owner_id,state,current_content_version,category,campus) VALUES ('61000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000001','fulfilled',1,'other','wushan')",
			"INSERT INTO public.wish_versions(wish_id,content_version,body_cipher,school_detection_version) VALUES ('61000000-0000-4000-8000-000000000001',1,'{}','pending')",
			`INSERT INTO public.story_versions(wish_id,story_version,author_id,body_cipher,public_body,moderation_status,school_related,school_detection_state,school_detection_version) VALUES ('61000000-0000-4000-8000-000000000001',1,'60000000-0000-4000-8000-000000000001','{}','合成已审正文','approved',false,'completed','synthetic'),('61000000-0000-4000-8000-000000000001',2,'60000000-0000-4000-8000-000000000001','{}',NULL,'pending',NULL,'pending','pending')`,
		} {
			if e := tx.Exec(sql).Error; e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	before := time.Now().Add(-time.Second)
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
	if len(rows) != 2 || rows[0].PublishedAt == nil || rows[0].PublishedAt.Before(before) || rows[1].PublishedAt != nil {
		t.Fatal("切换时间回填范围错误", rows)
	}
	first := *rows[0].PublishedAt
	if e := Run(ctx, db, logger, "up"); e != nil {
		t.Fatal(e)
	}
	var again time.Time
	if e := db.Raw("SELECT published_at FROM public.story_versions WHERE story_version=1").Scan(&again).Error; e != nil || !again.Equal(first) {
		t.Fatal("重跑刷新了公开位置", e)
	}
	if e := ApplyPermissions(ctx, db, logger); e != nil {
		t.Fatal(e)
	}
	var canPublish bool
	if e := db.Raw("SELECT has_column_privilege('liyuanci_admin_api','public.story_versions','published_at','UPDATE')").Scan(&canPublish).Error; e != nil || !canPublish {
		t.Fatal("审核身份缺少公开时间写入权限", e)
	}
	assertComments(t, db)
}
