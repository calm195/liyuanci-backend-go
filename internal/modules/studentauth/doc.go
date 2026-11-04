// Package studentauth 管理学生微信登录、会话和本人资料。
// 微信HTTPS协议由adapters/gateway实现Exchanger接口，模块只接收显式注入。
// service执行业务授权，repository读取当前主体；身份检索与黑名单策略由共享模块提供。
package studentauth
