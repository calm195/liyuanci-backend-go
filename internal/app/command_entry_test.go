package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/config"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
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
	cfg.Business.AdminPassword = "synthetic-long-password"
	cfg.Business.Runtime.ObjectDirectory = filepath.Join(dir, "objects")
	cfg.Business.Runtime.JournalFile = filepath.Join(dir, "journal", "deletions.json")
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
	if err := RunServer(ctx, ServerOptions{ConfigFile: path, CheckConfig: true}); err != nil {
		t.Fatal(err)
	}
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
	for _, target := range []string{cfg.Business.Runtime.ObjectDirectory, filepath.Dir(cfg.Business.Runtime.JournalFile)} {
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
	for _, directory := range []string{cfg.Business.Runtime.ObjectDirectory, filepath.Dir(cfg.Business.Runtime.JournalFile)} {
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

// TestPostgresDevelopmentRecoveryGate 验证分页扫描包含已确认意图，两域重删完成前开发API失败关闭。
func TestPostgresDevelopmentRecoveryGate(t *testing.T) {
	dsn := testconfig.Value("whisper_text_test_dsn")
	if dsn == "" {
		t.Skip("需make integration提供隔离数据库")
	}
	db, shared, _ := applicationDatabase(t, dsn)
	cfg, path := developmentCommandDocument(t)
	cfg.Database = databaseSettings(shared)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{cfg.Business.Runtime.ObjectDirectory, filepath.Dir(cfg.Business.Runtime.JournalFile)} {
		must(os.MkdirAll(dir, 0700))
	}
	must(os.WriteFile(path, mustYAML(t, cfg), 0600))
	loaded, err := config.Load(path)
	must(err)
	development, err := developmentSettings(loaded)
	must(err)
	ctx, log := context.Background(), zap.NewNop()
	journal, closeJournal, err := openDevelopmentJournal(ctx, development, log)
	must(err)
	defer closeJournal()
	// 首批为已删除目标，真正恢复回来的目标放在第二页，防止只检查第一页或未确认项。
	for i := range 100 {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		must(journal.Append(ctx, privacy.Intent{ID: id, Scope: "account", Target: uuid.NewString()}))
		must(journal.Confirm(ctx, id))
	}
	accountID, evidenceID := uuid.NewString(), uuid.NewString()
	must(db.Create(&entity.Account{ID: accountID, Nickname: "恢复测试账号"}).Error)
	must(db.Create(&entity.EvidenceCase{ID: evidenceID, Kind: "crisis", SourceRef: uuid.NewString()}).Error)
	for _, intent := range []privacy.Intent{
		{ID: "ffffffff-ffff-4fff-8fff-fffffffffffe", Scope: "account", Target: accountID},
		{ID: "ffffffff-ffff-4fff-8fff-ffffffffffff", Scope: "evidence", Target: evidenceID},
	} {
		must(journal.Append(ctx, intent))
		must(journal.Confirm(ctx, intent.ID))
	}
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer client.Close()
	open := func() error {
		_, closeApp, err := OpenDevelopment(ctx, "integration", development, client, log, func(context.Context) error { return nil })
		if closeApp != nil {
			must(closeApp())
		}
		return err
	}
	if open() == nil {
		t.Fatal("历史确认意图未阻止恢复后的开发API")
	}
	must(RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: "account", Recover: true}))
	if open() == nil {
		t.Fatal("只恢复账号域就开放了证据读取")
	}
	must(RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: "evidence", Recover: true}))
	must(open())
	// 正常恢复没有清掉历史意图；再次恢复旧快照目标仍须被阻断。
	must(db.Create(&entity.Account{ID: accountID, Nickname: "恢复测试账号"}).Error)
	if open() == nil {
		t.Fatal("重删后丢失了历史恢复屏障")
	}
	must(RunWorker(ctx, WorkerOptions{ConfigFile: path, Kind: "account", Recover: true}))
	must(open())
	must(os.WriteFile(development.JournalFile, []byte("broken"), 0600))
	if open() == nil {
		t.Fatal("损坏账本被当作无需恢复而开放读取")
	}
}

// TestFoundationServerStarts 验证公共基础配置可在没有中间件、主密钥和业务凭据时真实监听并取消退出。
func TestFoundationServerStarts(t *testing.T) {
	common, err := os.ReadFile(filepath.Join("..", "..", "configs", "common.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "common.yaml"), common, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "foundation.yaml")
	if err = os.WriteFile(path, []byte("server:\n  address: 127.0.0.1:0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err = RunServer(ctx, ServerOptions{ConfigFile: path}); err != nil {
		t.Fatal(err)
	}
}
