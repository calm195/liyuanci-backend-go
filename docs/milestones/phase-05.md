# 第五阶段：后台本地账号与会话

2026-10-21（Asia/Shanghai），后端会话。T008独立后台子范围；REQ-002、014、016、018、019。直接前置T006：用户本轮明确“T005/T006可认为已验收”，据此记录两项验收通过；这不是本会话新运行了所有历史外部资源测试。原实现/合成资源边界保留，T008不因此自动验收。

契约0.2.0来自docs提交 `9b775900422fdfec1fb222ce2b84bbb355f63b91`；前序MQ成果已按本轮授权提交 `32fb4c6`，本阶段随后提交。起始后端分支feat/backend-foundation，完整HEAD `550be1e4f340008113c44e9b3c1273d49e7ac84f`。起始前序工作区均为本会话已确认归属的MQ/协调文件，无重置或覆盖。

## 已实现范围与职责

`internal/adminauth` 负责独立后台本地身份，不借用学生身份/资格。Config要求显式SessionTTL、命令/回执时限、仓库外初始口令及独立回执密钥；年级字典与生产TTL未定，代码没有替用户决定。后台默认禁用；缺配置拒绝启用。首个超管通过离线 `cmd/admin-init` 显式初始化，任何后台账号已存在就拒绝，既不重置也不接管历史外部身份。默认初始口令遵循用户已确认策略，由仓库外文件注入，不在代码/镜像中硬编码。未新增强制改密门槛。

`httpapi.RegisterAdmin` 在明确装配后开放四个真实操作：POST /api/v1/admin/accounts、POST /api/v1/admin/sessions、POST /api/v1/admin/password、DELETE /api/v1/admin/session。创建返回201，登录200，改密/退出204空体；使用0.2.0标准封装和错误码。严格JSON拒绝未知/重复字段、null、尾随JSON；领域写要求Bearer及Idempotency-Key，敏感成功/失败响应均no-store。默认未装配仍只有健康端点。整体readyz继续因学生认证/密钥未就绪返回503，不把后台接口局部可用当全业务就绪。

服务/仓储分开：业务流程在service.go，固定SQL和GORM查询在repository.go，HTTP只验证协议/映射状态。数据库查询WithContext且禁用GORM SQL日志，防止摘要、口令/令牌或创建理由进入技术日志。每项身份操作记录中文完成日志operation/request_id/result/duration，panic恢复只记稳定失败；HTTP完成日志保留失败/恢复最终状态。资源装配开启/关闭也记录完成日志，不记录DSN/文件内容或驱动原文。

后台有专用连接池，必须连接同一业务PostgreSQL权威并使用专用维护组身份；开启时拒绝当前/原始超级用户、RLS绕过、属主、创建角色/库及其他业务/迁移/证据组成员，不接受超级用户SET ROLE伪装隔离。外层先等待HTTP请求退出，再关闭身份池，最后清理共享资源。普通启动不迁移、不配置权限、不创建账号。

## 口令、会话、授权和幂等

口令使用固定版本scrypt格式、N32768/r8/p1、16字节随机盐及32字节输出；同初始口令每账号摘要不同，不截断Unicode/长口令，不从数据库接受任意计算参数。CPU/内存计算最多两并发；创建/改密在计算前先轻量查有效后台会话，这只是资源准入，持锁后再次授权，不能拿初读快照提交业务。

登录必须使用真实共享Redis限流：账号和来源分别用独立密钥HMAC映射为技术键，计数及窗口过期通过同一Lua脚本完成，成功/失败尝试都计数。限额/窗口显式注入；错误503、超限429，不做缓存故障放行或永久账号锁定。未知账号和错口令均401，缺本地摘要/禁用账号拒绝。高代价校验在事务外；取得账号guard后重读摘要、状态/代次，匹配才按数据库当前时间签发绝对期限会话，防止旧口令在改密之后签发可用token。只在提交成功后返回高熵不透明token，数据库只存SHA256摘要。

超管创建复用T006根事务/当前授权及回执，创建账号、角色绑定、创建理由留迹与回执同事务；同键同摘要重读当前结果，同键异请求409、重复登录名409，重放仍检查当前超管角色。登录名精确区分大小写，空白名/理由拒绝，不自动去空格/改名。允许空角色账号登录和本人改密，但不给其创建权限。

