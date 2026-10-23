package privacy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// TestReceiptAuthentication 在数据库访问前拒绝截断、跨用途及篡改回执，防止随机UUID枚举状态。
func TestReceiptAuthentication(t *testing.T) {
	s := &Service{logger: zap.NewNop(), cfg: Config{Key: bytes.Repeat([]byte{1}, 32), Command: command.Config{Timeout: time.Second}}}
	id := uuid.NewString()
	handle := s.receipt(id)
	other := &Service{cfg: Config{Key: bytes.Repeat([]byte{2}, 32)}}
	for _, h := range []string{"", id, handle + "x", other.receipt(id), strings.Replace(handle, id, uuid.NewString(), 1), "bad." + handle} {
		if _, e := s.Status(context.Background(), "request", h); !errors.Is(e, command.ErrNotFound) {
			t.Fatal("未拒绝非法回执", e)
		}
	}
	if s.jobID("account", id, "one") == s.jobID("evidence", id, "one") || s.jobID("account", id, "one") == s.jobID("account", id, "two") {
		t.Fatal("删除标识未按用途和键隔离")
	}
}

// TestDeletionValidation 校验失败不得触碰日志、主体或数据库。
func TestDeletionValidation(t *testing.T) {
	s := &Service{logger: zap.NewNop()}
	for _, in := range []DeletionInput{{}, {Confirmation: "yes", ExpectedVersion: 1}, {Confirmation: "delete_account"}} {
		if _, e := s.Request(context.Background(), "token", "key", "request", in); !errors.Is(e, command.ErrInvalid) {
			t.Fatal("接受非法确认", e)
		}
	}
	for _, in := range []EvidenceInput{{}, {Reason: " ", ExpectedVersion: 1}, {Reason: strings.Repeat("a", 2001), ExpectedVersion: 1}} {
		if _, e := s.DeleteEvidence(context.Background(), "token", "key", "request", uuid.NewString(), in); !errors.Is(e, command.ErrInvalid) {
			t.Fatal("接受非法删除理由", e)
		}
	}
	if _, e := New(nil, nil, nil, nil, zap.NewNop(), Config{}); !errors.Is(e, command.ErrInvalid) {
		t.Fatal("缺失依赖未关闭")
	}
}

// TestEraserFailureBoundary 外部panic与取消不允许变成清理完成。
func TestEraserFailureBoundary(t *testing.T) {
	if e := eraseSafely(context.Background(), panicEraser{}, Intent{}, nil); e == nil {
		t.Fatal("panic被当作完成")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := eraseSafely(ctx, emptyEraser{}, Intent{}, nil); !errors.Is(e, context.Canceled) {
		t.Fatal("取消被当作完成", e)
	}
}

type panicEraser struct{}

func (panicEraser) Erase(context.Context, Intent, []Object) error { panic("synthetic secret") }

type emptyEraser struct{}

func (emptyEraser) Erase(context.Context, Intent, []Object) error { return nil }
