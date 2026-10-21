# R2 后台身份局部联调准入包

2026-10-21（Asia/Shanghai）；T002/T008/T010；REQ-002–008、015、018、019，实际环境仅涉及REQ-002后台域隔离、018、019。状态：后端环境已准备，本端验证结果见第五阶段；INT-003未执行，待协调会话核对管理端快照与接线。小程序不参与本次局部联调。原交接的“环境实例未交付”由本文现状覆盖，历史测试不扩展为认证验收。

## 权威输入与动作边界

后端分支feat/backend-foundation，环境源码完整HEAD `764fd9f8a044ea2cf275b77eece2743895cb8166`；开始时工作区干净。本轮只有文档改动，无提交/推送。docs main正式输入 `f8c35f93fcc10cd2b4adc3f3b67ae4e4736b0b87`，契约0.2.0、静态索引revision 1；共享plans三份既有改动和未跟踪审核草案保持原状。

正式入口为共享 `contracts/actions.v0.2.0.md`、`actions.v0.2.0.json`，逐操作HTTP方法/路径、path_targets、projection、version_fields与实现状态以该提交为准；[原交接](contract-int003-handoff-2026-10-21.md)保留领域目标映射。索引SHA256 `1c6b67db585a9f80970bdde16b996c145fb5627ee56c86de2ce82af3bfb2ebd2`，指纹清单SHA256 `0d377b65bf7a6f353ecf620f1db403d3117debbb239084e4fd5791c61a0680ba`；清单包含八项输入，不能把其中base_commit当索引正式提交。83项是协议目录，79项未实现。

| 正式operationId | 目标、允许条件与版本来源 |
| --- | --- |
| postAdminAccounts | 当前有效后台超管创建新账号；无expected_version；角色由服务端当前数据库确认，空角色账号无创建权；幂等键必填 |
| postAdminSessions | 本地后台账号；有效且存在本地口令摘要，凭据正确且限流/依赖允许；无expected_version，不接受客户端角色 |
| postAdminPassword | 当前有效本人后台会话；不要求特定角色，必须验证当前口令；无expected_version，以锁内当前账号auth_epoch/会话为准，幂等键必填 |
| deleteAdminSession | 当前有效本人后台会话；无expected_version；撤销后重用401，幂等键必填 |
| postAdminModerationTasksIdDecision | 案件id，expected_version属于案件；现有DDL缺独立案件版本，正式响应无目标动作投影，真实入口不启用；human_required候选矩阵仍未激活，不能当已冻结允许状态 |
| postAdminContentsKindIdReleaseRestriction | kind/id内容处置目标，expected_version属于该处置目标；目标稳定身份与独立版本仍待B05评审，不得借案件id/正文版本填充，不启用 |
| putAdminAccountsIdRoles | 账号目标版本；仅协议定义，角色修改HTTP未实现，不以SQL夹具当角色管理能力 |

Me/WishResult的提示必须同时满足：已知operationId、适用投影、同一主体/目标、当前服务端动作集合；版本从命令目标取得。缺失/null/空集合/未知动作/跨目标均拒绝相应动作，未知项不能扩权；GET不是写授权。服务端仍重查资格、状态、版本及屏障。首次认证无记录省略expected_version，重提取Verification.version；改正文/删草稿/送审/上架取当前心愿版本，送审还携当前正文版本。尚未交付的领域状态机不从静态索引推断为可调用。

B05扩展 `moderation-extension.draft.json` 为draft-not-active，不在正式提交，也不参与生成校验。registry.id精确正文资源与稳定实体级处置资源的取舍、新target/allowed_actions与案件/目标独立版本的兼容范围待确认；须独立版本及影响评审、追加迁移、并发/锁/屏障及两端适配，不改写已应用迁移或冻结0.2.0。

## 当前可访问环境

用户本轮指定用启动脚本提供Docker环境。专用项目 `liyuanci-int003`，网络 `liyuanci-int003_foundation`（内部）与`liyuanci-int003_ingress`；PG/Redis/NameServer/Broker沿用compose.integration.yaml锁定digest及独立项目卷。无管理/监控项目启动，不复用生产。基础API `127.0.0.1:18080`默认关闭后台；专用后台容器 `liyuanci-int003-admin-api`发布 **http://127.0.0.1:18082**，跨主机需受控隧道，不绑定公网。

本次真实检查：宿主healthz=200、readyz=503。后者保持学生认证/密钥整体失败关闭，不能据局部存活开放其他业务。没有执行四操作跨端请求。

制品本地不可变image ID `sha256:a1704279ff8a46ced681e539a5ba2c4ad6bc93b63dbcae5a076745b1a48a1273`；并非已推送镜像digest。环境引用 `/private/tmp/liyuanci-int003-secrets/environment-evidence.json`，迁移账本证据同目录`migration-evidence.txt`。临时目录不是持久secret系统，重启或清理前由环境方接管，不保证机器外访问或持久保管。