改密按后台guard→会话锁序，锁后检查当前状态、代次、撤销/到期；旧口令错误403。成功同事务更新摘要/账号version/auth_epoch、撤销该账号全部旧后台会话、写改密留迹和回执。口令请求摘要使用独立密钥HMAC，不把可猜测口令的裸SHA请求摘要或正文放进回执。204后重新登录；旧token重试401，新有效会话同键重放仍重新授权且不再次改密。留迹/后续步骤失败整体回滚，不保留部分更新。两并发旧会话改密只有一个提交，另一方401，不覆盖新代次。退出只撤销当前有效会话；学生token不能冒充后台，禁用/代次变化/绝对到期均即时拒绝，无自动续期。

## 迁移、权限与持久留迹

第七版0007_admin_local新增admin_accounts.login_name（历史可NULL，非空精确唯一）和admin_account_actions。表、列与新增索引均有中文数据库注释，真实目录检查覆盖；前六版本上下行不改写。已交付的新账号或留迹非空时down拒绝，不删除登录名或动作记录。维护组增加后台sessions INSERT、命令回执必要字段和动作留迹SELECT/INSERT；仍不能写学生会话或修改/删除动作留迹。普通后台API组没有增加账号写入权限。权限重复执行和正式迁移CLI验证另记结果。

动作留迹只存技术主体引用、动作、时间与创建理由/固定改密用途，无口令/令牌；该表是本范围账号操作留迹，不替代全系统访问审计或D05独立删除日志。后续角色管理等动作若交付须单独扩展，不把SQL测试改角色当已上线角色API。

## 本轮实际验证与快照

- `go mod tidy`：通过；uuid与x/crypto从既有间接依赖提升为直接依赖，版本未变，go.sum无差异。
- `make fmt`、`make check`（只读格式检查、go vet、go test -race -cover）、`make build`：通过；新增admin-init二进制。初次普通沙箱tidy遇到缓存访问限制，授权重跑后通过，未隐瞒该限制。
- `python3 scripts/check-adminauth.py`：最终项目 `liyuanci-admin-check-c871b3eb4a39` 通过，真实后台用例11.72s，包覆盖率80.4%。固定digest Docker PG/Redis、随机独立网络/loopback端口/凭据、真实独立数据库登录身份与实际HTTP处理器（httptest）；资料均合成，无替代数据库/缓存。项目容器和网络已清理、卷保留。此前成功项目57a948f268aa等用于修复前快照，不代替最终结果。
- 覆盖初始化首次/重复拒绝、登录成功/未知/错误、令牌摘要、超管与普通角色正反、同键/异摘要/名称冲突、并发创建、撤权后回执拒绝、改密错误/成功/并发、留迹故障原子回滚、全部旧会话失效、角色为空本人改密、主体禁用/代次/到期、学生token拒绝、RLS及审计写入权限、Redis原子窗口/超限/故障拒绝、HTTP状态/脱敏/no-store、异常及完成日志。
- 初轮真实检查发现安全DTO的Roles被GORM误解析并输出默认诊断；补明确忽略持久字段，集中仓储查询后最终复验无该诊断。后来补充超级用户SET ROLE不能伪装受限身份及事务外计算准入，最终真实检查包含这两个防护。
- `python3 scripts/check-migrations.py --container`：真实七版本结构/中文注释、身份/命令/durable回归和锁定Go1.25镜像/正式UID10001迁移CLI验证已通过；最终制品 `liyuanci-migrate-check:e83c5d3f1c6d`，完整检查退出0；迁移79.7%、identity81.2%、command83.5%、durable78.0%。镜像离线测试含adminauth/httpapi，不将无服务DSN的跳过当真实身份验证。
- `python3 -m unittest discover -s scripts -p test_services.py -v`：13项通过。未改启停控制脚本，不重跑无关真实启停/监控/MQ探针验收；未运行生产部署或初始化生产账号。

按本次源码SHA256记录，提交后完整SHA由最终回报锁定；不把后续工作区测试归给旧HEAD。

## 状态、缺口与协同变化

