package moderation

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// WordlistRestore 从已归档版本创建恢复草案，发布前仍可检查和试跑。
type WordlistRestore struct {
	ExpectedHeadVersion int64 `json:"expected_head_version"`
}

// RestoreWordlist 将历史词条及上游来源复制至基于当前生效版本的新草案。
func (s *Service) RestoreWordlist(ctx context.Context, token, key, rid, id string, input WordlistRestore) (out Wordlist, err error) {
	defer s.done("wordlists.restore", rid, time.Now(), &err)
	if !validID(id) || input.ExpectedHeadVersion < 1 {
		return out, command.ErrInvalid
	}
	return s.changeWordlist(ctx, token, key, rid, wordlistChange{
		operation: "wordlist.restore", path: "/api/v1/admin/wordlists/" + id + "/restore",
		id: id, version: input.ExpectedHeadVersion, request: input,
		buildDraft: func(db *gorm.DB, _ entity.WordlistHead, source entity.WordlistVersion) (entity.WordlistVersion, []entity.WordlistEntry, error) {
			if source.Status != "archived" {
				return entity.WordlistVersion{}, nil, command.ErrConflict
			}
			entries, e := readWordlistEntries(db, source.ID)
			return entity.WordlistVersion{UpstreamVersion: source.UpstreamVersion, UpstreamDigest: source.UpstreamDigest}, entries, e
		},
	})
}

// WordlistPreview 比较当前所有生效规则与换入当前草案后的命中。
type WordlistPreview struct {
	Effective WordlistEvaluation `json:"effective"`
	Draft     WordlistEvaluation `json:"draft"`
}

// PreviewWordlist 使用生产扫描器试跑指定草案，测试内容不持久化。
func (s *Service) PreviewWordlist(ctx context.Context, token, rid, id, body string) (out WordlistPreview, err error) {
	defer s.done("wordlists.preview", rid, time.Now(), &err)
	if !validID(id) || !text(body, 4000) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorize(db.Statement.Context, db, token, command.Discovery{})
		if e != nil {
			return e
		}
		row, e := readList(db, id)
		if e != nil {
			return e
		}
		if row.Status != "draft" {
			return command.ErrConflict
		}
		effective, e := readRules(db)
		if e != nil {
			return e
		}
		draft := effective
		if e = applyWordlistSnapshot(db, row, &draft); e != nil {
			return e
		}
		out.Effective, e = scanWordlists(body, effective)
		if e != nil {
			return e
		}
		out.Draft, e = scanWordlists(body, draft)
		if e != nil {
			return command.ErrInvalid
		}
		return audit(db, actor, id, "wordlist.preview")
	})
	return
}

func applyWordlistSnapshot(db *gorm.DB, row entity.WordlistVersion, target *rules) error {
	if row.Type == builtinKind {
		target.builtin = row
		entries, e := builtinRules(db, row.ID)
		target.builtinEntries = entries
		return e
	}
	entries, e := enabledEntries(db, row.ID)
	if e != nil {
		return e
	}
	switch row.Type {
	case "crisis":
		target.crisis, target.crisisEntries = row, entries
	case "school":
		target.school, target.schoolEntries = row, entries
	case "ordinary_violation":
		target.ordinary, target.ordinaryEntries = row, entries
	default:
		return command.ErrInvalid
	}
	return nil
}

func validateWordlistMatcher(db *gorm.DB, row entity.WordlistVersion) error {
	var snapshot rules
	if e := applyWordlistSnapshot(db, row, &snapshot); e != nil {
		return e
	}
	if _, e := scanWordlists("", snapshot); e != nil {
		return command.ErrInvalid
	}
	return nil
}
