# 第四阶段：持久工作与消息链路

2026-10-21；T007，REQ-006、007、015、017、018、020。用户授权继续按计划实施，按功能与依赖顺序提交；不推送、不部署，不更改共享审批/需求/设计。

## 依赖核验与本批范围

T005 已提供 work_items/outbox_events/consumer_receipts 及显式权限，T006 的事务/幂等/排序锁/当前授权库已提交并验证，可先推进持久写入和转发基础。T005 整体真实 D02 密钥适配、T004 两端验收仍未完成；T006 未装配领域命令及网关 Redis 故障限流，不将前置状态改写为整体验收通过。

T004 的真实 RocketMQ 探针历史证据不等于业务链路。当前没有生产 D04 Broker、ACL、专用主题/生产消费身份、保留/死信/重驱动及执行网络的交付验证；本批 Sender 为合成适配器，尚未接真实 Broker。本地 Docker 中间件可以支持后续真实隔离联调，无需以生产未交付为由停止测试资源可做的工作。D02、D03、D05 的其他缺口沿用[交付清单](../dependency-deliverables.md)，不阻塞本批纯数据库验证。

## 已实现能力、关键流程与职责

- `internal/durable.Append` 接收服务端固定 Route、纯技术 Draft、调用方 GORM 事务和 zap。拒绝根连接、非法类型/UUID/非正聚合版本。通过单条 CTE 插入 work_items 与 outbox_events；业务结果、工作、事件由调用方统一提交或回滚，操作标识唯一约束不被静默覆盖。调用方仍须遵守 T006 的账号/资源→工作→发件箱顺序，回调仅调用 Append，不发 MQ。
- Draft 不接受自由 JSON，只有工作/稳定操作/事件/聚合 UUID 和聚合版本。Route 的工作、目标和事件类型只由可信服务装配，不能把任意客户端类型或正文当路由。目标版本位于 outbox.aggregate_version；work.version 是自身并发版本，不能混用。没有默认真实工作类型或上线主题。
- `NewRelay` 显式注入根 DB、zap、Sender、Route 和技术配置，不持有连接池/Sender 生命周期；不接收外部事务。`Once` 每次最多领取/发送一条，由未来进程装配负责循环、并发和关闭，当前不启动新后台循环。
- 领取短事务按工作→事件锁序，SKIP LOCKED 跳过其他领取者持有的工作，按 next_at/event_id 排序，过滤已确认、未来重试、未到期租约及取消/完成/attention 工作。提交租约后才调用 Sender，不把数据库锁跨越外部发送。发送 context 有显式上限，适配器必须遵守取消。
- Event 只含批准的 event_id、event_type、aggregate_id、aggregate_version、correlation_id、created_at。correlation_id 映射 work UUID 便于回查持久源；不发 operation_id、主体身份、正文、图片或 URL。消费者仍必须按当前持久源查 stable operation_id，不从事件创建工作。
- 确认事务再次先锁工作，再锁事件；用事件 attempts 代次及未过期 lease 检查当前所有权，旧持有者不能确认新租约或覆盖后来成功。发送成功但确认前中断可按原事件重发，event_id 与技术时间稳定，不承诺外部恰好一次。
- 临时失败记录 next_at，保持可重试；达到显式 MaxAttempts、不可恢复/未知发送错误或适配器 panic，将非终态工作标记 attention。确认前中断也消耗发送预算，重领发现已耗尽时仅持久化 attention，不再发送。已 cancelled/completed 的工作不被 attention 更新复活；发送中的取消不能撤回已进行的 MQ 调用，晚到唤醒仍须由未来消费者复核终态。
- 发送上下文超时/取消作为临时故障处理；父上下文中断导致确认无法写入时保留租约，过期后再取。数据库/供应商原始错误不越过稳定错误边界。Append 只记录 pending_commit 暂存日志，最终提交成功由外层命令日志负责；Relay 仅在确认提交后记 success，空队列 idle，预算耗尽 attention，失败 failure。日志含 duration/result/claimed，无原始错误或载荷，技术日志不替代业务审计与删除日志。
- 复用五版本业务结构及领域 worker 权限，不新增/改写编号迁移或表字段，中文数据库注释保持。真实 SET ROLE 验证领取/确认权限，无需授予探针 worker 或额外证据权限。API/worker 默认行为、readyz 和启停入口保持原状。

## 验证与真实/模拟状态

新增 `APP_DURABLE_TEST_DSN` 独立测试库，由 `scripts/check-migrations.py` 在随机专用 PostgreSQL 容器中创建；原 CI migrations job 串行运行新包，锁定 Go 1.25 builder 也纳入离线库测试。无 DSN 时明确跳过集成测试，不将单元检查当成真实中间件验收。

真实数据库覆盖业务版本/工作/事件原子提交与回滚、事件冲突不留孤立工作、八并发租约、外部发送时独立连接 NOWAIT 取得工作锁、发送确认、临时重试/门槛/配置失败/panic 待办、过期租约重领稳定事件、旧持有者不得覆盖新确认、终态跳过/发送中取消不复活、确认前中断预算、领域工作组实际权限、技术载荷字段和日志脱敏。故障通过合成 Sender 与显式过期租约注入，不宣称已做真实杀进程/Broker 丢包、断网、死信或外部供应商验证。最终命令结果在本阶段完成前记录。

## 未完成部分与后续顺序

T007 仍为部分实现。下一步按依赖推进：消费方从持久源读取 operation_id，持锁当前业务资格/版本，结果与 consumer_receipts/工作终态同事务；取消/晚到无操作终结；数据库定时补偿和重驱动的当前授权；真实 RocketMQ Sender/订阅及确认前中断/重复晚到往返；再接独立 worker 的业务装配、资源隔离与健康状态。异常待办当前只持久状态，没有管理 API/UI、告警或安全重驱动命令；不能手工改 pending 来冒称重驱动验收。

本批不实现真实通知/图片安全/审核、匹配/聊天、删除恢复或领域业务权限。仅见到 work.status 终态而跳过发送不是完整的注销/处罚/隐私矩阵；尚未有可上线消费者，不能开放对外业务。生产 D04 身份/主题/保留窗口、D02 与 D05 的全副本/恢复仍须交付并实测，不把合成 Sender 当真实 MQ 闭环。

## 本批最终验证结果

`make fmt`、`make check`、`make build`、`python3 scripts/check-migrations.py --container`、`git diff --check` 通过。隔离真实数据库包覆盖率：迁移 79.7%、身份 81.2%、命令 83.5%、durable 84.9%。锁定 Go 1.25.0 builder 离线库测试通过，正式 CLI 以 UID10001 验证重复 up/permissions、down/up、恢复库隔离和安全日志；本地制品 `liyuanci-migrate-check:634f1d7bf41a` 未发布。首次普通脚本验证通过后，补入确认前中断预算检查并重跑上述完整镜像验证；后者为本批最终实测。

未改真实 Broker/监控/启停行为，未重跑这些无关验收；远端 CI、真实发送/消费、生产部署、性能、删除恢复均未执行。SQL 沿用完整中文表字段注释并由结构测试复核，没有新增表字段或改写已应用版本。用户授权按计划提交，本批按依赖接在命令框架之后，不推送或部署。
