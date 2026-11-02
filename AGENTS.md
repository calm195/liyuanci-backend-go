# 后端协作规则

## 工程与代码规范

- 模块为 github.com/calm195/liyuanci-backend-go，采用已批准方案 B；PostgreSQL 是业务权威。API 与 worker 分别构建，基础模式只提供健康端点；生产真实服务必须显式配置，无模板用户示例或业务 Mock 路由。
- cmd下8个命令只负责参数、信号和退出状态；internal/app按文件组织API、worker、初始化与显式迁移的依赖装配和生命周期，业务包不得反向导入app。配置加载放 internal/config，仅依赖yamlconfig和securedata等叶子能力，禁止导入业务服务；领域配置和资源投影由app显式转换，YAML读取/合并统一通过internal/yamlconfig中的独立Viper实例，禁止业务代码直接调用go-yaml或绑定环境变量；日志与中间件客户端放 internal/platform，Gin 处理器/中间件放 internal/httpapi；领域按模块置于 internal；正式HTTPS供应商在 internal/adapters/gateway，本地运行适配在 internal/adapters/local，不依赖 config 或应用装配包。配置示例在 configs，专用 SQL 在 migrations 及 internal/migration/sql，测试靠近源文件。
- 使用 Go 1.25+；依赖变化后 go mod tidy 并提交 go.mod/go.sum。make run 使用私有开发覆盖，纯健康入口显式选择 configs/common.yaml；make build 构建当前 Makefile 列出的进程。真实中间件往返用 make integration。
- gofmt 使用 Tab；导入按标准库、第三方、本模块分组。多字段配置换行，顶层声明间留空行；包名小写，符号 MixedCaps。I/O 首参为 context.Context，显式注入依赖，避免可变全局客户端。HTTP 校验与状态映射在 handler，业务规则在 service，GORM 查询在 repository 并使用 WithContext；包装错误保留上下文，不向 HTTP 暴露凭据或内部错误。
- 按用户2026-11-02要求，应用与运维配置只读YAML，口令、令牌、密钥和CA内联私有YAML；禁止恢复环境变量覆盖、业务JSON或独立秘密文件入口；不提交密钥、本地覆盖或生产 DSN。按用户2026-11-02简化要求，所有进程只使用 database 的 host/port/name/user/password/sslmode/timezone/connecttimeout 分项字段生成的唯一连接；禁止恢复可配置的dsn字段；应用账号拥有专用业务库，禁止集群管理权限。GORM AutoMigrate 仅在显式迁移入口执行，普通 API/worker 不隐式 DDL；业务权限由会话和岗位角色在领域函数中复核。实体每表独立结构体，按模块分文件；PostgreSQL 专用约束、生成列、RLS、锁函数与权限继续版本 SQL 管理。
- SQL 每表、每字段必须有中文数据库对象注释（COMMENT ON TABLE/COLUMN），含迁移账本与探针；SQL 行注释不能替代。迁移集成检查验证目录注释非空。已应用迁移不改写，通过新增迁移变更；未交付迁移首次验收前可补齐。
- 注释默认中文，覆盖包、导出符号、关键逻辑和测试说明；导出注释以 Go 标识符开头。技术术语、工具指令如 //go:build 及生成代码保留原样；用户明确要求其他语言时遵循用户。
- 非测试代码的标识符、注释、字符串和日志不出现任务/里程碑编号；追踪在测试和 docs 工程。日志消息与人类说明使用中文，字段名、协议值和必要技术标识稳定。关键业务规则、生命周期、资源归属、恢复及非显然决策变动必须同步准确注释。
- 改动的运行操作在所属边界使用注入的 *zap.Logger 记录结构化完成日志，按需含 request ID、耗时、结果或安全标识；成功后才记录成功，失败/恢复仍保留请求完成日志。不记录凭据、DSN、请求体或个人数据。
- 保留 Git 历史及无关改动，使用 Conventional Commits；PR 说明问题、结果、验证、配置及迁移步骤，只有可见 UI 变化才需截图。

## 验证

日常迭代先运行相关包/相关用例，使用 `make check-fast PACKAGES=./internal/模块/...`；仅在需要定位并发问题时增加 race。一次完整交付前统一运行 make fmt、make check；check 须只读校验格式、go vet ./...、go test -race -cover ./...，CI 保持执行。Go 源码或依赖变化交付前再运行 make build；已通过后仅因新增修改、失败或未解决风险重跑相关检查。测试用标准 testing/net/http/httptest，文件 *_test.go、名称 TestBehavior；校验与 HTTP 失败优先表驱动。单测无需中间件，不设覆盖率硬阈值；真实数据库、Redis与持久任务变化须显式集成检查并说明服务依赖。