本端后台独立子范围“本端验证通过”；T008整体“开发中”，未签整体/三端联调验收。后台账号列表分页、角色修改HTTP、后台会话读取、微信真实身份/学生会话、罚限/学生资格等后续范围未交付。已有真实DB撤权测试不是角色管理API交付。年级字典/生产TTL、实际部署参数与凭据交付/跨端验证仍缺；按影响阻塞对应准入，开发可用合成测试TTL显式注入，不视为选择生产参数。

相对docs起点a5e6124，协调提交 `56863509d6b5b5fb12af8d690148376fbab2bb6d` 已更新progress/blockers/integration-log：R1汇总三端证据、B01待验证、B02/B03/B05/B06/B07政策与协议处理中、新增B14契约迁移；两端新进展及MQ独立记录已汇总。该提交已是契约提交9b77590的父提交，取回核对后无需合并或覆盖；本会话没有修改共享plans。用户本轮T005/T006验收确认和当前后台交付需由docs协调会话后续汇总，不能由旧表倒推当前未获授权。

B02本端实现/隔离身份正反已补，仍需管理端按0.2.0适配和真实联合验证；B07的TTL/角色刷新及年级、B09微信资源、B14两端迁移仍保持。B03/B04学生卡和B05/B06审核词库仅契约不因后台服务完成而关闭。下一步提供本轮提交/配置说明给两端协调，锁定参数与快照后验证后台登录/改密联调，再按依赖推进T008剩余范围。无部署/上线授权；本轮用户已明确授权依功能依赖提交和推送，禁止强推。

## 本轮源码指纹

```text
9d4bd11921438e1216fa66c25c11d6f6f73ced09c82026d1381b474c597b98a2  internal/adminauth/service.go
0d55b9602f104685e06c3b543d33d209a1eb80fda1ea9390b49c733ca70888f6  internal/adminauth/repository.go
4b5087fda87ede567c6b29be855b6b51e32769c3234c56bdda3238d45f186c73  internal/adminauth/open.go
a2d23510394d492e872c96ca0840bfc1a46b779bc28b3aa2bc638e9dc9a68942  internal/adminauth/password.go
09fe0a06aebdf2bfca2cc99d03d76fc510e03143ec9565523712b5208e936f95  internal/adminauth/limiter.go
a21111e5fd0c0691d12512de6df8685411ec478b536f279d3006d91f55688806  internal/adminauth/integration_test.go
a4cdd93d2b689ee2326dad8e83cf43398928ab062f7ba558acf57ee534df9155  internal/httpapi/admin.go
57f711114ef05d6ae82fadcb81066092b8a45c7fb6d59e19047040c9d2d917f4  internal/migration/sql/0007_admin_local.up.sql
ea1e985c9dfc0f3da0e0a464d5ef955eac0c0b2818bba2bab6db443d5551b1a4  scripts/check-adminauth.py
```

Python语法检查通过（临时缓存目录），两仓git diff --check通过；最终规定质量检查通过。正式镜像已包含本轮最后的会话计算准入与连接隔离，未推送镜像或部署。

## c927db0协调增量：B05/B07及INT-003准备

2026-10-21后端会话；T002/T008/T010，REQ-002–008、015、018、019。完整映射、版本缺口、正式静态动作词表/未激活Wire扩展、后台角色刷新与学生策略边界、资产查询建议和标签更新未决项，以及INT-003交接清单/I3-01–08预期见[本轮交接](../contract-int003-handoff-2026-10-21.md)。本端起点8ba233f7a3c4edab32de8331f775aefd14b2dcad；docs基准c927db0fa2a1b12b1ded49bc77e23991cc80325d。未改现有共享五项改动、共享plans及运行代码，新增静态指纹manifest；没有本轮提交或推送/部署。

T002静态核对子范围本端验证通过，整体开发中（B05/B07/B14阻塞）；INT-003开发中（环境/实际配置/管理端接线缺），未达到待联调，未发真实请求。历史后台PG/Redis/HTTP验证不作为本轮跨端通过。B05目标/案件独立version缺DDL，新增target/动作响应和目标ID决策继续草案；B07词表只固定静态范围，年级字典、生产TTL、学生体验、标签更新语义不自行编造。模块职责保持service业务、repository查询、HTTP验证、外部配置/显式迁移；本轮不引入领域运行行为。B02–B13按原解除条件保留，B14只部分解除。

