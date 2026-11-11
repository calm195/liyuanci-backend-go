package moderation

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ActivityTextChecker 复用当前双词库与匹配规则，缓存仅绑定不可变词库版本。
// 活动资料仍须人工审核；高风险文本在落入活动资料前拒绝，不伪造普通审核任务。
type ActivityTextChecker struct{ cache wordlistCache }

// Check 在调用方已授权事务中读取有效规则，缺失依赖时失败关闭。
func (c *ActivityTextChecker) Check(ctx context.Context, db *gorm.DB, body string) error {
	snapshot, err := readCachedRules(db.WithContext(ctx), &c.cache)
	if err != nil || snapshot.builtin.ID == "" {
		// 旧审核读取器兼容未初始化内置词库的独立夹具；新活动不继承该降级路径。
		return command.ErrUnavailable
	}
	matcher, err := c.cache.compile(snapshot)
	if err != nil {
		return command.ErrUnavailable
	}
	evaluation := matcher.scan(body)
	if evaluation.routesTo("crisis") || evaluation.routesTo("ordinary_violation") {
		return command.ErrReviewPending
	}
	return nil
}

// ScanTrait 为特质独立审核保留真实词库命中证据；任何命中均交由人工处理。
func (c *ActivityTextChecker) ScanTrait(ctx context.Context, db *gorm.DB, body string) (bool, json.RawMessage, error) {
	snapshot, err := readCachedRules(db.WithContext(ctx), &c.cache)
	if err != nil || snapshot.builtin.ID == "" {
		return false, nil, command.ErrUnavailable
	}
	matcher, err := c.cache.compile(snapshot)
	if err != nil {
		return false, nil, command.ErrUnavailable
	}
	evaluation := matcher.scan(body)
	evidence, err := json.Marshal(struct {
		WordlistEvaluation
		Versions [4]string `json:"wordlist_versions"`
	}{evaluation, snapshot.versionIDs()})
	return len(evaluation.Matches) > 0 || len(evaluation.Routes) > 0, evidence, err
}