失败修复并复跑；环境受限报告命令和原因，未运行不得宣称通过。文档变动报告完成与验证，另在 docs 工程运行文档检查与固定指纹检查。运维专项检查继续按下文执行。


## 跨仓库文档与分支规则

- 统一遵循 [docs 文档规范](../docs/governance/README.md)；本端入口为 [现状](../docs/development/backend/README.md)、[缺陷](../docs/development/backend/defects.md)、[后续计划](../docs/development/backend/plan.md)。
- 按用户2026-10-30最新要求，docs本端目录仅保留上述三份滚动文档，按模块维护；不再新增milestones、validation、阶段补证、截图或原始日志。源码仓库只保留运行/开发必要说明。
- 现状写源码完整SHA、当前能力和实际验证边界；缺陷区分已复现问题、依赖缺口和待验收项；计划写优先级、依赖和完成标准，不虚构负责人或日期。完成任务同步更新这三份文档，不将已解决问题继续列为开放缺陷。
- 本次明确退役的历史材料按docs清单保存在Git快照；既有需求、审批、有效契约及未退役固定输入不改写。AI不签发G1/G2/G3，业务范围和契约语义变化另走适用授权。
- 修改前核对本仓库及docs分支、完整HEAD和工作区，保护已有改动。用户2026-11-02明确新增简化分支，主仓库、后端与docs当前使用codex/simplify-backend；保留develop/main，不删除其他分支。提交合并推送仍按当次授权执行。并行任务协调写入，必要时使用detached工作树并在汇总后清理。
- 已授权提交时先提交源码仓库，再补docs精确版本；提交、合并、推送和部署按当次授权执行，不重复索取已有授权。共享模块映射与跨端计划由docs协调维护。
- 中文文档保留必要代码标识；报告真实/合成/未执行边界。纯文档整理只执行文档工具检查、链接/固定输入和差异检查；业务代码变化继续遵循本仓库完整质量门槛，不将历史测试冒充本轮重测。

## 简化分支运行约定

- 用户2026-11-02要求删除MQ和Docker，配置只保留configs/common.yaml及development.yaml、production.yaml环境覆盖；实际覆盖Git忽略，公共example保留中文字段注释。禁止恢复环境变量、JSON配置、分角色DSN或独立秘密文件入口。
- PostgreSQL保存持久任务，Redis提供共享限流；学生卡、内容、树洞/胶囊、账号清理及证据清理均按现有租约、重试、幂等和取消语义执行。角色区别由业务账号会话及领域函数检查，不能以“共用连接”跳过授权。
- 日常入口为scripts/manage.py和Makefile。普通运行不创建数据库、不迁移、不初始化账号，不自动启动/关闭已有PG或Redis。make integration使用本轮临时原生实例，退出后删除本轮测试数据。
- 删除意图使用独立加密文件账本；保持原密钥和历史意图、跨进程锁、原子替换及fsync，不把同机文件称作独立故障域。恢复数据库前先执行两域recover，失败保持读取/外发门禁。
- 用户本轮已授权删除本项目Docker容器、卷、网络和相关镜像及本地旧配置/备份，不保留本次备份；这不构成生产服务器部署授权。未来资源清理仍须有明确归属和当次授权，不使用全局force/prune。
- 启动变更运行Python入口单测、Go规定门槛和受影响的真实原生集成。目标Linux服务管理器、真实供应商、设备与容量未执行时明确说明，不沿用历史验收结论。
- 应用密钥仅由私有YAML顶层masterkey管理，通过HKDF-SHA256按环境/用途派生，派生字段使用json:"-"，禁止恢复旧用途密钥配置或共用原始主密钥。派生版本、环境与用途标签是持久协议，不得随重构改变。外部服务凭据与KMS引用仍独立配置。用户本次明确允许清空旧开发数据后切换；这不授权清空生产数据。主密钥和当前状态仅在Git忽略的私有环境覆盖及.local/data；初始化不覆盖已有私有文件。对旧生产库的对象所有权和历史日志转换须显式执行，不在普通启动中迁移或清空。
