package privacy

import "context"

// Intent 只包含恢复删除必需的随机引用，不复制身份、正文或管理员理由。
type Intent struct {
	// SourceModule 随持久意图恢复投影命名空间，历史空值按旧入口处理。
	SourceModule string `json:"source_module,omitempty"`
	ID           string `json:"id"`
	Scope        string `json:"scope"`
	Target       string `json:"target"`
}

// Journal 必须使用独立于业务恢复的持久资源；Append成功即授权恢复程序继续删除。
// Pending不得漏过未确认意图，Confirm只在数据库清理事务提交后调用。
type Journal interface {
	Append(context.Context, Intent) error
	Pending(context.Context, string, int) ([]Intent, error)
	Confirm(context.Context, string) error
}
