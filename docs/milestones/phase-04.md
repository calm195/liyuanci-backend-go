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

## 数据库补偿与安全重驱动组件

2026-10-21；T007/REQ-006、007、015、017、018、019、020。用户明确要求先完成数据库补偿与安全重驱动；前置唤醒替代防护已提交 `3551f24`，本节组件随后独立提交。T005/T006 整体状态与缺失外部交付不因此改写。

### 数据库补偿与资源所有权

`Consumer.Compensate(ctx, limit)` 在显式总时限内扫描到期 pending/retry_wait 和租约已过期 leased；排除有效租约、未来 next_at、attention、completed、cancelled。每次 1–100 项，按 next_at/工作 UUID 排序，使用当前未替代事件的数据库关联，不受 outbox.sent_at 是否确认影响。已确认却丢失唤醒、临时失败到期以及过期租约均能恢复推进。扫描候选不代表授权或领取，实际执行逐项调用已有 Consume，仍复核当前 guard/版本、串行化工作并原子提交业务/终态/回执；并行补偿不重复产生效果。没有新建工作或外部发送，晚到 Broker 唤醒按原 operation_id 去重。

当前目标版本保存在事件关联；缺失或关联不符时补偿不能猜测版本。仅将仍到期且未终结、确无当前有效关联的工作改为 attention，不重建事件或业务内容；并发取消/完成、有效租约及后来补齐关联均受条件更新保护。批次按独立事务推进，后项失败不撤销已提交的前项，返回汇总与首个稳定错误，日志保留实际已提交计数。单批时限可提前结束批次，吞吐、公平性、重试间隔与连接池容量仍须后续压测，不以有界扫描宣称性能验收。

`RunCompensator` 提供显式串行时钟入口：装配方必须传入有效间隔和批次上限，无默认自动启动。单批失败等下一周期，context 取消后停止接收新批并等待当前调用退出，释放 ticker，不关闭注入的连接池。当前 API/探针 worker 没有偷偷启动该循环；业务 worker 的装配、健康状态及资源隔离待后续接线。定时入口取消测试使用首批提交完成日志触发取消，验证无额外批次和残留工作，不用 sleep 猜测提交。

### 安全重驱动与授权/版本规则

`RedriveCommand(policy, input)` 构造固定后台 command.Definition；调用方把同一 RedriveRequest 作为 command.Input.Request，提供已验证的 Bearer/Idempotency-Key，使摘要覆盖工作 UUID 与 ExpectedVersion。角色白名单与 Recheck 来自可信服务装配，不接受客户端策略；构造器拒绝空复核/角色、未知角色或缺失正版本。Engine 取得当前后台会话/角色、规范业务锁和当前对象访问；Authorize 在首次和回执重放时均执行 Recheck，角色撤销或当前访问拒绝不能靠回执绕过。Authorize 不把 attention 当作重放条件，避免原命令自身已经改变状态后无法查询技术结果。

底层 Redrive 仅可在已认证后台命令事务中使用，拒绝 Actor 为空的后台工作事务或无允许角色。RedrivePlan 供 Discover 发现完整依赖，Redrive 检查所需 guard/资源确已持锁，随后锁工作、重读源与集合；新参与者或变动版本不能追加逆序锁。ExpectedVersion 是工作自身版本；TargetVersion 是持久源中的目标内容版本，不能替代。只处理 attention，取消/完成/正常排队均冲突；已存在同消费者 operation_id 回执也冲突，不通过清回执来制造重放。Recheck 必须复核当前资格/代次、目标版本、领域状态及隐私/删除条件，不得发送外部请求或产生业务效果；权限不足返回稳定拒绝，工作失效返回 Invalidate。

通过复核返回 Resume 时，重驱动只恢复同一工作和 operation_id，重置该工作新处理预算/next_at，并在同事务标记旧事件 superseded、创建新 UUID 的最小事件；旧事件 attempts 不重置、sent_at 不伪造。转发器的领取与确认、消费者读取均拒绝已替代事件；旧转发者迟到也不能确认新周期或覆盖其状态。缺目标版本时禁止 Resume。Invalidate 则取消工作、替代旧事件并记录无操作消费回执，不创建新事件。后续命令失败会一起回滚工作/事件替代/新事件/回执；command_receipts 提供同键幂等，新事件 UUID 只在首次 Apply 内生成，不因 HTTP 重试重新生成摘要。

