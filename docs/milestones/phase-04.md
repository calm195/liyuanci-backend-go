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

## 消费侧增量：当前持久源、去重与取消终结

2026-10-21；T007/REQ-006、007、015、017、018、020。依赖先提交 `d3a6ff0` 的后台 LockPlan 与最小账号 guard 函数，再提交本节消费实现；不改变业务范围或依赖交付状态。

### 实现范围与关键流程

`NewConsumer` 显式注入根 DB、zap、固定 ConsumerDefinition 与技术配置，拒绝已有事务。事件只能提供最小技术引用；Consumer 用全部技术事件字段匹配真实 outbox/work 关联，锁后再次重读关联，不接受伪造目标/版本/类型/时间，不从未知事件重建工作。事件/工作已清理或不匹配返回 Missing，不插入回执。

初读使用真正只读事务发现完整业务参与者，LockPlan 按既有顺序锁 guard/资源，再锁工作源。后台 Tx.Actor 为空，不能借用学生 Bearer；领域 Discover 必须列全当前存在的依赖，Apply 必须复核当前生命周期、权限代次、资格/处罚、目标版本与隐私屏障，具体规则仍由后续领域实现。Work.TargetVersion 来自数据库 outbox.aggregate_version，Work.Version 是工作自身版本，绝不互相替代；锁后用持久源重新发现集合，变化整事务回滚后进入持久重试，不追加逆序锁。

同一工作行串行化消费者，按服务端 consumer_kind 与数据库 operation_id 查询去重。Apply 的纯数据库业务结果、工作 completed/cancelled 终态及 consumer_receipts 在同一根事务提交；运输层只可在 Consume 返回后确认，确认前中断再投递不会重复产生业务结果。Apply 不得手动提交/另起写事务/进行网络外发，涉及外部操作的工作必须拆分领取、调用、最终复核，不能把本入口当成持长事务的外部执行器。

已 completed/cancelled 的工作无需重新发现可能已被注销清理的依赖，不执行 Apply，只幂等记录技术回执；attention、未到 next_at、未过期 leased 返回 Deferred，不插入成功回执。当前领域发现结果需要安全处理目标不存在：只锁仍存在的依赖，Apply 返回 Obsolete 后无操作取消；不能把缺失内容从旧事件恢复。

临时失败先回滚业务，再以工作版本 CAS 持久化 retry_wait/next_at/attempts；达到显式预算或配置失败/panic 转 attention，均不插成功回执。失败调度事务只修改工作元数据，拒绝覆盖其他消费者新版本、completed/cancelled/attention；父 context 已取消或数据库不可用时失败关闭，保留数据库当前状态待后续重投/补偿。Apply panic 先由 GORM 回滚，再由边界恢复与调度，日志只留 recovered 标记，不留原文。初读 Discover panic 也恢复为稳定失败，但不在尚未建立可信工作上下文时写待办。

消费完成日志只在根事务结果确定后记录 result、consumer、duration、replayed/cancelled/missing/deferred/recovered，不记录身份、事件正文或数据库/panic 原文。工作库错误统一屏蔽，ErrTemporary/ErrConfiguration 为回调显式错误契约；未分类存储错误保持失败，不擅自签成业务取消。复用现有表及完整中文表字段注释，无编号迁移/自动迁移/默认真实工作类型；未修改启停、监控或 Broker。

### 实测与限制

真实 PostgreSQL 新用例覆盖八并发同工作只提交一次、伪造/未知事件不建工作或回执、终态取消跳过领域回调、资格撤销与目标版本过时无操作取消、工作与目标版本独立、失败业务回滚/持久重试预算/attention 不自动重放、panic 后先回滚再待办、初读后取消不被重试复活、未来 next_at 不插回执、实际领域 worker 权限和提交后模拟运输确认丢失的 JSON 重投。合成领域只改测试账号或只完成技术工作，不冒称真实审核/消息服务；取消测试使用已有持久终态，没有实现注销/处罚领域命令或完整隐私矩阵。

首次真实检查发现领域 worker 的账号 SELECT 不足以 FOR UPDATE，已通过前置最小 guard 函数修复，并复验通过；没有给它账号 UPDATE。`make fmt`、`make check`、`make build` 和真实隔离 `python3 scripts/check-migrations.py --container` 通过；锁定 Go 1.25.0 库测试、正式非 root CLI/重复权限/上下行/恢复隔离及日志也通过。首次完整镜像制品 `liyuanci-migrate-check:8a733559866c` 为本地验证；其后新增锁后事件重读与独立目标版本反例，最终结果另附，不把先前制品当作新增反例已验证。

### 最新未完成项

T007 仍部分实现：数据库定时补偿/重新唤醒、业务取消写入命令、安全重驱动/待办入口、真实 RocketMQ Sender 与订阅/ACK/NACK、重复/晚到/确认前故障的真实往返、独立业务 worker 装配及资源隔离尚未完成。Deferred 的唤醒不能被装配方当成工作最终完成，需要未来数据库补偿；当前没有对外可用的 Broker 消费进程。D04 真实身份/主题/留存、D02 真实密钥与身份、D05 删除/恢复缺口继续保留；本批不推送、不部署，不签整体或生产验收。

消费增量最终复验：锁后持久事件重读及 TargetVersion/Version 独立反例加入后，`make fmt`、`make check`、`make build`、`python3 scripts/check-migrations.py --container`、`git diff --check` 全部通过。真实包覆盖率为迁移 79.7%、身份 81.2%、command 83.5%、durable 80.3%；新增分支后覆盖率变化如实保留，不以比例代替场景断言。锁定 Go 1.25 builder 与正式 CLI 完整复验通过，本地制品 `liyuanci-migrate-check:2891abf6304d`。未连接真实业务 Broker、未发布；原迁移 CI job 自动包含新包内测试，远端未执行。

## 补偿与重驱动前置：唤醒替代迁移

2026-10-21；T007/REQ-006、007、015、017、018、020。版本 6（0006_outbox_supersession）增加 outbox_events.superseded_at 与当前工作关联索引；字段与索引具有中文数据库注释，既有表注释完整、版本 1–5 原文件不改写。默认 NULL 保留旧事件可用；非 NULL 禁止转发领取/确认及消费。down 先排他锁，发件箱非空时拒绝，不能因移除标记让旧唤醒重新生效。迁移目录/版本账本断言同步为六版本，业务表数量保持四十。

重驱动将使用新事件标识而非重置旧租约 attempts，防止旧发送者与新周期拥有相同 fencing 代次。旧事件替代不伪造 sent_at。后台最小列授权仅允许 consumer_kind/operation_id SELECT/INSERT，用于检查去重及无操作终结；不能读取 completed_at、修改或删除既有回执，重放权限前显式清除两组历史列 ACL。运行组无自动提权，普通启动仍不迁移或配置权限。

`make fmt`、`make check`、`make build`、隔离真实 `python3 scripts/check-migrations.py --container` 已通过，含全部表字段中文注释、旧版追加/空表回退重建、非空发件箱拒绝回退且保留替代标记、实际后台窄权限、旧转发者失效和正式 Go 1.25/UID10001 CLI。补偿/重驱动运行组件另行提交，完整实现与最终验证见后续增量记录；真实供应商、Broker 与生产状态不变，未推送或部署。
