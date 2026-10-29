package moderation

// wordlistDetectorVersion 标识只由已发布词库快照筛选产生的候选，不冒充外部供应商结论。
const wordlistDetectorVersion = "wordlist-first-v1"

// requiresReview 将三个分类统一前置；重审标记优先于自动通过，私密内容命中涉校词也须继续复核。
// 只读取快照，不变更原文、身份标签、词库或持久任务；最终效果仍由消费者锁内复核决定。
func (r rules) requiresReview(body string, recheck bool) bool {
	return recheck || hit(body, r.crisisEntries) || hit(body, r.schoolEntries) || hit(body, r.ordinaryEntries)
}
