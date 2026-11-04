package privacy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// TestJournalSnapshotCapacity 直接验证存储层的32MiB完整快照边界，包括编码换行、替换保护和确认后重启读取。
// 密文真实性由TestJournalPersistence验证；此处使用大引用构造精确尺寸，不创建或记录个人明文。
func TestJournalSnapshotCapacity(t *testing.T) {
	ctx := context.Background()
	core, logs := observer.New(zap.InfoLevel)
	path := filepath.Join(t.TempDir(), "deletions.json")
	j := &FileJournal{path: path, log: zap.New(core)}
	row := journalRecord{ID: "old", Envelope: securedata.Envelope{KeyRef: ""}}
	base, err := json.Marshal(map[string]journalRecord{"old": row})
	if err != nil {
		t.Fatal(err)
	}
	row.Envelope.KeyRef = strings.Repeat("x", journalMaxBytes-len(base)-1)
	if err = j.access(ctx, true, func(rows map[string]journalRecord) error {
		rows["old"] = row
		return nil
	}); err != nil {
		t.Fatal("精确上限快照被拒绝", err)
	}
	hash := func() [32]byte {
		t.Helper()
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		h := sha256.New()
		if _, err = io.Copy(h, file); err != nil {
			t.Fatal(err)
		}
		return [32]byte(h.Sum(nil))
	}
	before := hash()
	if err = j.access(ctx, true, func(rows map[string]journalRecord) error {
		changed := rows["old"]
		changed.Envelope.KeyRef += "x"
		rows["old"] = changed
		return nil
	}); err == nil {
		t.Fatal("发布了超过读取上限的快照")
	}
	if hash() != before {
		t.Fatal("拒绝超限时修改了原账本")
	}
	entries := logs.FilterMessage("文件删除日志操作完成").All()
	if len(entries) != 2 || entries[1].ContextMap()["capacity_exceeded"] != true {
		t.Fatal("容量拒绝没有安全完成日志")
	}
	// false变为true会减少一字节，满容量后仍可完成确认，不能把既有任务卡死。
	if err = j.Confirm(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	restarted := &FileJournal{path: path, log: zap.NewNop()}
	if err = restarted.access(ctx, false, func(rows map[string]journalRecord) error {
		if !rows["old"].Confirmed || len(rows) != 1 {
			t.Fatal("重启后确认或历史记录丢失")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), "journal-pending-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("临时快照未清理", leftovers, err)
	}
}
