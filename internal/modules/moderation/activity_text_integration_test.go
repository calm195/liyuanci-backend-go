package moderation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
)

// verifyActivityTextRules 使用真实双词库快照验证新活动桥接，不改变旧领域夹具的初始化边界。
func verifyActivityTextRules(t *testing.T, root *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	tx := root.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	checker := &moderation.ActivityTextChecker{}
	if err := checker.Check(ctx, tx, "合成普通介绍"); !errors.Is(err, command.ErrUnavailable) {
		t.Fatalf("缺失内置词库仍允许活动提交：%v", err)
	}
	version := entity.WordlistVersion{ID: uuid.NewString(), Type: "builtin", Version: 1, Status: "published", UpstreamVersion: "synthetic-dual-v1", UpstreamDigest: strings.Repeat("a", 64)}
	must(tx.Create(&version).Error)
	for _, spec := range []struct {
		word, action string
		disabled     bool
	}{
		{"合成内置拦截词", "review", false},
		{"合成内置提示词", "hint", false},
		{"合成停用内置词", "review", true},
	} {
		must(tx.Create(&entity.WordlistEntry{ID: uuid.NewString(), VersionID: version.ID, Entry: spec.word, Action: spec.action, UpstreamLabel: "synthetic", UpstreamAction: spec.action, Disabled: spec.disabled}).Error)
	}
	must(tx.Model(&entity.WordlistHead{}).Where("kind='builtin'").Update("effective_id", version.ID).Error)
	for _, spec := range []struct {
		body    string
		blocked bool
	}{
		{"合成普通介绍", false}, {"学校命中", false}, {"普通命中", true}, {"危机命中", true},
		{"合成内置拦截词", true}, {"合成内置提示词", false}, {"合成停用内置词", false},
	} {
		err := checker.Check(ctx, tx, spec.body)
		if spec.blocked {
			if !errors.Is(err, command.ErrReviewPending) {
				t.Fatalf("高风险资料未拒绝：%v", err)
			}
		} else {
			must(err)
		}
	}
	for _, spec := range []struct {
		body    string
		pending bool
	}{
		{"合成普通介绍", false}, {"学校命中", true}, {"普通命中", true}, {"危机命中", true}, {"合成内置提示词", true}, {"合成内置拦截词", true}, {"合成停用内置词", false},
	} {
		pending, evidence, err := checker.ScanTrait(ctx, tx, spec.body)
		must(err)
		if pending != spec.pending || len(evidence) == 0 {
			t.Fatalf("特质词库分流错误：%s %v", spec.body, pending)
		}
	}
	// 同一检查器已缓存快照后，内置指针丢失仍须失败关闭。
	must(tx.Model(&entity.WordlistHead{}).Where("kind='builtin'").Update("effective_id", nil).Error)
	if err := checker.Check(ctx, tx, "合成普通介绍"); !errors.Is(err, command.ErrUnavailable) {
		t.Fatalf("缓存绕过丢失的内置词库：%v", err)
	}
}
