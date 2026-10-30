# 后端协作规则

## 工程与代码规范

- 模块为 github.com/calm195/liyuanci-backend-go，采用已批准方案 B；PostgreSQL 是业务权威。API 与 worker 分别构建，基础模式只提供健康端点；生产真实服务必须显式配置，无模板用户示例或业务 Mock 路由。
- cmd/server 负责 API 依赖装配与生命周期；cmd/worker 是隔离集成探针消费者。配置加载放 internal/config，日志与中间件客户端放 internal/platform，Gin 处理器/中间件放 internal/httpapi；领域按模块置于 internal。配置示例在 configs，专用 SQL 在 migrations 及 internal/migration/sql，测试靠近源文件。
- 使用 Go 1.25+；依赖变化后 go mod tidy 并提交 go.mod/go.sum。make run 默认无需外部服务；make build 构建当前 Makefile 列出的进程。真实中间件往返用 make integration。
- gofmt 使用 Tab；导入按标准库、第三方、本模块分组。多字段配置换行，顶层声明间留空行；包名小写，符号 MixedCaps。I/O 首参为 context.Context，显式注入依赖，避免可变全局客户端。HTTP 校验与状态映射在 handler，业务规则在 service，GORM 查询在 repository 并使用 WithContext；包装错误保留上下文，不向 HTTP 暴露凭据或内部错误。
- APP_ 为应用环境前缀；不提交密钥、本地覆盖或生产 DSN。GORM AutoMigrate 仅在显式迁移入口由专用身份执行；普通 API/worker 启动不隐式 DDL，运行身份不授 DDL 权限。实体每表独立结构体，按模块分文件；PostgreSQL 专用约束、生成列、RLS、锁函数与权限继续版本 SQL 管理。
- SQL 每表、每字段必须有中文数据库对象注释（COMMENT ON TABLE/COLUMN），含迁移账本与探针；SQL 行注释不能替代。迁移集成检查验证目录注释非空。已应用迁移不改写，通过新增迁移变更；未交付迁移首次验收前可补齐。
- 注释默认中文，覆盖包、导出符号、关键逻辑和测试说明；导出注释以 Go 标识符开头。技术术语、工具指令如 //go:build 及生成代码保留原样；用户明确要求其他语言时遵循用户。
- 非测试代码的标识符、注释、字符串和日志不出现任务/里程碑编号；追踪在测试和 docs 工程。日志消息与人类说明使用中文，字段名、协议值和必要技术标识稳定。关键业务规则、生命周期、资源归属、恢复及非显然决策变动必须同步准确注释。
- 改动的运行操作在所属边界使用注入的 *zap.Logger 记录结构化完成日志，按需含 request ID、耗时、结果或安全标识；成功后才记录成功，失败/恢复仍保留请求完成日志。不记录凭据、DSN、请求体或个人数据。
- 保留 Git 历史及无关改动，使用 Conventional Commits；PR 说明问题、结果、验证、配置及迁移步骤，只有可见 UI 变化才需截图。

## 验证

日常迭代先运行相关包/相关用例，使用 `make check-fast PACKAGES=./internal/模块/...`；仅在需要定位并发问题时增加 race。一次完整交付前统一运行 make fmt、make check；check 须只读校验格式、go vet ./...、go test -race -cover ./...，CI 保持执行。Go 源码或依赖变化交付前再运行 make build；已通过后仅因新增修改、失败或未解决风险重跑相关检查。测试用标准 testing/net/http/httptest，文件 *_test.go、名称 TestBehavior；校验与 HTTP 失败优先表驱动。单测无需中间件，不设覆盖率硬阈值；真实数据库、Redis、RocketMQ 变化须显式集成检查并说明服务依赖。

失败修复并复跑；环境受限报告命令和原因，未运行不得宣称通过。文档变动报告完成与验证，另在 docs 工程运行文档检查与固定指纹检查。运维专项检查继续按下文执行。


## 跨仓库文档与分支规则

- 统一遵循 [docs 文档规范](../docs/governance/README.md)；本端文档入口为 [端内索引](../docs/development/backend/README.md)。
- 开发工程不保留 docs/、里程碑、设计、评审、联调或验证报告；所有过程产物写入 docs 工程 `development/backend/`。源码旁必要用法说明和根 README/AGENTS 可保留；README 只维护项目介绍、选项/目录、启动及开发须知。
- 任何工程变动前先核对两仓库分支、完整 HEAD、工作区。按用户 2026-10-30 明确要求，源码与 docs 的本地和远程仅保留 `develop`、`main`，后端开发及对应文档统一落在各自 `develop`，经授权合入 `main` 并推送。保护已有修改；目标分支无法安全准备时停止受影响交付。并行任务协调写入范围，必要时使用 detached 工作树，汇总后清理，不保留额外分支。
- 一次完整交付新增一份阶段记录；同一任务内的调试、局部修复与验证复用尚未固定的记录，不按工具调用或每次编辑生成报告。上下文优先读取根规则、端内索引和当前任务相关文档，历史阶段按需读取。
- 已固定阶段文件不修改、不追加、不格式化、不删除；每次完整交付新增 `development/backend/milestones/YYYY-MM-DD-<主题>-rN.md`，证据放同端 `validation/`。注明任务/REQ、源码分支/完整提交、docs 基点、契约版本、实现与未完成范围、取舍、真实/模拟边界、实际检查及依赖，交付时追加 docs 固定清单。
- 勘误或补证据另建文件引用原记录与指纹，更新端内索引；不重写历史完成状态。共享进度/阻塞/联调台账由 docs 协调者维护。契约疑问写 docs 对应端新记录；后端仅在既有授权范围协调共享 contracts/，改变批准语义先评审。
- requirements/original.md 只读；既有需求、审批及设计不覆盖，变更新版本按影响重新评审并由用户确认。AI 不签发 G1/G2/G3；受阻前置只停止依赖它的工作。
- 按功能范围提交；需有用户授权。先提交开发工程，再将精确源码提交写入本次尚未固定的 docs 记录并提交 docs；检查暂存范围，不混入无关改动。已明确授权的动作不重复索取确认；不因本规则自动获得推送、合并、部署或上线权限。
- 修改前说明计划，完成后列出变更文件、风险与实际验证结果；文档用中文，代码标识符保留英文。事实、假设、建议和待确认问题分开。业务需求使用 REQ-001 格式和可测试验收标准，设计/任务引用原 REQ；协作治理不编造业务 REQ。

