package entity

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// TestEntityCatalog 验证 T005/T008、REQ-020 的每张实体表和字段均有唯一结构体映射。
func TestEntityCatalog(t *testing.T) {
	models := append(BusinessModels(), &RecoveryDeleteJournal{}, &RecoverySchemaMigration{}, &FoundationProbeReceipt{})
	catalog := map[string]*schema.Schema{}
	for _, model := range models {
		parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		if catalog[parsed.Table] != nil {
			t.Fatalf("重复实体：%s", parsed.Table)
		}
		catalog[parsed.Table] = parsed
		if len(parsed.PrimaryFields) == 0 {
			t.Errorf("缺少主键：%s", parsed.Table)
		}
		for _, field := range parsed.Fields {
			if field.DBName != "" && field.Comment == "" {
				t.Errorf("缺少字段注释：%s.%s", parsed.Table, field.DBName)
			}
		}
	}
	paths, err := filepath.Glob("../migration/sql/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, "../migration/journal/0001_journal.up.sql", "../../migrations/integration/0001_probe.up.sql")
	columns := regexp.MustCompile(`COMMENT ON COLUMN ([a-z_.]+) IS`)
	seen := map[string]bool{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range columns.FindAllStringSubmatch(string(data), -1) {
			ref := match[1]
			if strings.HasPrefix(ref, "foundation_probe_receipts.") {
				ref = "public." + ref
			}
			dot := strings.LastIndex(ref, ".")
			table, column := ref[:dot], ref[dot+1:]
			seen[table] = true
			parsed := catalog[table]
			if parsed == nil {
				t.Errorf("缺少表实体：%s", table)
				continue
			}
			if parsed.FieldsByDBName[column] == nil {
				t.Errorf("缺少字段映射：%s", ref)
			}
		}
	}
	// 两张技术账本在迁移器中声明，其余映射必须有对应的 SQL 实体，防止误注册恢复库。
	for table := range catalog {
		if strings.HasSuffix(table, "schema_migrations") {
			continue
		}
		if !seen[table] {
			t.Errorf("实体无对应表：%s", table)
		}
	}
	session := catalog["public.sessions"]
	for _, column := range []string{"subject_kind", "subject_id"} {
		field := session.FieldsByDBName[column]
		if field.Creatable || field.Updatable || !field.IgnoreMigration {
			t.Errorf("生成列必须只读且不参与普通字段迁移：%s", column)
		}
	}
	for _, model := range BusinessModels() {
		parsed, _ := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		if strings.Contains(parsed.Table, "recovery_") || strings.Contains(parsed.Table, "probe_") {
			t.Errorf("隔离表混入业务实体：%s", parsed.Table)
		}
	}
}
