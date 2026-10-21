# 鲤源池 Go 后端

## 1. 项目介绍

鲤源池后端采用 Go 模块化单体 API 与独立 worker，模块为 `github.com/calm195/liyuanci-backend-go`。PostgreSQL 是业务权威，Redis 与 RocketMQ 提供配套能力，事务发件箱组件供后续领域服务接入。

当前已提供工程基础、真实中间件集成探针、开发启停、管理工具和日志/指标设施，以及数据库事务与当前授权命令库、持久工作同事务写入、发件箱租约转发、数据库幂等消费、到期补偿和受控重驱动组件、独立业务结构/恢复日志迁移工具、身份唯一占用、受控双代轮换和待密钥供应商接入的信封及有界对象加密组件。API 仅提供健康端点；认证、实际领域处理与业务接口尚未实现，无业务 Mock 路由，当前不能作为完整生产服务。

阶段实施、任务 ID/REQ、设计取舍及历史验收记录统一存放于 [docs/milestones](docs/milestones/README.md)。共享接口定义见[契约入口](../docs/contracts/README.md)，契约定义不代表接口已实现。

## 2. 项目选项与目录结构

| 选项 | 用途 |
| --- | --- |
| 宿主运行 | Go 1.25+，无需中间件，仅启动健康端点 |
| Docker 开发环境 | Docker Engine、Compose 与 Python 3.9+；真实 PostgreSQL、Redis、RocketMQ，以及 API/探针 worker |
| 管理与监控 | 开发脚本默认启用管理页面及独立日志、告警、指标项目；可显式关闭 |
| 扩展指标 | 中间件指标需监控身份与 secret；宿主指标仅支持 Linux，均须显式启用 |
| 生产配置 | 仅中间件/监控骨架，独立项目、卷与凭据；尚无完整业务发布入口 |

```text
cmd/
  server/             API 依赖装配与生命周期
  worker/             独立集成探针消费者
  probe/              集成探针生产与回执检查
  migrate/            显式版本迁移命令
internal/
  command/            短事务、排序锁、幂等回执及当前会话/权限复核
  adminauth/          显式启用的后台账号、口令及数据库会话
  durable/            持久工作、转发/消费、数据库补偿和受控重驱动
  transportmq/        显式装配的最小 RocketMQ 运输适配
  identity/           加密身份存储、唯一占用及受控轮换
  migration/          内嵌迁移 SQL、事务锁、版本校验及显式权限配置
  securedata/         信封加密及域隔离 HMAC 基础
  config/             YAML 与环境配置
  httpapi/            Gin 处理器与中间件
  platform/           日志及中间件客户端
  worker/             探针消费逻辑
configs/              配置示例与监控配置
migrations/           集成探针 SQL（业务版本 SQL 位于 internal/migration/sql）
scripts/              初始化、启停及隔离验收脚本
docs/
  milestones/         按阶段分文件的实施与验收记录
  operations.md       启停参数与故障排查
  docker-middleware.md 中间件与管理工具操作
  observability.md    日志、指标及告警操作
compose.*.yaml        按用途拆分的 Docker 编排
```

应用配置使用 `APP_` 前缀；脚本及 Compose 参数使用 `LIYUANCI_` 等专用变量，详见配置示例与[启停说明](docs/operations.md)。构建默认使用 `https://goproxy.cn,direct`，可用 `LIYUANCI_BUILD_GOPROXY` 覆盖，保留 go.sum/GOSUMDB 校验。

## 3. 项目启动方式

在仓库根目录执行。仅需宿主健康端点时：

```sh
make run
```

默认监听 `127.0.0.1:8080`；`GET /healthz` 返回 200，`GET /readyz` 当前返回 503（认证及密钥能力尚未实现），其他路径返回 404。

完整 Docker 开发环境：

```sh
# 首次初始化仓库外开发凭据；不覆盖已有口令。
./scripts/init-dev.sh
# 首次显式创建探针表、主题并验证往返。
./scripts/start.sh dev --bootstrap-probe --probe
# 日常启动不执行迁移或重置账号。
./scripts/start.sh dev
# 停止并保留数据卷。
./scripts/stop.sh dev
```

旧卷缺失原口令时初始化会拒绝生成，应恢复原凭据。默认配置文件位于 `~/.config/liyuanci/dev/operations.env`，按字面读取，不执行 shell。详细参数、手工操作及生产限制见[启停说明](docs/operations.md)与[中间件说明](docs/docker-middleware.md)。

| 入口 | 默认地址 | 说明 |
| --- | --- | --- |
| Docker API | http://127.0.0.1:18080/healthz | 存活 200，就绪端点仍为 503 |
| pgAdmin | http://127.0.0.1:15050 | 默认邮箱 `developer@example.com` |
| Redis Insight | http://127.0.0.1:15540 | 容器内连接 `redis:6379` |
| RocketMQ Dashboard | http://127.0.0.1:18081 | 发现地址 `namesrv:9876` |
| Grafana | http://127.0.0.1:13000 | 默认账号 `operator`，查看日志、指标及告警 |

pgAdmin 与 Grafana 口令分别保存在仓库外的 `~/.config/liyuanci/dev/pgadmin-password` 和 `~/.config/liyuanci/observability-dev/grafana-password`，自定义配置时以实际路径为准。pgAdmin 连接 PostgreSQL 使用 `postgres:5432`，数据库与用户名均为 `liyuanci`，口令来自开发目录的 `postgres-password`。已有卷不会因修改初始化文件而重置账号。

