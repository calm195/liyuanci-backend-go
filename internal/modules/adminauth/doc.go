// Package adminauth 管理后台账号、口令、会话与岗位授权。
// service/accounts负责登录及账号操作，repository复核当前会话和角色；调用方注入限流器，Redis实现位于adapters/redis/authrate。
package adminauth