初始化/启动实际使用：

```sh
./scripts/init-dev.sh --project liyuanci-int003 --config-dir /private/tmp/liyuanci-int003-secrets
./scripts/start.sh dev --project liyuanci-int003 --env-file /private/tmp/liyuanci-int003-secrets/operations.env --without-management --without-monitoring --bootstrap-probe --no-build
```

首次不带bootstrap-probe启动时中间件/API成功，探针worker退出导致脚本失败；上列命令显式建探针表/主题后成功。探针表不是业务迁移，普通start不隐式执行七版迁移。没有改启停入口或Compose，后台启用没有混入普通启动默认值。

专用后台是显式额外容器：复用上述镜像和两个网络，以只读挂载口令/回执文件、`--env-file`注入后台运行配置，执行`-config configs/integration.yaml`。准备辅助文件 `/private/tmp/prepare-liyuanci-int003.py` SHA256 `c5ee141ec91a02315bdb5b37c039621889ee92e72de405c268e9ba68d008076f`，只用于本次新建隔离实例，遇已存在角色/文件停止，不作为通用幂等运维入口；不重复执行以重置账号。所有容器带本项目标签，现有stop可覆盖它。停机后恢复先执行上列start，再`docker start liyuanci-int003-admin-api`；普通start不会自动启动这个额外容器。

## 七版迁移及身份/secret

实际已在基础API容器中显式执行`./migrate up`、`./migrate permissions`，使用该初始化容器已有外部管理DSN，未打印DSN；七版账本为1至7共7条，正式迁移校验器验证checksum。0007_admin_local的up文件SHA256 `57f711114ef05d6ae82fadcb81066092b8a45c7fb6d59e19047040c9d2d917f4`，down为`cf4af9e0b4dc0a0eccdeb4edf367c814ca62f4d1360d64af356f8f69da69212e`。已有账号/留迹不得以down清理。目录表/列缺注释均为0，包括探针表与迁移账本。

随后显式创建DB LOGIN `int003_admin`，随机口令，NOSUPERUSER/NOCREATEDB/NOCREATEROLE/NOREPLICATION/NOBYPASSRLS；实际继承清单只有自身和`liyuanci_admin_maintenance`，不属迁移或其他业务组，不拥有业务表。后台独立连接池连同一权威PG；本次局部实例的基础资源池也使用该受限DSN，只用于依赖连接，不授权学生业务。权限脚本本身不创建实际LOGIN。

凭据引用（均在仓库外，禁止复制值到交接/请求日志）：

| 引用 | 使用主体/作用 |
| --- | --- |
| `/private/tmp/liyuanci-int003-secrets/operations.env`及其postgres-password引用 | 现有启动脚本；仅开发PG初始化管理身份 |
| 同目录`admin-runtime.env` | Docker启动器读取，包含APP_ADMINAUTH_DSN及APP_DATABASE_DSN；0600，不source、不在命令行展开 |
| 同目录`admin-initial-password` | 只读挂载`/run/int003/initial`，容器UID10001读取；按既定初始策略，仅合成隔离身份，不公开口令指纹 |
| 同目录`admin-receipt-key` | 只读挂载`/run/int003/receipt`，UID10001读取；独立随机64字节，实例一致，0600 |

父目录0700；挂载secret的容器UID所有权由初始化步骤设置，宿主普通用户不能据此假设可直接读取。Redis内部无密码，仅隔离开发网络，不对宿主发布，不作为生产配置。所有secret仅Docker执行器/容器读取，文档不保存口令或token。

首个应用超管`int003-bootstrap`已由`admin-init -config configs/integration.yaml`显式初始化，仅空库允许，不自动重置。INT-003创建步骤必须创建独立合成账号`int003-<run-id>-member`，roles=[]，用于登录/本人改密/退出与无创建权403；超管仅用于创建和正向授权，不让四操作全用超管跑。review_operator/crisis_operator也不具备创建账号权；本轮不验证其领域读卡/危机权限。每轮新命名账号/幂等键；已交付留迹不删除，空库bootstrap不能重复运行。

## 显式测试配置及逐次执行

| APP变量（均为ADMINAUTH前缀） | 当前隔离测试值 |
| --- | --- |
| SESSIONTTL | 10m，绝对到期 |
| TIMEOUT / LOCKTIMEOUT | 20s / 5s |
| RECEIPTTTL / MAXATTEMPTS | 1h / 3 |
| LOGINWINDOW / LOGINLIMIT | 1m / 100，账号和来源分别计数，成功也计入 |

运行环境使用integration，Redis启用、地址redis:6379、DB=0、Password空；RocketMQ启用以满足integration依赖规则。过期/限流边界须独立测试批次：TTL实际等待10m；限流先等新窗口，以独立合成账号同来源记录第101次429，避免污染四操作主用例。参数是本轮测试值，未批准生产TTL；不为测试改成前端计时授权。回执期限仅幂等保存期限，不是会话续期。

