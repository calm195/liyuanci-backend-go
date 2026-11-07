package adminauth

import (
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

// TestPostgresDashboard 以连接局部临时表验证真实PostgreSQL分桶、去重和删除边界，不改业务数据。
func TestPostgresDashboard(t *testing.T) {
	dsn := testconfig.Value("adminauth_test_dsn")
	if dsn == "" {
		t.Skip("需要显式PostgreSQL测试配置")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("测试数据库连接失败")
	}
	pool, _ := db.DB()
	defer pool.Close()
	err = db.Transaction(func(tx *gorm.DB) error {
		statements := []string{
			"CREATE TEMP TABLE dashboard_accounts(id text,created_at timestamptz,lifecycle text,verification text) ON COMMIT DROP",
			"CREATE TEMP TABLE dashboard_identity_reviews(account_id text,reviewed_at timestamptz,status text) ON COMMIT DROP",
			"CREATE TEMP TABLE dashboard_wishes(id text,created_at timestamptz,state text,draft_deleted_at timestamptz) ON COMMIT DROP",
			"CREATE TEMP TABLE dashboard_story_versions(wish_id text,created_at timestamptz) ON COMMIT DROP",
			"CREATE TEMP TABLE dashboard_resource_materials(created_at timestamptz) ON COMMIT DROP",
			`INSERT INTO dashboard_accounts VALUES ('a','2026-10-01','active','verified'),('b','2026-11-06T16:00:00Z','active','unverified'),('c','2026-11-07','deleting','verified')`,
			`INSERT INTO dashboard_identity_reviews VALUES ('a','2026-10-15','approved'),('a','2026-10-16','approved'),('c','2026-11-07','approved')`,
			`INSERT INTO dashboard_wishes VALUES ('w','2026-10-01','fulfilled',NULL),('deleted','2026-10-01','draft','2026-11-01')`,
			`INSERT INTO dashboard_story_versions VALUES ('w','2026-11-06T16:00:00Z'),('w','2026-11-07T01:00:00Z')`,
			`INSERT INTO dashboard_resource_materials VALUES ('2026-10-01')`,
		}
		for _, stmt := range statements {
			if e := tx.Exec(stmt).Error; e != nil {
				return e
			}
		}
		sql := dashboardSQL
		for _, name := range []string{"accounts", "identity_reviews", "wishes", "story_versions", "resource_materials"} {
			sql = strings.ReplaceAll(sql, "public."+name, "pg_temp.dashboard_"+name)
		}
		now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
		for _, period := range []string{"month", "year"} {
			var rows []DashboardPoint
			if e := tx.Raw(sql, now, period).Scan(&rows).Error; e != nil {
				return e
			}
			count := 31
			if period == "year" {
				count = 13
			}
			if len(rows) != count {
				t.Fatalf("时间桶数错误：%s %d", period, len(rows))
			}
			summary := rows[len(rows)-1]
			if summary.Date != "summary" || summary.RegisteredUsers != 2 || summary.VerifiedUsers != 1 || summary.NewUsers != 1 || summary.Resources != 1 || summary.Wishes != 1 || summary.FulfilledWishes != 1 {
				t.Fatalf("汇总或去重错误：%+v", summary)
			}
			if period == "month" {
				yesterday := rows[len(rows)-3]
				today := rows[len(rows)-2]
				if yesterday.Date != "2026-11-06" || yesterday.RegisteredUsers != 1 || yesterday.FulfilledWishes != 0 || today.Date != "2026-11-07" || today.NewUsers != 1 || today.FulfilledWishes != 1 {
					t.Fatalf("北京时间午夜边界错误：%+v %+v", yesterday, today)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
