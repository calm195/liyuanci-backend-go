// Package whisper 管理树洞帖子、暖评、抱抱、匿名关系和私人胶囊。
// commands/query及各功能文件组织用例，repository访问表模型，capsule_worker执行到期任务。
// 审核提交与限流通过注入接口协作，安全和黑名单策略始终在当前事务中复核。
package whisper