管理端口仅绑定 loopback，远程通过 SSH 隧道访问。开发集成 Redis 无密码、Broker 无 ACL，不得用于生产。监控扩展与查询见[监控说明](docs/observability.md)；真实通知和生产验收尚未完成。上述地址为配置默认值，不代表服务当前在线。

## 4. 开发规范与须知

领域服务可复用[命令框架](docs/command-framework.md)，需显式声明锁集合、权限与版本字段；当前未接入业务路由，外部副作用须留待持久工作链路。

`durable.Append` 在现有事务内写工作/事件，`Relay.Once` 提交租约后才发送，需显式注入 Sender；`Consumer.Consume` 从数据库持久源复核，将业务回调、终态和回执同事务提交；`Compensate`/`RunCompensator` 提供显式到期补偿，`RedriveCommand` 在后台命令中重新授权并替代旧唤醒；需先显式执行七版本迁移与 permissions，普通启动不运行补偿。`transportmq` 提供固定通道发送、订阅与 ACK/NACK、停止接收/等待在途后关闭；由进程在启动任何 MQ 客户端前显式配置 SDK 脱敏日志。业务 worker 尚未装配领域消费者，使用边界见[阶段说明](docs/milestones/phase-04.md)。

账号/身份迁移须使用外部注入的 `APP_DATABASE_DSN` 显式执行 `bin/migrate up`；专用数据库管理员另可显式执行 `bin/migrate permissions` 配置无登录权限组。`bin/migrate down` 逆序回退全部版本，账号或身份配置非空时拒绝。普通启动不执行迁移或权限配置；已覆盖业务表及隔离证据域结构；恢复日志须用独立 `APP_RECOVERY_DATABASE_DSN` 执行 `journal-up` 和 `journal-permissions`，拒绝与业务表混建。密钥组件无默认密钥或生产适配器，操作和限制见[迁移与加密说明](docs/data-foundation.md)。

- 使用 Go 1.25+、gofmt 与标准 Go 命名；测试与源码相邻，单元测试无需中间件。依赖变化后执行 `go mod tidy`，同步维护 `go.mod`、`go.sum`。
- 按领域组织功能；HTTP 层负责校验及状态映射，service 负责业务规则，repository 负责 GORM 查询。I/O 首参使用 `context.Context`，数据库调用使用 `WithContext`，依赖显式注入。
- 注释默认中文，导出符号注释以标识符开头；关键业务、资源归属、生命周期和恢复逻辑说明原因。非测试代码不含任务编号，运行日志使用中文消息与注入的 `*zap.Logger`，成功后记录完成，失败在所属边界记录。
- 不记录凭据、DSN、请求正文或个人信息；日志索引仅使用 environment/service/host。技术日志不能替代业务审计或删除日志。
- 数据库迁移显式执行，禁止启动 AutoMigrate；开发与生产隔离环境、网络、数据卷及凭据。停止不得删除卷、force/prune 或操作其他项目。
- SQL 必须为每张表和每个字段提供中文数据库注释，使用 `COMMENT ON TABLE/COLUMN`；迁移集成检查验证注释完整性。
- 每次修改完成后运行下列格式与质量检查；Go 代码或依赖变化另运行构建。CI 保持相同检查，未执行的验证必须明确说明。

```sh
make fmt
make check  # 只读格式检查、go vet、race 测试与覆盖率
make build # Go 代码或依赖变更时必需；输出 server、worker、probe、migrate
```

业务迁移验证可执行 `python3 scripts/check-migrations.py --container`，包含正式镜像及非 root CLI 验证；外部资源交付见[依赖清单](docs/dependency-deliverables.md)。真实中间件往返独立执行 `sh scripts/integration.sh`；启停、监控及中间件指标变更的专项检查见 [AGENTS.md](AGENTS.md)，具体历史结果按阶段记录。

功能完成时在上述四部分更新必要的项目说明、选项或用法；详细功能设计、任务 ID/REQ、范围、未完成项、真实/模拟状态及验证命令和实际结果写入 `docs/milestones/` 对应阶段文件，不在 README 累积里程碑记录。阶段文档不得把未实现或未经验证的能力描述为完成。

保留既有历史和无关修改，遵循 Conventional Commits；PR 说明问题、行为、验证及配置/迁移步骤。工作台审批、需求和设计只读，共享新增契约限共享 `docs/contracts`。未经明确授权不自动提交、推送或部署。完整协作要求见 [AGENTS.md](AGENTS.md)。

真实运输适配独立检查：`python3 scripts/check-mq.py`。在随机 Docker 项目中使用固定镜像运行 Broker、NameServer 和合成处理器，验证发送/重投/重复/晚到与退出，保留卷，不代表领域消费或整体需求验收；不连接生产服务。

后台身份入口默认关闭；显式配置独立数据库、仓库外凭据文件、会话/幂等期限及共享限流后启用账号创建、登录、改密与退出。先显式执行七版本迁移/权限，首个超管仅通过 `bin/admin-init` 线下初始化；普通启动不创建或重置账号。配置、接口及未完成范围见[后台操作说明](docs/admin-auth.md)，真实隔离验证运行 `python3 scripts/check-adminauth.py`。未确定的年级字典和生产TTL没有默认值。
