# 业务结构、迁移与权限边界

2026-10-20；T005，REQ-002–008、014–020。依据共享 database/security/api 设计和契约 0.1.0，批准材料保持只读。本文件描述已落地的数据库结构，不将表存在或约束通过当作对应领域服务完成。任务验收见 [第二阶段](milestones/phase-02.md)。

## 版本与覆盖

保留版本 1–5 原文件及 SHA-256；版本 3/4/5 补齐领域结构，版本 6 增加发件箱替代标记及当前关联索引。总计 40 张业务表（35 张 public、5 张 evidence），另有业务迁移账本。新增的 36 张表、全部字段及原有表都带中文数据库注释；真实检查查询 PostgreSQL 目录，不以 SQL 中有 `--` 注释代替。

| 版本 | 表 | 已落实的结构职责 |
| --- | --- | --- |
| 1 | accounts | 普通账号、生命周期和权限/聚合版本 |
| 2 | identity_lookup_keys、identity_secrets、identity_lookup_claims | 学号/OpenID 信封及双代唯一占用，已存在的 Store 协议保持 |
| 3 | identity_names | 姓名信封及内容版本，独立规范化行结构，不建立姓名检索索引 |
| 3 | admin_accounts、admin_role_bindings、sessions | 后台独立主体；外部凭据引用与本地摘要二选一；会话两类主体外键二选一，生成命名空间/主体标识，令牌只存摘要 |
| 3 | assets、identity_reviews | 普通学生卡私有对象元数据、每账号一个当前待审版本、驳回原因非空 |
| 3 | blocks、penalties、appeals | 有向拉黑唯一、不可自拉黑；等级/动作/期限对应、警告确认时间、违规动作幂等、成功申诉理由 |
| 4 | wishes、wish_versions | 分类/校区/报答契约枚举、1–30 整数天、聚合/内容版本分离、当前内容延迟外键、公开投影与涉校状态 |
| 4 | applications、matches、conversations、messages | 活跃申请部分唯一、同心愿一个 current 匹配、匹配作者外键、会话唯一、消息幂等与送达序号约束 |
| 4 | story_versions、content_registry | 故事作者必须为许愿人、精确内容版本注册、涉校检测状态，原始正文为信封 |
| 5 | wordlist_versions、wordlist_entries、moderation_tasks | 规则版本、精确内容审核外键、主体/屏障代次、驳回原因和修改建议分别非空 |
| 5 | reports、content_restrictions、privacy_holds | 普通举报、各限制原因独立、幸存内容有效屏障部分唯一，无注销者身份/匹配外键 |
| 5 | work_items、outbox_events、consumer_receipts | 稳定操作唯一、工作租约/重试、最小唤醒事件、消费回执，不设身份正文 payload |
| 5 | notifications、delivery_attempts、command_receipts | 通用通知与渠道状态、命令摘要幂等及结果引用，不保存正文快照 |
| 5 | access_audits、deletion_jobs | 受控普通访问留迹、删除阶段/剩余计数/安全错误码，completed 必须清除 subject_ref |
| 5 | evidence.evidence_cases、crisis_cases、assets、evidence_items、evidence_actions | 限定案件、危机流程、私有证据对象、主体/正文信封与必要动作；不以普通账号外键依赖其存续 |
| 6 | outbox_events 扩展 | superseded_at 中文字段注释及当前工作关联索引；旧唤醒不可领取/确认/消费，非空发件箱拒绝回退 |

未增加延期需求的积分/胶囊/独立搭子等表。词库、举报类型和部分内部处理状态是实现代码，非已交付的真实运营资料；没有预置后台账号、角色绑定、口令、真实词条或学生资料。

## 约束与事务边界

wishes 与当前 wish_versions 采用延迟到提交时检查的外键，创建或改版须在同一事务写聚合和内容。报答方式/校区/分类沿用已批准契约。公开投影必须已审核且涉校检测 completed；500 Unicode 码点上限检查的是心愿 public_body，无法从密文在 SQL 中证明原文长度或 Unicode 全空白，因此原文仍须在 HTTP/服务边界验证。普通文本 btrim 非空约束不替代所有 Unicode 空白校验。

