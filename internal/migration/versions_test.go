package migration

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestVersionCatalog 拒绝重复、断号和不成对的制品，避免新增 SQL 被遗漏或静默忽略。
func TestVersionCatalog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		valid bool
	}{
		{"连续配对", []string{"0001_a.up.sql", "0001_a.down.sql", "0002_b.up.sql", "0002_b.down.sql"}, true},
		{"缺少回退", []string{"0001_a.up.sql"}, false},
		{"孤立回退", []string{"0001_a.down.sql"}, false},
		{"断号", []string{"0002_a.up.sql", "0002_a.down.sql"}, false},
		{"重复版本", []string{"0001_a.up.sql", "0001_a.down.sql", "0001_b.up.sql", "0001_b.down.sql"}, false},
		{"空目录", nil, false},
		{"错误命名", []string{"bad.up.sql", "bad.down.sql"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := fstest.MapFS{}
			for _, p := range tc.paths {
				fs["sql/"+p] = &fstest.MapFile{Data: []byte("SELECT 1;")}
			}
			_, err := readVersions(fs)
			if (err == nil) != tc.valid {
				t.Fatalf("目录校验结果不符：%v", err)
			}
		})
	}
	entries, err := loadVersions()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 23 {
		t.Fatalf("迁移版本数量错误：%d", len(entries))
	}
	// 权限重放只能管理策略与授权，不能再偷偷创建表、字段或函数。
	permissions, err := files.ReadFile("sql/application_permissions.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{"CREATE FUNCTION", "REPLACE FUNCTION", "CREATE TABLE", "ALTER COLUMN"} {
		if strings.Contains(strings.ToUpper(string(permissions)), ddl) {
			t.Fatalf("权限脚本包含结构定义：%s", ddl)
		}
	}
}
