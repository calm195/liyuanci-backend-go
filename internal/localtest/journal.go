package localtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// Journal 是本机加密删除意图账本；通过进程间锁和原子替换保留重启状态，不代表独立故障域。
type Journal struct {
	path  string
	codec *securedata.Codec
	log   *zap.Logger
}
type journalRecord struct {
	ID        string
	Envelope  securedata.Envelope
	Confirmed bool
}

// NewJournal 只装配已有私有目录中的账本，不生成密钥或连接第二个数据库。
func NewJournal(path string, codec *securedata.Codec, log *zap.Logger) (*Journal, error) {
	if !filepath.IsAbs(path) || codec == nil || log == nil {
		return nil, command.ErrInvalid
	}
	info, e := os.Stat(filepath.Dir(path))
	if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, command.ErrInvalid
	}
	return &Journal{path, codec, log}, nil
}

func (j *Journal) access(ctx context.Context, write bool, fn func(map[string]journalRecord) error) (err error) {
	defer done(j.log, "journal_access", time.Now(), &err)
	lock, e := os.OpenFile(j.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return command.ErrUnavailable
	}
	defer lock.Close()
	for {
		e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			break
		}
		if !errors.Is(e, syscall.EWOULDBLOCK) && !errors.Is(e, syscall.EAGAIN) {
			return command.ErrUnavailable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if e = ctx.Err(); e != nil {
		return e
	}
	rows := map[string]journalRecord{}
	file, e := os.Open(j.path)
	if e == nil {
		raw, readErr := io.ReadAll(io.LimitReader(file, 32<<20+1))
		file.Close()
		if readErr != nil || len(raw) > 32<<20 || json.Unmarshal(raw, &rows) != nil || rows == nil {
			return command.ErrUnavailable
		}
	} else if !os.IsNotExist(e) {
		return command.ErrUnavailable
	}
	if e = fn(rows); e != nil {
		return e
	}
	if !write {
		return nil
	}
	file, e = os.CreateTemp(filepath.Dir(j.path), "journal-pending-")
	if e != nil {
		return command.ErrUnavailable
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if e = file.Chmod(0600); e != nil {
		return e
	}
	if e = json.NewEncoder(file).Encode(rows); e != nil {
		return e
	}
	if e = file.Sync(); e != nil {
		return e
	}
	if e = file.Close(); e != nil {
		return e
	}
	if e = os.Rename(file.Name(), j.path); e != nil {
		return e
	}
	directory, e := os.Open(filepath.Dir(j.path))
	if e != nil {
		return e
	}
	defer directory.Close()
	return directory.Sync()
}

// Append 幂等保存加密意图；相同标识不得改变删除目标。
func (j *Journal) Append(ctx context.Context, in privacy.Intent) error {
	if in.ID == "" || in.Target == "" || (in.Scope != "account" && in.Scope != "evidence") {
		return command.ErrInvalid
	}
	return j.access(ctx, true, func(rows map[string]journalRecord) error {
		if row, ok := rows[in.ID]; ok {
			old, e := j.decode(ctx, row)
			if e != nil {
				return e
			}
			if old != in {
				return command.ErrConflict
			}
			return nil
		}
		raw, e := json.Marshal(in)
		if e != nil {
			return e
		}
		defer clear(raw)
		envelope, e := j.codec.Encrypt(ctx, securedata.Binding{Purpose: "local_deletion_intent", ObjectID: in.ID, Version: 1}, raw)
		if e != nil {
			return e
		}
		rows[in.ID] = journalRecord{ID: in.ID, Envelope: envelope}
		return nil
	})
}
func (j *Journal) decode(ctx context.Context, row journalRecord) (out privacy.Intent, err error) {
	raw, e := j.codec.Decrypt(ctx, securedata.Binding{Purpose: "local_deletion_intent", ObjectID: row.ID, Version: 1}, row.Envelope)
	if e != nil {
		return out, command.ErrUnavailable
	}
	defer clear(raw)
	if json.Unmarshal(raw, &out) != nil || out.ID != row.ID {
		return privacy.Intent{}, command.ErrUnavailable
	}
	return out, nil
}
func (j *Journal) list(ctx context.Context, after string, limit int, all bool) (out []privacy.Intent, err error) {
	if limit < 1 || limit > 1000 {
		return nil, command.ErrInvalid
	}
	out = []privacy.Intent{}
	err = j.access(ctx, false, func(rows map[string]journalRecord) error {
		ids := []string{}
		for id, row := range rows {
			if id > after && (all || !row.Confirmed) {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		if len(ids) > limit {
			ids = ids[:limit]
		}
		for _, id := range ids {
			in, e := j.decode(ctx, rows[id])
			if e != nil {
				return e
			}
			out = append(out, in)
		}
		return nil
	})
	if err != nil {
		out = nil
	}
	return
}

// Pending 返回未确认的持久意图。
func (j *Journal) Pending(ctx context.Context, after string, limit int) ([]privacy.Intent, error) {
	return j.list(ctx, after, limit, false)
}

// AllIntents 包含已确认项，供本地重启或恢复检查使用。
func (j *Journal) AllIntents(ctx context.Context, after string, limit int) ([]privacy.Intent, error) {
	return j.list(ctx, after, limit, true)
}

// Confirm 仅标记已完成清理的意图，不删除恢复记录。
func (j *Journal) Confirm(ctx context.Context, id string) error {
	return j.access(ctx, true, func(rows map[string]journalRecord) error {
		row, ok := rows[id]
		if !ok {
			return command.ErrNotFound
		}
		row.Confirmed = true
		rows[id] = row
		return nil
	})
}