申请 pending_scan/pending_review/active 使用部分唯一，accepted/历史终态不占该索引；current 匹配部分唯一并校验许愿人对应心愿作者。是否允许新申请、三条额度/B01、账号当前存续和限制、当前状态与匹配一致性均需要后续持有账号 guard 的领域事务，不能靠静态索引判断到期或跨表权限。

消息 delivered 必须有正 delivery_seq 和 delivered_at，其他状态两者必须为空；会话/非空序号唯一及会话/发送者/client_message_id 幂等。next_delivery_seq 是普通事务行字段，初始 1，不使用序列；测试证明事务失败不会消耗字段计数，但送达分配、晚到审核取消和接收者拉取尚未实现。

penalties 对 minor/warning、general/mute 七天、severe/ban 永久做一致性约束；警告的 first_displayed_at/confirmable_at 相隔 30 秒，ack 不得更早。首次展示只写一次、当前处罚效力、申诉和解除不恢复旧关系仍是后续服务职责。公开读取必须反连接有效 privacy_holds，旧审核不能释放屏障；表结构本身不会自动隐藏仍存于版本表中的旧 public_body。

普通私有对象/业务关系外键默认限制删除，不能以账号 CASCADE 跳过外部对象清理或幸存方处理。已有身份占用/会话等局部级联只清理普通关联。证据和 privacy_holds 不依赖普通账号/匹配外键，避免普通注销把证据或公开屏障顺便删掉；真正注销/证据删除仍需 T031/T035 协议。

## 显式命令

通过仓库外注入 APP_DATABASE_DSN 指向专用业务库：

```sh
bin/migrate up
bin/migrate permissions
```

up 追加到版本 5，down 在单事务逆序回退全部版本；任何版本有数据时拒绝且全部回滚，不自动删数据、不使用 DROP CASCADE。循环外键在安全空表回退时显式解除。permissions 由管理员执行，迁移组拥有表/evidence schema，并获当前专用数据库的 CREATE 权以便未来受控建立 schema。升级至新版本后要显式重放权限矩阵；老部署首次增加 schema 且旧迁移组尚无数据库 CREATE 时由专用数据库管理员执行本次升级，不能临时给运行组提权。普通启动没有迁移或权限操作。

## 权限矩阵

所有组都是 NOLOGIN，不创建凭据或真实成员；表权限按显式清单重置，撤销 PUBLIC 和保留运行组的旧表授权，不建立未来所有表的默认宽授权。运行组无 public/evidence CREATE 权，不能读取迁移账本；真实账号若同时拥有其他组/直接授权，须单独审计，不能由此表推断其权限。

| 组 | 允许的范围 | 明确排除 |
| --- | --- | --- |
| liyuanci_migrator | 当前业务表/schema 所有者及专用库 CREATE | 不给普通请求使用，不与恢复迁移组混用 |
| liyuanci_api | 普通账号、会话、心愿/申请/聊天/故事写入，必要身份绑定/任务入队及状态读取 | 后台角色写入、检索轮换维护、证据域、迁移账本 |
| liyuanci_worker | 原有探针角色仅读账号 | 消息正文表与身份密文 |
| liyuanci_domain_worker | 显式业务聚合读/更新、审核/工作/发件箱/回执/通知处理 | 身份字段、后台角色、证据域 |
| liyuanci_identity_maintenance | 身份检索配置、占用及姓名密文维护 | 业务/证据和后台角色 |
| liyuanci_admin_api | 审核所需普通密文/状态、案件审核/处置及词库维护、受控访问留迹 | 后台角色绑定写入、证据域；危机证据须用专用连接 |
| liyuanci_admin_maintenance | 后台账号/角色绑定维护、后台会话撤销所需表操作 | 证据域、迁移账本 |
| liyuanci_privacy_maintenance | 显式普通清理表的读取/更新/删除，屏障/清理工作入队 | 后台凭据/角色、词库、检索配置、证据域 |
| liyuanci_evidence_reader | 证据域只读 | 普通身份库、证据写删 |
| liyuanci_evidence_writer | 证据域增改，动作留迹仅追加 | 证据删除、普通身份库 |
| liyuanci_evidence_purger | 证据域读取/状态更新/删除 | 迁移、普通身份库；实际永久删除须先落独立恢复日志并经超管授权 |