重驱动暂存日志仅 pending_commit/failure，最终成功由命令根事务提交日志负责；补偿与调度记录 duration/result、实际计数及失败数，不记录令牌、身份、载荷或驱动原文。技术日志与命令回执不替代后续业务审计、管理待办页面或 D05 删除意图。公开 HTTP 管理入口尚未接线，不能直接把底层 Redrive 当成免认证运维方法。

### 真实验证、模拟与缺口

`make fmt`、`make check`、`make build`、真实隔离 `python3 scripts/check-migrations.py --container`、`git diff --check` 通过。锁定 Go 1.25.0 builder 库测试、正式非 root UID10001 CLI、六版本重复迁移/权限、空库上下行、恢复库混建拒绝与日志脱敏通过；本地镜像制品 `liyuanci-migrate-check:e42928b3a6b2` 未发布。补充无中间件契约用例后另行通过 make check；常规测试仍明确跳过无 DSN 的数据库用例，不把低覆盖单元运行当成真实数据库验证。

新增真实场景包括已确认但未消费的工作补偿、到期/租约/终态/limit、缺版本转待办且不造数据、并发补偿仅一次效果、重驱动同键重放保持操作、旧事件失效、新事件补偿完成、工作旧版本/终态冲突、当前目标失效取消、已有消费回执拒绝、角色与对象权限拒绝（包括重放再授权）、后续错误原子回滚、旧 Sender 回来不能覆盖新周期、实际后台恢复/失效终结与领域 worker 补偿权限、回执额外字段/UPDATE 拒绝、非空回退保护、定时补偿退出和日志脱敏。首次检查中修正了后台去重标识缺少最小读取权限；首次缺失版本用例的合成复核已判失效而安全取消，后改为显式 Resume 反例，确认无版本时确实拒绝恢复。最终复验通过，不隐瞒这两项初次检查失败。

身份、领域复核和 Sender 均使用合成夹具，数据库/会话/角色/权限/事务/租约是真实 PostgreSQL；不是实际 D02 登录/密钥或 RocketMQ 断网/进程崩溃验收。D04 业务主题、生产消费身份/ACL、保留/死信及真实 Sender/订阅/ACK/NACK 仍未接线；本地隔离 Broker 可以继续推进，不把生产交付缺失当成本地验证阻塞。D02 真实密钥/后台身份、D03 正式运营资料、D05 独立删除意图/全副本与恢复仍按交付清单未完成；删除/恢复类工作必须有领域 Recheck 确认独立意图，通用补偿库不能替代或绕过 D05。

本批数据库补偿与安全重驱动可独立实施范围已补齐，T007 整体仍为部分实现：真实 Broker 往返、实际领域消费者/取消写入、管理待办与业务审计、HTTP/独立业务 worker 装配、限并发/健康/压测和生产验收待推进。未推送、部署、改动共享审批材料或读取真实秘密；阶段记录与 README 已同步。

最终追加六并发同键重驱动反例后，`python3 scripts/check-migrations.py` 完整真实数据库复验通过：一个原工作、一个 operation_id、仅一条新唤醒，重复命令由根回执复用；迁移 79.7%、identity 81.2%、command 83.5%、durable 78.0%。`make fmt/check/build`、`git diff --check` 均通过。该追加测试未改变运行代码，镜像工具链/CLI 结果沿用上段对应运行代码的本轮制品，不将补充宿主测试描述为新一次镜像构建。


## 真实 MQ 运输适配独立收口

2026-10-21；T007/REQ-006、007、015、017、018、020，契约0.1.0。后端HEAD仍为 `550be1e4f340008113c44e9b3c1273d49e7ac84f`，本次工作区未提交。docs起点 `a5e6124dd77c0f539516e550fb790d7ad2bd0195`，结束只读复核无相对更新。用户明确确认 nameservers.go 与 transportmq/ 是可接续前序改动；起始已存在，不能冒称本轮从零实现。首次检查发现测试函数未实现Handler接口，修正显式handleFunc转换后复验通过。

