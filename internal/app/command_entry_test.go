package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

// TestWorkerModes 拒绝未知职责、歧义检查及非删除恢复，避免参数被静默忽略。
func TestWorkerModes(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		options WorkerOptions
		valid   bool
	}{
		{"普通消费", "card", WorkerOptions{}, true},
		{"离线检查", "content", WorkerOptions{CheckConfig: true}, true},
		{"依赖检查", "whispers", WorkerOptions{Check: true}, true},
		{"账号恢复", "account", WorkerOptions{Recover: true}, true},
		{"证据恢复", "evidence", WorkerOptions{Recover: true}, true},
		{"未知职责", "unknown", WorkerOptions{}, false},
		{"缺少职责", "", WorkerOptions{}, false},
		{"扫描恢复", "card", WorkerOptions{Recover: true}, false},
		{"两种检查", "account", WorkerOptions{Check: true, CheckConfig: true}, false},
		{"检查与恢复", "account", WorkerOptions{Check: true, Recover: true}, false},
		{"离线与恢复", "account", WorkerOptions{CheckConfig: true, Recover: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateWorkerOptions(tc.options, tc.kind); (err == nil) != tc.valid {
				t.Fatalf("参数校验结果不符：%v", err)
			}
		})
	}
}

// developmentCommandDocument 使用不可达中间件，验证离线模式不连接、不创建对象或账本。
func developmentCommandDocument(t *testing.T) (config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Environment, cfg.Mode = "integration", "development"
	cfg.MasterKey = bytes.Repeat([]byte{87}, 32)
	cfg.Database = databaseSettings("host=127.0.0.1 port=1 user=application password=synthetic dbname=synthetic sslmode=disable")
	cfg.Redis.Enabled, cfg.Redis.Address = true, "127.0.0.1:1"
	cfg.Development.AdminPassword = "synthetic-long-password"
	cfg.Development.ObjectDirectory = filepath.Join(dir, "objects")
	cfg.Development.JournalFile = filepath.Join(dir, "journal", "deletions.json")
	cfg.Bootstrap.Login = "synthetic-admin"
	return cfg, filepath.Join(dir, "common.yaml")
}

// TestDevelopmentOfflineCommands 验证统一入口的开发离线检查、YAML默认职责与错误分流。
func TestDevelopmentOfflineCommands(t *testing.T) {
	cfg, path := developmentCommandDocument(t)
	cfg.Worker.Kind = "content"
	if err := os.WriteFile(path, mustYAML(t, cfg), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, kind := range []string{"", "card", "content", "account", "evidence"} {
		if err := RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: kind, CheckConfig: true}); err != nil {
			t.Fatalf("%s：%v", kind, err)
		}
	}
	if err := RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: "whispers", CheckConfig: true}); err == nil {
		t.Fatal("未启用树洞仍接受树洞任务")
	}
	if err := RunInitialization(ctx, path, true); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{cfg.Development.ObjectDirectory, filepath.Dir(cfg.Development.JournalFile)} {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("离线检查创建了状态目录：%s", target)
		}
	}
	cfg.Mode = "foundation"
	if err := os.WriteFile(path, mustYAML(t, cfg), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RunInitialization(ctx, path, true); err == nil {
		t.Fatal("健康模式接受业务初始化")
	}
	if err := RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: "card", CheckConfig: true}); err == nil {
		t.Fatal("健康模式接受任务")
	}
}

// TestPostgresDevelopmentCommands 使用临时原生库验证开发初始化幂等、检查不消费及两域恢复退出。
func TestPostgresDevelopmentCommands(t *testing.T) {
	dsn := testconfig.Value("whisper_text_test_dsn")
	if dsn == "" {
		t.Skip("需make integration提供隔离数据库")
	}
	db, shared, _ := applicationDatabase(t, dsn)
	cfg, path := developmentCommandDocument(t)
	cfg.Database = databaseSettings(shared)
	for _, directory := range []string{cfg.Development.ObjectDirectory, filepath.Dir(cfg.Development.JournalFile)} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, mustYAML(t, cfg), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := RunInitialization(ctx, path, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"card", "content", "account", "evidence"} {
		if err := RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: kind, Check: true}); err != nil {
			t.Fatalf("%s检查：%v", kind, err)
		}
	}
	for _, kind := range []string{"account", "evidence"} {
		if err := RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: kind, Recover: true}); err != nil {
			t.Fatalf("%s恢复：%v", kind, err)
		}
	}
	// 未来重试任务必须保留；检查不消费，恢复也不能把暂时无到期任务当作成功。
	job := entity.DeletionJob{JobID: uuid.NewString(), Scope: "account", Phase: "intent", NextAt: time.Now().Add(time.Hour)}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: "account", Check: true}); err != nil {
		t.Fatal(err)
	}
	var after entity.DeletionJob
	if err := db.Where("job_id=?", job.JobID).Take(&after).Error; err != nil || after.Phase != "intent" || after.Attempts != 0 {
		t.Fatal("检查消费或修改了删除任务", err)
	}
	if err := RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: "account", Recover: true}); err == nil {
		t.Fatal("未完成删除任务被误报为恢复成功")
	}

}