本轮文档完成验证：`make fmt`、`make check`（只读格式检查、`go vet ./...`、`go test -race -cover ./...`）退出0；单元结果含缓存，未配置集成DSN的真实集成不计本轮验证。两仓`git diff --check`通过。无Go/依赖修改，未运行`make build`；未重跑Docker后台/迁移检查或真实跨端联调。最终新增/未提交文件列表以本轮回报为准，无本轮提交。

## B07正式提交收口（后续增量）

2026-10-21后端会话；T002/B07，REQ-002–008、018、019；验收关联C01/C06、002-A–C、019-A/B适用静态子项。用户本轮明确授权将可独立交付的动作索引/生成校验形成正式提交。修改前两仓状态与前轮一致，八文件旧指纹核对无变化；沿用本会话已审查范围，未覆盖其他会话文件。docs main正式提交 `f8c35f93fcc10cd2b4adc3f3b67ae4e4736b0b87`，父提交为协调基准c927db0fa2a1b12b1ded49bc77e23991cc80325d。

本次七文件：contracts/README.md、actions.v0.2.0.json、actions.v0.2.0.md、actions-review.v0.2.0-r1.json、authorization.py、build.py、validate.py。正式输入为契约0.2.0 / 静态动作索引revision 1。索引SHA256 `1c6b67db585a9f80970bdde16b996c145fb5627ee56c86de2ce82af3bfb2ebd2`；manifest SHA256 `0d377b65bf7a6f353ecf620f1db403d3117debbb239084e4fd5791c61a0680ba`。其余生成源/文档指纹由manifest固定；OpenAPI/examples保持原指纹。

authorization.py只生成/校验正式静态索引，移除对审核扩展的生成/读取依赖；GET列表不作为WishResult写动作提示。允许动作仍必须来自当前投影并绑定当前主体/目标；加入缺失/未知/错误投影/跨账号/跨心愿的合成拒绝校验。83项是协议目录，不是83个运行授权。现有四后台操作以外的服务状态未变。消费说明列选图上传、首次/重提认证、新建/改版/删草稿/送审/上架及版本归属、403/409边界；后台角色刷新不套给学生。年级字典/部署TTL、学生会话体验、标签更新省略/清除、查询节奏与页面实现仍未冻结或交付。

moderation-extension.draft.json保持原字节/未跟踪，未提交、未激活。B05需用户决定的取舍单列在共享actions.v0.2.0.md：registry精确正文资源还是独立稳定实体处置资源；新增字段/独立版本模型的兼容发布范围。不得直接作为0.2.0启用，未改迁移。

验证：实际build.py生成83操作/130 schema；validate.py通过783示例、5原负边界/8新增负例及B07合成消费边界；make fmt、make check（格式检查、vet、race/coverage）退出0，单元含缓存。无Go/依赖修改不运行make build；不运行真实PG/Redis/HTTP或跨端服务。LibreSSL/RefResolver告警保留但校验退出0。Git默认沙箱不能写index.lock，经授权沙箱权限重试后提交成功，无强推。共享plans未修改、未推送或部署。

状态：B07正式静态输入子范围本端验证通过；T002整体开发中（阻塞），B07/B14不整体关闭，等待两端按上述完整SHA消费并验证，资源/真实业务分别按原阻塞处理。INT-003仍仅准备，无本轮真实联调证据。本节覆盖前文“索引未提交”的时间状态，旧快照指纹保留为历史，不可用于本次正式接入。

正式提交干净制品复验：`git archive f8c35f93fcc10cd2b4adc3f3b67ae4e4736b0b87 contracts`导出至`/tmp/liyuanci-b07-committed`，执行该副本build.py/validate.py均退出0；manifest八项指纹一致，目录中不存在moderation-extension.draft.json，证明可独立生成/校验。最终两仓git diff --check通过；后端阶段文档同步后make fmt/make check再次退出0。

## R2实际隔离环境交付与准入剩余（2026-10-21）

