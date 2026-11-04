package migration

import (
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// modelSchemas 仅解析实体映射，绝不通过 ORM 推导或执行 DDL。
func modelSchemas(tx *gorm.DB, models []any) ([]*schema.Schema, error) {
	out := make([]*schema.Schema, 0, len(models))
	for _, model := range models {
		stmt := &gorm.Statement{DB: tx}
		if err := stmt.Parse(model); err != nil {
			return nil, fmt.Errorf("实体映射解析失败：%w", err)
		}
		out = append(out, stmt.Schema)
	}
	return out, nil
}

// validateModelColumns 拒绝缺表或缺字段的漂移，不以重跑 up 静默修复现场。
// 类型、约束、默认值与索引由版本 SQL 定义，并在原生集成中验证。
func validateModelColumns(tx *gorm.DB, models []any) error {
	schemas, err := modelSchemas(tx, models)
	if err != nil {
		return err
	}
	var columns []struct{ TableName, ColumnName string }
	if err := tx.Raw(`SELECT n.nspname||'.'||c.relname AS table_name,a.attname AS column_name
 FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid
 WHERE n.nspname IN ('public','evidence') AND c.relkind IN ('r','p')
 AND a.attnum>0 AND NOT a.attisdropped`).Scan(&columns).Error; err != nil {
		return fmt.Errorf("数据库结构读取失败：%w", err)
	}
	actual := map[string]map[string]bool{}
	for _, column := range columns {
		if actual[column.TableName] == nil {
			actual[column.TableName] = map[string]bool{}
		}
		actual[column.TableName][column.ColumnName] = true
	}
	for _, model := range schemas {
		for _, field := range model.Fields {
			if field.DBName != "" && !actual[model.Table][field.DBName] {
				return fmt.Errorf("数据库结构与版本不符，缺少字段 %s.%s；须通过版本 SQL 修复", model.Table, field.DBName)
			}
		}
	}
	return nil
}

// guardRollback 对全部已存在的业务表先锁后验，兼容只应用部分历史版本的库。
// 两张初始化基准表只允许原始未使用状态；迁移账本不属于业务数据。
func guardRollback(tx *gorm.DB) error {
	schemas, err := modelSchemas(tx, businessModels())
	if err != nil {
		return err
	}
	var existing []struct {
		TableName string
		Forced    bool
	}
	if err := tx.Raw(`SELECT n.nspname||'.'||c.relname AS table_name,c.relforcerowsecurity AS forced FROM pg_catalog.pg_class c
 JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname IN ('public','evidence') AND c.relkind IN ('r','p')`).Scan(&existing).Error; err != nil {
		return fmt.Errorf("回退表目录读取失败：%w", err)
	}
	known := map[string]bool{}
	forced := map[string]bool{}
	for _, table := range existing {
		known[table.TableName] = true
		forced[table.TableName] = table.Forced
	}
	var tables []string
	for _, model := range schemas {
		if model.Table != "public.schema_migrations" && known[model.Table] {
			tables = append(tables, model.Table)
		}
	}
	sort.Strings(tables)
	if len(tables) == 0 {
		return nil
	}
	quoted := make([]string, len(tables))
	for i, table := range tables {
		quoted[i] = tx.Statement.Quote(clause.Table{Name: table})
	}
	if err := tx.Exec("LOCK TABLE " + strings.Join(quoted, ", ") + " IN ACCESS EXCLUSIVE MODE").Error; err != nil {
		return fmt.Errorf("回退保护锁定失败：%w", err)
	}
	// 表所有者在排他锁内临时取消 FORCE，避免 RLS 把非空表伪装为空表。
	// 失败则事务恢复原设置；检查通过后也恢复 FORCE，再执行版本回退。
	if err := tx.Exec("SET LOCAL row_security = off").Error; err != nil {
		return err
	}
	for i, table := range tables {
		if forced[table] {
			if err := tx.Exec("ALTER TABLE " + quoted[i] + " NO FORCE ROW LEVEL SECURITY").Error; err != nil {
				return fmt.Errorf("回退保护需要表所有权：%w", err)
			}
		}
		predicate := "true"
		switch table {
		case "public.interaction_block_policy":
			predicate = "opaque_enabled OR switched_at IS NOT NULL"
		case "public.wordlist_heads":
			predicate = "version <> 1 OR effective_id IS NOT NULL"
		}
		var populated bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM " + quoted[i] + " WHERE " + predicate + ")").Scan(&populated).Error; err != nil {
			return fmt.Errorf("回退数据检查失败：%w", err)
		}
		if populated {
			return fmt.Errorf("业务表 %s 非空或已启用，拒绝整体回退", table)
		}
	}
	for i, table := range tables {
		if forced[table] {
			if err := tx.Exec("ALTER TABLE " + quoted[i] + " FORCE ROW LEVEL SECURITY").Error; err != nil {
				return fmt.Errorf("回退保护恢复行安全失败：%w", err)
			}
		}
	}
	return nil
}
