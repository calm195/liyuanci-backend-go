# Repository Guidelines

## Project Structure & Module Organization

This module is `github.com/calm195/liyuanci-backend-go`. `cmd/server` assembles dependencies and manages the HTTP server lifecycle. `internal/config` loads YAML and environment configuration; `internal/platform` owns logging and middleware clients. `internal/httpapi` contains Gin handlers and middleware. `cmd/worker` runs the isolated T004 probe consumer; domain services will be added by later tasks. Keep new features grouped by domain under `internal`. Configuration examples live in `configs`; database-specific SQL lives in `migrations`. Tests sit beside source files.

## Build, Test, and Development Commands

Use Go 1.25 or newer. Run `go mod tidy` after changing dependencies and commit both `go.mod` and `go.sum`. `make run` starts the API without external services by default. `make build` produces `bin/server`, `bin/worker`, and `bin/probe`. `make fmt` applies gofmt; `make check` runs vet, race-enabled tests, coverage reporting, and formatting checks. `sh scripts/integration.sh` starts the isolated middleware verification described in README.md.

## Coding Style & Naming Conventions

SQL 强制为每张表和每个字段编写中文数据库注释（PostgreSQL 使用 `COMMENT ON TABLE` 与 `COMMENT ON COLUMN`），包括迁移账本、集成探针及后续新增字段；SQL 文件中的 `--` 注释不能替代数据库对象注释。迁移集成检查须验证数据库目录中表及字段注释非空。已应用迁移不得改写，注释变更通过新增迁移执行；尚未交付的迁移可在首次验收前补齐。

Use gofmt with Tab indentation. Group imports as standard library, third-party packages, then this module, separated by blank lines. Expand multi-field configuration literals across lines and separate top-level declarations with blank lines. Use standard Go naming: lowercase package names, MixedCaps identifiers, and descriptive exported API comments. Accept `context.Context` as the first parameter for I/O operations. Pass dependencies explicitly; avoid mutable global clients. Keep HTTP validation and status mapping in handlers, business rules in services, and GORM queries in repositories. Use `WithContext` for database calls. Wrap errors with useful context without exposing credentials or internal errors in HTTP responses.

## Testing Guidelines

Use the standard `testing` package and `net/http/httptest`. Name files `*_test.go` and tests `TestBehavior`; prefer table-driven cases for validation and HTTP failures. Unit tests must run without middleware. Add explicit integration tests when changing real database, Redis, or RocketMQ behavior, and document required services. No minimum coverage percentage is enforced. Run `make check` and `make build` before submitting changes.

## Commit & Pull Request Guidelines

Preserve the existing Git history and unrelated changes. Use Conventional Commits such as `feat: add user lookup` or `fix: close redis client`. PRs should describe the problem, resulting behavior, test commands and outcomes, linked issues, and any configuration or schema migration steps. Include screenshots only for visible UI changes.

## Configuration & Agent Instructions

Environment variables use the `APP_` prefix. Never commit secrets, local overrides, or production DSNs. Apply explicit database-specific migrations; do not add startup AutoMigrate. Codex should read this guide before editing, preserve unrelated changes, keep scope focused, and report checks that could not run. Never claim tests passed without execution.

## 项目文档与阶段记录要求

README.md 仅保留四部分：项目介绍、项目选项与目录结构、项目启动方式、开发规范与须知。功能变更涉及项目能力、选项、目录或启动方式时，同步更新对应部分，内容与最终实现一致；不得在 README 累积里程碑、实施流水或验收记录。

每个功能的完整修改完成后，必须在 docs/milestones/ 对应阶段文件中说明已实现功能、实际行为、关键流程、模块职责、依赖与设计取舍，引用任务 ID 与 REQ，并记录实现范围、未完成部分、真实/模拟状态、验证命令与实际结果。新增阶段另建文件并更新阶段索引；详细设计可链接至本仓库文档或共享契约，但阶段文件必须保留必要说明，不能仅提供链接。阶段记录更新是功能完成及提交前的必要条件；不得将尚未实现或未经验证的能力描述为已完成。

## Required Checks After Every Change

After every modification, run `make fmt` and `make check` before declaring the work complete. `make check` must verify formatting without modifying files, run `go vet ./...`, and run `go test -race -cover ./...`. Also run `make build` after Go code or dependency changes. Fix all reported issues and rerun affected checks. If environment restrictions prevent a check, report the command and reason explicitly; do not skip it silently or claim it passed. Keep this workflow enforced in CI.