这些是后续服务使用的数据库技术组，不是应用内 review_operator/crisis_operator/super_admin 的权限实现。当前没有后台登录、角色管理、危机或清理接口，不因数据库组存在而宣称 REQ-019 完成。不同组和 schema 也不构成独立服务器/租户隔离或外部密钥隔离证明。

sessions 启用行级安全：普通 API/普通隐私清理仅能操作学生主体，后台 API/后台维护仅能操作后台主体；权限配置前运行角色默认拒绝。重放会重新启用被关闭的 RLS；检测到未知会话策略时拒绝覆盖，避免保留额外放权或擅自删除现场限制。运行组若拥有当前数据库或 public schema，也拒绝继续，因为普通 REVOKE 不能去除所有者权限。表所有者仍可迁移维护，业务连接不得使用所有者身份。测试实际验证双方看不到对方行且跨域插入被拒绝；同一命名空间内的本人、代次、角色和案件授权仍由后续会话服务复核，不能用这层 RLS 代替。

## 独立恢复日志结构

恢复日志不进入上述六个业务版本。通过独立外部 APP_RECOVERY_DATABASE_DSN 指向专用恢复库：

```sh
bin/migrate journal-up
bin/migrate journal-permissions
# 仅在日志为空且确需撤销结构时，显式执行；非空拒绝。
bin/migrate journal-down
```

独立 recovery_schema_migrations 保存其版本和哈希；recovery_delete_journal 保存随机 intent_id、范围码、包含技术实体/对象标识的信封、技术时间/确认和单调 record_no。这个序号不是聊天序号。业务/恢复命令在数据库内使用同一互斥锁，互相检查对方账本/标志表，拒绝在同一数据库混建，即使另一侧已安全回退也保持专库边界。

恢复 migrator 拥有结构；writer 只可插入/读取并更新 confirmed_at，不能修改旧意图或删除；reader 只读；retention 才有显式清理权。重放权限会移除已存在业务组的历史误授权，不在独立服务器创建缺失业务组。真实保留窗口覆盖/到期清理由未来恢复流程核验，数据库 DELETE 权本身不会校验 90 天窗口。

D02 未交付时没有真实恢复信封加密客户端，D05 未交付时没有独立持久服务、物理卷/备份隔离、删除意图写入流程、恢复先重删或生产回滚保护。测试仅使用同一隔离 PostgreSQL 容器内不同逻辑数据库，不能冒称物理隔离或恢复验收完成；生产必须使用独立资源及凭据、密钥和备份策略，禁止同业务备份一起恢复。

## 本次验证范围

`make fmt/check/build`、`python3 scripts/check-migrations.py --container` 和 `git diff --check`。真实入口在随机容器内建立迁移、业务 schema、恢复三个逻辑库，执行 race/cover，含旧版追加、空表安全回退/拒绝非空回退、专用迁移角色重建 schema、全部注释、关键唯一/FK/CHECK、计数器回滚、实际 SET ROLE 正反权限和历史误授权重放清除。仅本次容器停止移除、保留卷；不操作已有开发/生产库。远端 CI、生产身份、真实 D02/D03/D05 及性能/恢复未验证。详细实际结果保留在阶段记录。

外部依赖仍按[交付清单](dependency-deliverables.md)未交付记录；D03 不是本结构的阻塞，D05 的真实独立持久资源与恢复流程不由同容器逻辑库测试代替。


2026-10-21，T007/REQ-006、007、015、017、018、020：版本 6 为安全重驱动增加 nullable superseded_at（默认未替代），不修改旧事件 sent_at 来伪造发送确认，不重置旧 attempts。旧事件按原 ID 保留，重驱动新事件具有独立 UUID，工作和 operation_id 不变。显式 permissions 给后台组 consumer_kind/operation_id 两列 SELECT/INSERT，不给 completed_at 读取或 UPDATE/DELETE；重驱动的后台会话、角色和当前业务复核仍由命令入口负责。索引/字段与中文注释已在真实 PostgreSQL 验证，具体结果和未接线范围见第四阶段。