## 中间件部署约定

日常可选原生PG/Redis模式（`make dev-native-init/infra/api/worker/doctor/stop`）：连接用户已运行的本地PG/Redis，不隐式启停、重置或清理它们，无MQ时完全不要求Docker；`dev-native-mq`最多启动隔离Docker NameServer/Broker。该模式需宿主psql及显式本地数据库管理员身份完成首次初始化，运行身份继续受限，原生口令只从仓库.local忽略目录或显式外部文件读取。

日常默认宿主运行Go API与业务card-worker，入口为 `make dev-init/dev-infra/dev-api/dev-worker/dev-doctor/dev-stop`。本机Docker默认只启动PostgreSQL、Redis；MQ、管理工具和监控显式按需开启。基础设施常驻，普通修改只重启对应Go进程，`dev-stop`保留中间件，`dev-infra-stop`显式停止所选隔离项目并保留卷。`make run`仍为健康模式；旧完整Docker入口用于集成验收。开发凭据一次自动生成并复用，保留权限、RLS、加密和幂等行为，不引入生产资源。

T003/T004/REQ-018、020：开发集成及未来生产的 PostgreSQL、Redis、RocketMQ（NameServer/Broker）统一使用 Docker 部署，保持环境、网络、数据卷与凭据隔离。配套管理工具使用 pgAdmin、Redis Insight、Apache RocketMQ Dashboard 官方发布镜像，固定版本与 digest；管理端口仅绑定 loopback，远程经 SSH 隧道访问。生产配置独立于集成探针，凭据使用仓库.local忽略目录或显式外部文件/secret 注入，不得将集成无鉴权设置用于生产；生产部署仍须完成业务、安全、备份恢复等验收，未经用户明确授权不部署。

## 可观测性变更约定

T004/REQ-018、020：日志采用独立Docker监控项目中的Alloy/Loki/Grafana，指标采用Prometheus及显式启用的exporter，固定版本/digest。日志索引仅用environment/service/host，禁止把请求ID、消息ID、用户身份作为索引标签；指标不得采SQL正文、业务键值或个人信息。Docker API只读代理与Alloy可读取inspect元数据，属于高信任宿主组件，项目过滤不等于租户隔离。技术日志不代替D05删除日志或业务审计。生产监控显式提供持久状态密钥、secret及保留期，不复用开发账号/卷；Linux宿主指标不能用macOS Docker VM结果冒充。采集、告警、中间件指标和Broker变更按影响临时验证，记录真实依赖及实际结果；专项验收脚本不再留存。真实通知未配置时只验收隔离合成Webhook，不宣称通知闭环或生产验收完成；每个模块仍须在 docs 工程新增阶段记录，按需同步 README，并完成规定质量检查。

## 启停入口维护

T004/REQ-018、020：完整Docker运维入口为scripts/init-dev.sh、start.sh、stop.sh，共用services.py；宿主入口scripts/dev.py共用其项目归属和字面环境配置；普通启动不得隐式迁移或重置账号，生产不得运行开发API/worker/探针。凭据默认在仓库.local忽略目录显式初始化，旧卷缺原口令时拒绝生成；环境文件不执行shell。停止按项目标签及依赖顺序执行，永远不删除卷，不使用force/prune或操作其他项目。启停变更运行`python3 -m unittest discover -s scripts -p 'test_*.py' -v`，按影响临时验证原生模式、宿主进程及Docker生命周期；真实中间件往返使用`make integration`。专项验收脚本按用户授权删除，CI保留全量Go检查、入口单测和Make集成往返；不在日常启动隐式执行验收。更新README及当前运维文档，报告未验证边界。

## 配置和目录

- 公共模板与必要覆盖项放configs/app、middleware、observability、examples；Compose统一放deploy/compose，共用MQ定义避免复制。
- 开发/运维入口与共享模块在scripts，离线单测在scripts/tests；不保留独立专项验收脚本或过时指纹检查副本。
- 实际凭据、密钥、对象和本机状态统一放.local/<模式>，Git与Docker构建上下文均忽略；禁止放入普通源码目录。兼容显式外部路径，旧配置导入只复制且不覆盖，运行中的旧应用先停止。不得因切换目录生成替代历史密钥或口令。
- 普通停止始终保留卷；只有用户明确授权的独立清理才可删除无引用资源，先检查所有容器（含停止容器）引用，不使用force/prune。
