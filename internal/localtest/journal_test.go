package localtest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// TestJournalPersistence 验证多实例竞争、重启、已确认重放及篡改失败关闭。
func TestJournalPersistence(t *testing.T) {
	ctx := context.Background()
	log := zap.NewNop()
	codec, e := securedata.New(Keys{bytes.Repeat([]byte{7}, 32), log}, log)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	if e = os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "journal.json")
	first, e := NewJournal(path, codec, log)
	if e != nil {
		t.Fatal(e)
	}
	second, e := NewJournal(path, codec, log)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			j := first
			if i%2 == 1 {
				j = second
			}
			errs <- j.Append(ctx, privacy.Intent{ID: fmt.Sprintf("%03d", i), Scope: "account", Target: "私有测试目标"})
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	rows, e := first.AllIntents(ctx, "", 100)
	if e != nil || len(rows) != 20 {
		t.Fatal("并发意图丢失", len(rows), e)
	}
	if e = second.Confirm(ctx, "000"); e != nil {
		t.Fatal(e)
	}
	restarted, e := NewJournal(path, codec, log)
	if e != nil {
		t.Fatal(e)
	}
	pending, e := restarted.Pending(ctx, "", 100)
	if e != nil || len(pending) != 19 {
		t.Fatal("确认状态未持久化", e)
	}
	rows, e = restarted.AllIntents(ctx, "", 100)
	if e != nil || len(rows) != 20 {
		t.Fatal("确认后丢失恢复意图", e)
	}
	if e = restarted.Append(ctx, privacy.Intent{ID: "000", Scope: "account", Target: "其他目标"}); e == nil {
		t.Fatal("同标识修改了目标")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("私有测试目标")) {
		t.Fatal("日志保存了明文目标")
	}
	if e = os.WriteFile(path, []byte("broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.AllIntents(ctx, "", 100); e == nil {
		t.Fatal("损坏账本被当为空账本")
	}
	if e = restarted.Append(ctx, privacy.Intent{ID: "new", Scope: "account", Target: "new"}); e == nil {
		t.Fatal("损坏账本被覆盖")
	}
}

// TestLocalKeys 验证重启兼容既有开发引用，错误密钥和篡改均拒绝。
func TestLocalKeys(t *testing.T) {
	ctx := context.Background()
	key := bytes.Repeat([]byte{9}, 32)
	k := Keys{key, zap.NewNop()}
	ref, wrapped, e := k.Wrap(ctx, []byte("测试数据密钥"))
	if e != nil {
		t.Fatal(e)
	}
	out, e := (Keys{key, zap.NewNop()}).Unwrap(ctx, ref, wrapped)
	if e != nil || string(out) != "测试数据密钥" {
		t.Fatal("重启解密失败", e)
	}
	if _, e = (Keys{bytes.Repeat([]byte{8}, 32), zap.NewNop()}).Unwrap(ctx, ref, wrapped); e == nil {
		t.Fatal("接受错误密钥")
	}
	wrapped[len(wrapped)-1] ^= 1
	if _, e = k.Unwrap(ctx, ref, wrapped); e == nil {
		t.Fatal("接受篡改")
	}
}
