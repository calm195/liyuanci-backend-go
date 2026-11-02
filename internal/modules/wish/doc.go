// Package wish 管理心愿、应愿申请、匹配生命周期及还愿故事。
// query与commands相关功能文件组织用例，repository文件持有查询；审核由持久任务驱动。
// 跨模块协作函数使用调用方事务，保持账号及资源锁顺序，不自行提交或外发。
package wish