`internal/transportmq` 显式固定主题、组、事件类型、实例、超时和有限重试；拒绝探针主题、默认主题、缺失/半套凭据，隔离测试必须显式允许无鉴权。复用启动期NameServer DNS解析，SDK诊断自由文本/字段丢弃，仅保留受控中文日志；装配方须在任何客户端启动前安装进程级SDK日志器，不在运行中替换。这里只提供库，不修改API/worker默认装配。

Sender只发送原六个技术字段，以event_id为消息key；只有SendOK且context未超时才返回成功，不确定结果作为临时失败交持久重试，不能提前确认发件箱。Subscriber使用集群和单消息批次，严格拒绝未知/重复JSON字段、尾随JSON、超长或错误主题/事件；处理器失败、panic、取消、无明确结果均NACK。Applied/Replayed/Cancelled/Missing/Deferred可ACK，Deferred依赖数据库补偿，不表示业务完成；消息不是业务权威，也不建立缺失工作。

生产者拥有客户端并等待同步发送后关闭；订阅先禁止新回调、取消在途context、等待回调退出，再关闭SDK；外层最后关闭DB。处理器必须遵守context并自行保证数据库原子事务，不能用网络外发充当持久回调。开启/发送/确认决策/关闭均记录中文结构化完成日志，含result/duration、受控reason及重投次数，不记录载荷/凭据或SDK原始错误。新增固定主题防误投保护和各ACK分支/错误主题/生命周期日志测试。

`python3 scripts/check-mq.py` 在随机隔离Docker项目显式建主题，运行真实发送、订阅、NACK重投、重复与合成取消晚到、取消发送及退出；保留卷，不能操作其他项目。CI新增独立mq-transport job，远端尚未执行。README同步库目录、装配约束和检查命令，没有新增对外业务入口或普通启动隐式行为。

验证：make fmt/check/build通过；真实MQ两轮通过（40.02s/40.03s），真实PostgreSQL check-migrations通过，原有 sh scripts/integration.sh退出0，git diff --check与Python语法检查通过。首次Go/Python默认缓存写入受限，分别授权重跑/改临时缓存目录解决，没有把限制当成功。源文件SHA256、隔离项目、命令范围及原验收映射见[本轮验证快照](t007-mq-validation-2026-10-21.md)。最终文档修改后再次执行规定质量检查，结果以本轮会话最终输出为准。

MQ真实而处理器为合成内存夹具；PG真实而领域/身份/Sender仍是合成。两者未组成实际领域闭环，不宣称提交后ACK前杀进程、Broker断网/死信、生产ACL或业务取消已经通过。实际领域消费者、注销/处罚取消命令与隐私矩阵、管理待办/业务审计、业务worker循环/健康/资源隔离、性能及生产验收仍缺。T007整体“开发中”附B13，仅本端MQ运输子范围“本端验证通过”，不签整体验收。

B02–B07依据、影响操作、建议、请求响应及兼容性逐项见[本仓库协调提案](../contract-coordination-2026-10-21.md)。身份方案、标签、词库完整/增量及归档取舍未确认，不自行选择；共享契约仍0.1.0，无已生效变更。T005/T006整体依赖仍未完成，相关阻塞仅停止受影响实施/真实验收，不阻塞独立MQ与PG验证。共享台账由docs协调会话汇总，本轮不并发修改，不自动提交/推送/部署。


## 协调增量：待用户决定事项与已批准技术边界

2026-10-21；T002及T008–T017/REQ-002–008、015、018、019，不改变T007运行代码。核对G3原文指纹后，将B02–B07需要用户决定的事项拆为后台身份来源、词库完整/增量、历史版本读取、历史重扫、身份标签词表、后台会话体验六项；建议均未采纳，完整选项/兼容性见[协调提案](../contract-coordination-2026-10-21.md)。标签“可选”和二进制卡代理已获批准，无需用户重复选择；B03版本来源、B04当前授权/审计、B05目标映射属于技术收敛，不转嫁为用户字段选型。

已在共享contracts授权范围增加技术澄清并增强validate.py静态约束；build.py与validate.py通过，80操作、125 schema、757示例及5负边界与新增断言；生成的wire schema/示例未变化，契约仍0.1.0。仅静态补全子范围“本端验证通过”，B02–B07整体仍附阻塞，无业务HTTP/真实联调或审核证据新增。后台身份与业务取舍等用户确认；共享进度由docs协调会话汇总，未修改plans、审批、基线或设计。文档及校验器变更后运行make fmt/check与两仓git diff --check，结果见本轮最终回报；不提交、推送或部署。