协调准入后，按[原交接I3-01–08](contract-int003-handoff-2026-10-21.md)执行，I3-08创建在本轮为必测，先创建再登录。请求体键严格为创建login_name/roles/reason、登录login_name/password、改密current_password/new_password；退出无正文。创建/改密/退出均带独立Idempotency-Key，登录不要求。成功依次201/200/204/204；错误口令401、错误当前口令403、无创建权403；重名409，同键异请求409，旧token/撤销/到期401，依赖失败503。保留同账号两个旧会话A/B，改密后两者均401，新口令重登可用；其他账号不受影响。204空体、不尝试读JSON。

原I3-07中学生token域隔离须使用既有本端隔离夹具；真实学生token未交付则跨端项记未执行，不伪造为微信实测。测试执行器仅内存持token/口令；证据记录run-id、请求ID、HTTP、时间、后端/前端完整SHA、配置/迁移/镜像、脱敏完成日志，不记录正文/Authorization。晚到旧响应不得覆盖新会话。

## 会话、生命周期与清除语义

已定：后台登录roles是快照；服务端逐请求读当前角色。401清token/角色重登；403停止该操作，不推断会话失效或新角色；菜单刷新显式重登。改密204清会话重登、退出清会话；token仅内存，绝对到期不自动续期，无新增后台refresh或读取会话接口。

学生独立Bearer与生命周期/auth_epoch/撤销/到期/资格复核已定；TTL、续期/恢复体验待冻结，禁止沿用后台10m。资产单次GET与首次/重提版本来源已定；离开取消、忽略旧代响应、重入立即GET本人当前asset、不复用旧approved，是既有技术建议，尚非冻结页面协议。轮询间隔/退避/总时限、弱网恢复配置及页面接线待协调。approved只代表安全扫描，不等于学生资格。grade受控字典仍缺；gender已定male/female/undisclosed；创建标签省略为未填，null非法。更新标签的省略保留还是清除尚未冻结，不以空对象或PUT方法推断，也不增加null清除。

## 单列剩余工作与准入

T007（REQ-006、007、015、017、018、020）不在本轮扩展：仍缺领域worker接线、当前资格/正文/epoch/hold复核、取消/obsolete不重放、管理操作审计、同一真实PG+MQ领域闭环，以及提交后ACK前故障/重复消息验证；运输和探针不能替代这些证据。

INT-003后端环境材料已提供；仍需管理端完整HEAD/正式输入消费指纹、显式真实传输/地址与内存token、401/403/204/弱网/重复/晚到清理证据，由协调会话核对准入后执行。本轮未发送跨会话消息、未运行两端联调或签验收，不等待T011准备局部后台，也不据此关闭B05/B07/B14或完整认证。B08–B10资源缺失继续停止受影响学生真实适配；T005/T006用户验收不代表密钥/学生身份/业务闭环完成。停机用`./scripts/stop.sh dev --project liyuanci-int003`，不删卷/secret；本轮保留环境供协调核对。

## 本轮证据指纹与复核

非敏感环境引用JSON SHA256 `9a645ea566ec6d6333afc51b82d121c7b7d40d64b1d460e34780f1aadbcdfebd`；迁移账本证据 SHA256 `cc7dc3cc1b61cb2708d3feb94fd8af87576e15c1803ad52343d95f7354a81791`。这些文件位于上文仓库外目录，不对含口令的runtime.env或低熵口令公开指纹。

本次从正式提交git archive到`/tmp/liyuanci-r2-int003-contract/contracts`，生成/校验均退出0：83操作/130 schema、783示例、5原负边界/8新增负例及动作缺失/未知/错误投影/跨目标拒绝；生成前后八文件指纹一致，导出不含审核草案。初次导出使用宿主Python3.9不支持的tarfile filter参数失败，改为逐文件校验路径后导出并复验成功；LibreSSL/RefResolver告警存在，未影响最终校验。

`python3 scripts/check-adminauth.py`退出0，独立项目liyuanci-admin-check-3ecd54392ade，真实PG/Redis/httptest与合成资料，TestPostgresAdmin 11.79s、覆盖率80.4%；并非管理端接线。`python3 scripts/check-migrations.py --container`退出0，正式镜像CLI制品liyuanci-migrate-check:7d3562d3a92c，七版/注释/权限/重复迁移/回退/UID10001及脱敏验证通过。`python3 -m unittest discover -s scripts -p test_services.py -v`13项通过。规定质量检查与最终结果见第五阶段。

`start.sh dev --project liyuanci-int003 --env-file /private/tmp/liyuanci-int003-secrets/operations.env --without-management --without-monitoring --no-build --probe`退出0，真实PG+MQ探针往返/worker回执通过。`make fmt`、`make check`与`git diff --check`退出0；没有Go/依赖变化，未单独执行宿主make build，Docker已构建五个Linux二进制。未改启停入口，不重跑完整管理/监控check-services.py；未做生产或跨端验收。
