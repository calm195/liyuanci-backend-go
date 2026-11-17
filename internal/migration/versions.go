package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

type versionEntry struct {
	version  int64
	name     string
	up, down []byte
	checksum string
}

// loadVersions 从成对文件建立连续版本目录，不为未交付原型保留编号缺口。
func loadVersions() ([]versionEntry, error) { return readVersions(files) }

func readVersions(source fs.FS) ([]versionEntry, error) {
	paths, err := fs.Glob(source, "sql/*.sql")
	if err != nil {
		return nil, fmt.Errorf("迁移目录读取失败：%w", err)
	}
	var entries []versionEntry
	nextVersion := int64(1)
	paired := map[string]bool{}
	for _, path := range paths {
		if !strings.HasSuffix(path, ".up.sql") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(path, "sql/"), ".up.sql")
		if len(name) < 6 || name[4] != '_' {
			return nil, fmt.Errorf("迁移文件名无效：%s", name)
		}
		version, e := strconv.ParseInt(name[:4], 10, 64)
		if e != nil || version != nextVersion {
			return nil, fmt.Errorf("迁移版本必须从0001连续编号：%s", name)
		}
		nextVersion++
		up, e := fs.ReadFile(source, path)
		if e != nil {
			return nil, fmt.Errorf("迁移 %s 读取失败：%w", name, e)
		}
		downPath := "sql/" + name + ".down.sql"
		down, e := fs.ReadFile(source, downPath)
		if e != nil {
			return nil, fmt.Errorf("迁移 %s 缺少回退文件：%w", name, e)
		}
		sum := sha256.Sum256(append(append([]byte(nil), up...), down...))
		entries = append(entries, versionEntry{version: version, name: name, up: up, down: down, checksum: hex.EncodeToString(sum[:])})
		paired[downPath] = true
	}
	for _, path := range paths {
		if strings.HasSuffix(path, ".down.sql") && !paired[path] {
			return nil, fmt.Errorf("回退文件缺少对应迁移：%s", path)
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("迁移目录不能为空")
	}
	return entries, nil
}

// requireCurrentVersion 防止只执行 permissions 而跳过结构升级或文件校验。
func requireCurrentVersion(tx *gorm.DB) error {
	entries, err := loadVersions()
	if err != nil {
		return err
	}
	var rows []entity.SchemaMigration
	if tx.Order("version").Find(&rows).Error != nil {
		return errors.New("权限配置需要先完成版本迁移")
	}
	if len(rows) != len(entries) {
		return errors.New("权限配置需要先升级到当前版本")
	}
	for i, row := range rows {
		if row.Version != entries[i].version || row.Checksum != entries[i].checksum {
			return errors.New("迁移版本或校验和不匹配")
		}
	}
	return nil
}