## 用户结论落地：契约0.2.0

2026-10-21；T002/T008–T017，REQ-002–008、015、018、019。用户已确认超管创建本地后台账号、默认初始密码策略及登录后可改；B06三项A；B07在读年级、男/女/保密、后台绝对到期重新登录。采用新契约0.2.0，保留原0.1.0 JSON不改写；不修改原审批、需求或设计，用户原文及变更说明在共享contracts/decisions.v0.2.0.md。

本轮只改契约/校验器与阶段文档：冻结后台本地登录、超管创建、本人改密（核旧口令、原子更新与撤销旧会话、204后重新登录）；初始值仓库外注入，无代码默认凭据、账号查询/日志不回口令，不新增强制改密业务门槛。词库使用完整不可变快照和当前kind并发基准，创建/发布分别复核、旧生效版原子归档、授权只读，不自动全量重扫历史。性别wire值male/female/undisclosed；标签可选、隐藏设置继续生效，在读年级具体字典仍待资料方。

独立技术补全新增本人资产扫描GET、首次/重提认证版本规则、词库详情与heads基准/发布双版本，明确schema条件依赖与服务端锁后复核，不能用账号版本代审核记录版本。兼容性涉及开放后台输入收紧、Account新增login_name、标签枚举、词库基准/返回字段及认证条件版本，两端需显式升级。未将schema对象当DDL或真实服务交付。

共享生成/校验通过83操作、130 schema、783示例、原5负边界与8新增负例，原0.1.0文件无差异。所有业务操作仍not-implemented；本轮无运行代码/依赖变更，不重跑make build或MQ/PG检查，执行make fmt/check与两仓git diff --check。状态为契约子范围本端验证通过，业务开发中附原阻塞；B04/B05真实授权审计/目标接线、D02/D03资源、年级字典、TTL及实际HTTP验收仍缺。T007前轮MQ局部通过边界不变；不自动关闭B02–B07，不修改共享plans或两端仓库，不提交、推送或部署。

本轮对应快照：后端分支feat/backend-foundation，HEAD `550be1e4f340008113c44e9b3c1273d49e7ac84f`；docs/main HEAD `a5e6124dd77c0f539516e550fb790d7ad2bd0195`，本轮契约均未提交。原0.1.0两个JSON的git diff为空。本轮共享契约SHA256：

```text
0008f847b84c1cebe78cf29ce39cda3166c93655c3c3e8389e1872bd181b4010  contracts/build.py
5c6e3a7c5a3836887c329570b8f9e2c9bd1ca9919fcd7bc904f4097614de0e40  contracts/validate.py
acf31b515538e1bf3b3f85fcecee9a210bb83e6b0408e2dbcde6643a736d9dd9  contracts/openapi.v0.2.0.json
0d3560fcb0e634d4e0e65a48e448bcb9e605fe3253f65aa976d31bd30bf2df1e  contracts/examples.v0.2.0.json
75de7fa5d156859bce884561d6b32a60a0a7ddb5cc4d76007aa17f02b82793cf  contracts/decisions.v0.2.0.md
```

本轮验证命令：独立虚拟环境Python执行contracts/build.py、contracts/validate.py均退出0；make fmt/check（含vet、race-cover）及两仓git diff --check均退出0。校验环境保留既有LibreSSL/RefResolver警告，未发生校验失败。不将通过记录外推为登录、改密、词库或扫描服务验收。

## 2026-10-21 其他模块 ORM 统一增量

T007、REQ-018/020：工作行锁、消费回执、终态/重试/租约清理、旧事件替代和新事件写入改用实体；保留单语句 CTE、SKIP LOCKED、UPDATE RETURNING、LATERAL 和原子补偿条件。显式限制回执回读列，不扩大后台权限。完整范围、设计取舍、兼容性修复、真实/合成状态与本轮实际验证见[ORM 专题后续增量](orm-refactor-2026-10-21.md#后续模块-orm-统一增量)。本次不推进生产或完整领域业务验收。