T002/T008/T010，REQ-002–008、015、018、019；本轮后台实际子范围REQ-002域隔离、018、019。开始分支feat/backend-foundation，完整HEAD `764fd9f8a044ea2cf275b77eece2743895cb8166`，工作区干净。共享docs正式输入 `f8c35f93fcc10cd2b4adc3f3b67ae4e4736b0b87`（0.2.0/index revision 1）；三份共享plans既有修改及审核草案保持原状，不提交、推送、部署生产或跨会话发消息。

新增[准入包](../int003-r2-readiness.md)，补逐操作目标、允许条件/未定状态与版本来源；B05独立版本/处置资源身份与Wire兼容仍待评审，草案未激活；B07正式索引独立生成、指纹和失败关闭核验完成，不能据此关闭其余阻塞。后台TTL/角色刷新与学生策略分开，查询生命周期/重入技术建议、年级字典、标签更新清除未定项保留。T007领域worker、取消、管理审计及同一真实PG+MQ领域闭环/ACK故障单列，不在本轮实现。

用户本轮指定Docker使用启动脚本。已通过init-dev/start创建专用liyuanci-int003项目：内部PG/Redis/NameServer/Broker、独立卷/网络；显式migrate up/permissions至七版，受限DB LOGIN int003_admin仅继承维护组；仓库外secret与测试参数明确；显式admin-init创建首超管，额外后台容器loopback18082。运行代码模块职责不变：迁移CLI管理版本/权限，装配层校验受限池与凭据，service处理身份，HTTP负责校验/状态映射。普通启动和18080基础API仍不启用后台；额外容器显式挂载配置及secret，不改Compose或运维入口。隔离用合成身份，无真实学生/外部密钥/私有桶。

首次start中间件和基础API已起来，但未初始化探针表/主题，worker退出使脚本失败；通过原脚本显式bootstrap-probe/no-build恢复，最终start退出0。排查时直接Compose构建因缺外部密码环境拒绝，未传明文补救，仍回到正式启动脚本。宿主18082 healthz200、readyz503；迁移账本1–7，受限角色五项高权限均false，表/列缺注释计数均0。后台运行镜像ID及secret/账本/配置非敏感指纹记录于准入包，口令/DSN/token不入仓库或报告。环境保留，stop按项目标签覆盖新增容器且不删卷。临时secret目录需环境方接管，不宣称生产持久secret交付。

真实验证：check-adminauth.py独立随机项目liyuanci-admin-check-3ecd54392ade退出0，真实PG/Redis/httptest、TestPostgresAdmin11.79s、包覆盖率80.4%；check-migrations.py --container退出0，制品liyuanci-migrate-check:7d3562d3a92c、正式CLI/UID10001/七版/注释/权限/回退验证通过；services单元13项通过。正式契约archive独立生成/校验退出0，83操作/130 schema/783示例及动作正反例，八项指纹一致，不依赖审核草案；初次Python3.9解包参数不兼容已修正，LibreSSL/RefResolver警告仍存在。

状态：本端环境/材料已准备；INT-003未执行、未登记联调通过，需协调核对管理端完整SHA/正式消费指纹、真实传输与内存token/清理行为后准入；创建、登录、改密、退出四操作均纳入，普通合成账号在创建步骤产生。后台局部不等待T011准备，也不代表完整认证通过。B05/B07/B08–B10/B14和T007剩余边界保留。无Go/依赖/运行配置源码变更；本轮不以历史用户T005/T006验收冒称外部资源或学生闭环完成。

最终验证：`make fmt`、`make check`（只读格式检查、go vet、go test -race -cover）及`git diff --check`退出0，常规单元含缓存，缺服务DSN的跳过不算真实集成；上文独立真实检查另计。`start.sh dev ... --no-build --probe`退出0，验证原有真实PG+MQ探针及worker回执，不是T007领域闭环。无Go/依赖变化，未另跑宿主`make build`；启动脚本及迁移镜像检查已实际构建全部五个Linux二进制。未改启停入口，未重跑完整check-services.py管理/监控栈；生产、真实学生、通知与跨端均未执行。

## 文档提交与主分支合并复核（2026-10-21）

