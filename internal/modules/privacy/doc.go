// Package privacy 管理账号及证据删除、脱敏和恢复前重删规则。
// service/activation建立屏障，cleanup执行持久清理，redaction处理幸存内容。
// journal保存独立删除意图；恢复失败保持读取与外发门禁，文件账本的锁和刷盘属于持久协议。
package privacy
