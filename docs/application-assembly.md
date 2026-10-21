# 学生应用与学生卡 worker 显式装配

T007/T008/T011，REQ-002、014、015、018、019、020。这里提供可由宿主调用的应用构造入口，不新增默认供应商、开发密钥或自动迁移。当前 cmd/server 和 cmd/worker 的默认启动行为保持原有范围，正式供应商接入及进程配置尚未交付。

## 学生卡 worker

宿主使用独立 domain_worker 池构造 studentcard.Service，并在任何 MQ 客户端启动前调用 transportmq.ConfigureSDKLogging。随后调用 cardworker.Open(ctx, db, scanner, logger, cfg)，其中 scanner 必须同时支持 Consume 和 ProcessNext。此模块仅处理固定 student_card.scan_requested 事件，不复用探针主题，不创建 MQ 主题或数据库结构。

Config 显式提供 MQ 通道/鉴权、Relay 租约/重试预算、RelayInterval、ScanInterval、OperationTimeout。Open 创建生产者、数据库 Relay 及订阅器；后续启动失败逆序关闭已创建生产者。成功后启动两个独立单线程循环，一个调用 Relay.Once，一个调用 ProcessNext。每轮完成后等待配置间隔，空队列、失败、panic 和超时均不会热循环；每轮独立 context 受 OperationTimeout 限制。消息消费并发单独由 MQ.Concurrency 控制，数据库租约仍是最终并发和重复执行屏障。

数据库补偿持续存在，即使 MQ 唤醒缺失或 Deferred 已 ACK，仍能扫描到期工作。转发失败和扫描失败分别记录 operation/result/duration 中文完成日志，不输出供应商错误正文；调度成功日志仅表示一次调用结束，不代表整条队列清空或所有扫描通过。领域状态、预算耗尽、取消、重试和幂等继续由 durable/studentcard 持久逻辑决定。

Open 的 context 仅用于启动，不成为整个生命周期的期限。宿主必须显式调用 Close：先取消调度，停止订阅并等待在途消息处理，再等待两个调度循环退出，最后关闭生产者。Close 并发/重复调用只释放一次自有资源；某个 Close 返回错误仍继续释放其他资源。依赖适配器必须遵守 context，宿主只在 Close 返回后关闭数据库和供应商；本模块不强杀不响应取消的 goroutine。

## 本轮验证范围

按用户要求不启动跨端联调或真实中间件测试。单元测试覆盖调度 panic/故障恢复、超时、取消、关闭等待与并发关闭。测试使用调度回调及关闭回调，不执行真实数据库或 MQ 操作。

实际检查结果见[第八阶段记录](milestones/phase-08.md)。此交付不关闭 T007/T008/T011 整体任务、真实 MQ+PG 领域闭环、真实供应商或部署验收。