按用户明确要求提交本项目变更并合入主分支。本次提交范围为T002/T008/T010、REQ-002–008、015、018、019的R2准入及交接文档，未新增运行能力，既有未完成和未验收边界保持上述状态。刷新远程分支后，仅有main与feat/backend-foundation，远程功能分支已包含于本地功能分支。提交前实际执行`make fmt`、`make check`和`git diff --check`均退出0；`make build`首次完成但有沙箱缓存写入警告，放开缓存写入后重跑退出0。此次未重跑真实中间件或跨端联调，历史证据不作为本轮新执行结果。不推送远程、不部署。

## 后台账号管理增量（2026-10-21）

T008/REQ-018、019；本轮用户授权逐任务实施、按模块依赖提交并新开分支，不考虑跨端联调。分支codex/backend-domain-services。补齐GET账号列表和PUT角色替换，复用现有独立受限后台池、命令根事务及当前角色授权；默认关闭，普通启动不迁移或初始化。模块职责：accounts.go负责流程/签名游标，repository.go负责单SQL分页角色聚合和角色替换，HTTP处理严格输入/状态，新增第八版仅扩展留迹动作及中文数据库注释，前七版不改。

分页limit默认20上限50、UUID升序，HMAC游标绑定主体/用途；每页当前超管鉴权，非快照全量导出。角色写按全局guard锁序、目标账号版本复核，角色/版本/留迹/回执共同提交；重放当前授权，撤权不能靠旧回执提权；不变更auth_epoch，不新增最后超管限制。日志含中文完成消息、operation/request_id/result/duration，不记录理由/登录名/正文。详细接口见admin-auth.md及共享contracts后台账号管理说明。

已执行真实Docker隔离PG/Redis检查python3 scripts/check-adminauth.py：项目liyuanci-admin-check-fa64836aa731退出0，TestPostgresAdmin13.26s，包覆盖率81.3%。新增分页不重复、签名篡改/跨主体拒绝、普通运营禁止列表/提权、角色成功及同键重放/异摘要、旧版本/并发冲突、留迹故障回滚、自身撤权后重放拒绝；HTTP协议测试覆盖参数/字段/版本错误。身份与口令为合成资料，PG/Redis/事务/权限真实；非跨端联调或生产验收。新增游标签名单元回归后规定检查和迁移镜像最终结果在下节记录。

T008整体仍未完成：微信/学生会话与资格后续实现；T007领域worker随对应业务接入。审核处置用户本轮明确使用registry.id绑定精确正文版本，供T016及共享契约后续演进使用，不在本模块实现审核。未推送、部署或修改共享审批/需求/设计。

本模块最终验证：新增第八版的`python3 scripts/check-migrations.py --container`退出0，正式镜像制品liyuanci-migrate-check:55000a0d2171；八版结构/表列中文注释、权限、空库上下行和正式UID10001 CLI通过。迁移79.7%、identity81.2%、command83.5%、durable78.0%。本次无跨端联调、生产、微信/对象桶外部调用。共享contracts生成83操作/130 schema，validate通过783示例及动作正反例；只将后台列表/角色两个已有操作更新为implemented-opt-in，不更改正式Wire字段，审核扩展保持未激活。Python已有LibreSSL/RefResolver告警，退出0。初次宿主Go检查受系统缓存沙箱限制，授权重跑通过；不将受限失败当通过。

共享静态动作索引推进revision 2，新增actions-review.v0.2.0-r2.json固定当前输入；r1清单仅作历史保留。最终`make fmt`、`make check`（格式只读校验/vet/race-cover）、`make build`及两仓`git diff --check`退出0；本模块无依赖变化。新增游标签名单元测试已纳入最终check。未推送、部署或执行跨端联调。

后续启动防护：checkSchema显式检查角色留迹约束包含roles_change，旧结构即使有表/列也拒绝后台启用，普通启动不补迁移。受限真实LOGIN的Open正反例覆盖该边界。复验`make fmt/check/build`退出0，`python3 scripts/check-adminauth.py`最终项目liyuanci-admin-check-d0b6a9f4cb8b退出0，TestPostgresAdmin12.67s、覆盖率82.0%；游标签名回归也纳入本轮真实检查。前述迁移镜像测试对应第八版DDL未变化，不冒称新的镜像复跑。
