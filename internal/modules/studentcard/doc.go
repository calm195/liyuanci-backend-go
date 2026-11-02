// Package studentcard 管理学生卡私有对象、扫描任务和授权读取。
// service接收上传，worker消费数据库持久任务，repository读取当前会话及材料版本。
// 对象存储和扫描器由宿主注入，任务完成前复核主体与当前版本。
package studentcard
