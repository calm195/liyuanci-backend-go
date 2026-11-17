package migration

import (
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

// TestVersionCatalog 要求所有迁移从0001连续编号，拒绝断号、重复和未配对文件。
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
	if len(entries) != 33 || entries[len(entries)-1].version != 33 {
		t.Fatalf("迁移版本数量错误：%d", len(entries))
	}
	for _, last := range []int{33, 34, 35, 36} {
		catalog := fstest.MapFS{}
		for version := 1; version <= 32; version++ {
			for _, direction := range []string{"up", "down"} {
				catalog[fmt.Sprintf("sql/%04d_test.%s.sql", version, direction)] = &fstest.MapFile{Data: []byte("SELECT 1;")}
			}
		}
		for _, direction := range []string{"up", "down"} {
			catalog[fmt.Sprintf("sql/%04d_test.%s.sql", last, direction)] = &fstest.MapFile{Data: []byte("SELECT 1;")}
		}
		versions, e := readVersions(catalog)
		if (e == nil) != (last == 33) {
			t.Fatalf("连续编号边界 %d: %v", last, e)
		}
		if e == nil && versions[len(versions)-1].version != 33 {
			t.Fatal("实际版本号丢失")
		}
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