## Required Comments & Completion Logging

注释默认使用中文，包括包注释、导出符号文档、关键逻辑和测试说明。导出符号注释仍以对应的 Go 标识符开头；保留必要的技术术语及工具指令（如 `//go:build`），不修改生成代码中的注释。用户明确要求其他语言时遵循用户要求。

非测试代码不得出现 `T00x` 等任务或里程碑编号，包括注释、标识符、字符串和日志；注释只描述中文的功能、业务规则及必要的设计原因，不记录实施批次或任务进度。运行日志的消息及面向人的说明使用中文，不包含任务或里程碑编号；结构化字段名、协议值和必要的技术标识保持稳定。任务 ID 与 REQ 的追溯记录放在测试和文档中，不写入非测试代码。

Every change must add or update comments at the affected critical points: business rules, lifecycle ordering, resource ownership, recovery, and non-obvious decisions. Explain why the code exists and keep comments accurate. Every changed runtime operation must emit a structured completion log using injected `*zap.Logger` (`go.uber.org/zap`), with request ID, duration, result, or safe identifiers as applicable. Log success only after the operation succeeds; record failures at the owning boundary. Never log credentials, DSNs, request bodies, or personal data. Preserve request completion logs on failure and recovery paths. Documentation-only changes must report completion and validation in the final response. Update relevant log assertions and run the required checks after every change.

## 鲤源池实施边界

用户已授权本仓库实施。T004/REQ-001、018、020：模块为 github.com/calm195/liyuanci-backend-go，固定方案B；只用PostgreSQL作为业务权威。API与独立worker分别构建。默认仅健康端点，无模板用户示例、无业务Mock路由；生产必须显式配置真实中间件，禁止AutoMigrate。新增实现须在对应文档中引用任务ID和REQ，测试保留任务ID和REQ追溯，非测试代码遵循上述中文注释与日志规范。docs工作台审批/需求/设计只读，共享新增契约限docs/contracts。不得自动提交、推送、部署。

## 中间件部署约定

T003/T004/REQ-018、020：开发集成及未来生产的 PostgreSQL、Redis、RocketMQ（NameServer/Broker）统一使用 Docker 部署，保持环境、网络、数据卷与凭据隔离。配套管理工具使用 pgAdmin、Redis Insight、Apache RocketMQ Dashboard 官方发布镜像，固定版本与 digest；管理端口仅绑定 loopback，远程经 SSH 隧道访问。生产配置独立于集成探针，凭据使用仓库外文件/secret 注入，不得将集成无鉴权设置用于生产；生产部署仍须完成业务、安全、备份恢复等验收，未经用户明确授权不部署。

## 可观测性变更约定

T004/REQ-018、020：日志采用独立Docker监控项目中的Alloy/Loki/Grafana，指标采用Prometheus及显式启用的exporter，固定版本/digest。日志索引仅用environment/service/host，禁止把请求ID、消息ID、用户身份作为索引标签；指标不得采SQL正文、业务键值或个人信息。Docker API只读代理与Alloy可读取inspect元数据，属于高信任宿主组件，项目过滤不等于租户隔离。技术日志不代替D05删除日志或业务审计。生产监控显式提供持久状态密钥、secret及保留期，不复用开发账号/卷；Linux宿主指标不能用macOS Docker VM结果冒充。采集/告警变更运行`python3 scripts/check-observability.py --alerts --metrics`；真实中间件指标变更运行`python3 scripts/check-management.py --middleware-only --metrics`，Broker行为变更还运行原有集成往返。真实通知未配置时只验收隔离合成Webhook，不宣称通知闭环或生产验收完成；每个模块仍须更新对应阶段记录，按需同步 README，并完成规定质量检查。

## 启停入口维护

T004/REQ-018、020：运维入口为scripts/init-dev.sh、start.sh、stop.sh，共用services.py；普通启动不得隐式迁移或重置账号，生产不得运行开发API/worker/探针。凭据仅在仓库外显式初始化，旧卷缺原口令时拒绝生成；环境文件不执行shell。停止按项目标签及依赖顺序执行，永远不删除卷，不使用force/prune或操作其他项目。变更运行`python3 -m unittest discover -s scripts -p test_services.py -v`与真实隔离`python3 scripts/check-services.py`，并更新README和ops文档、报告生产未验证边界；规定Go质量检查保持不变。
