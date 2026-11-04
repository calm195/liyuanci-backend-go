// Package app 是API、worker和初始化命令的应用装配边界。
// 它将配置、业务服务与供应商连接起来；业务规则和HTTP协议分别留在领域包与httpapi。
// 构造器借用传入依赖，Open/Run宿主拥有自己创建的资源；失败逆序清理，正常停止先排空工作。
// 正式与开发装配共享服务构造逻辑，开发适配只允许显式development/integration环境。
package app
