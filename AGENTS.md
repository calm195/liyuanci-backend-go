# Repository Guidelines

## Project Structure & Module Organization

This module is `github.com/calm195/liyuanci-backend-go`. `cmd/server` assembles dependencies and manages the HTTP server lifecycle. `internal/config` loads YAML and environment configuration; `internal/platform` owns logging and middleware clients. `internal/httpapi` contains Gin handlers and middleware. `cmd/worker` runs the isolated T004 probe consumer; domain services will be added by later tasks. Keep new features grouped by domain under `internal`. Configuration examples live in `configs`; database-specific SQL lives in `migrations`. Tests sit beside source files.

## Build, Test, and Development Commands

Use Go 1.25 or newer. Run `go mod tidy` after changing dependencies and commit both `go.mod` and `go.sum`. `make run` starts the API without external services by default. `make build` produces `bin/server`, `bin/worker`, and `bin/probe`. `make fmt` applies gofmt; `make check` runs vet, race-enabled tests, coverage reporting, and formatting checks. `sh scripts/integration.sh` starts the isolated middleware verification described in README.md.

## Coding Style & Naming Conventions

Use gofmt with Tab indentation. Group imports as standard library, third-party packages, then this module, separated by blank lines. Expand multi-field configuration literals across lines and separate top-level declarations with blank lines. Use standard Go naming: lowercase package names, MixedCaps identifiers, and descriptive exported API comments. Accept `context.Context` as the first parameter for I/O operations. Pass dependencies explicitly; avoid mutable global clients. Keep HTTP validation and status mapping in handlers, business rules in services, and GORM queries in repositories. Use `WithContext` for database calls. Wrap errors with useful context without exposing credentials or internal errors in HTTP responses.

## Testing Guidelines

Use the standard `testing` package and `net/http/httptest`. Name files `*_test.go` and tests `TestBehavior`; prefer table-driven cases for validation and HTTP failures. Unit tests must run without middleware. Add explicit integration tests when changing real database, Redis, or RocketMQ behavior, and document required services. No minimum coverage percentage is enforced. Run `make check` and `make build` before submitting changes.

## Commit & Pull Request Guidelines

Preserve the existing Git history and unrelated changes. Use Conventional Commits such as `feat: add user lookup` or `fix: close redis client`. PRs should describe the problem, resulting behavior, test commands and outcomes, linked issues, and any configuration or schema migration steps. Include screenshots only for visible UI changes.

## Configuration & Agent Instructions

Environment variables use the `APP_` prefix. Never commit secrets, local overrides, or production DSNs. Apply explicit database-specific migrations; do not add startup AutoMigrate. Codex should read this guide before editing, preserve unrelated changes, keep scope focused, and report checks that could not run. Never claim tests passed without execution.

## 功能完成后的 README 要求

每个功能的完整修改完成后，必须同步更新本仓库 README.md，说明已实现的功能、实际行为及设计（包括关键流程、模块职责、依赖与必要的设计取舍），并引用对应任务 ID 与 REQ。功能变更包含新增、修复、重构及配置调整；README 内容必须与最终实现一致，明确实现范围、未完成部分和真实/模拟状态，并记录相关验证命令与实际结果。详细设计可链接至本仓库文档或共享契约，但 README 必须保留功能与设计说明，不能仅提供链接。README 更新是功能完成及提交前的必要条件；不得将尚未实现或未经验证的能力描述为已完成。

## Required Checks After Every Change

After every modification, run `make fmt` and `make check` before declaring the work complete. `make check` must verify formatting without modifying files, run `go vet ./...`, and run `go test -race -cover ./...`. Also run `make build` after Go code or dependency changes. Fix all reported issues and rerun affected checks. If environment restrictions prevent a check, report the command and reason explicitly; do not skip it silently or claim it passed. Keep this workflow enforced in CI.

## Required Comments & Completion Logging

注释默认使用中文，包括包注释、导出符号文档、关键逻辑和测试说明。导出符号注释仍以对应的 Go 标识符开头；保留必要的技术术语及工具指令（如 `//go:build`），不修改生成代码中的注释。用户明确要求其他语言时遵循用户要求。

Every change must add or update comments at the affected critical points: business rules, lifecycle ordering, resource ownership, recovery, and non-obvious decisions. Explain why the code exists and keep comments accurate. Every changed runtime operation must emit a structured completion log using injected `*zap.Logger` (`go.uber.org/zap`), with request ID, duration, result, or safe identifiers as applicable. Log success only after the operation succeeds; record failures at the owning boundary. Never log credentials, DSNs, request bodies, or personal data. Preserve request completion logs on failure and recovery paths. Documentation-only changes must report completion and validation in the final response. Update relevant log assertions and run the required checks after every change.

## 鲤源池实施边界

用户已授权本仓库实施。T004/REQ-001、018、020：模块为 github.com/calm195/liyuanci-backend-go，固定方案B；只用PostgreSQL作为业务权威。API与独立worker分别构建。默认仅健康端点，无模板用户示例、无业务Mock路由；生产必须显式配置真实中间件，禁止AutoMigrate。所有新增实现与测试须引用任务ID和REQ。docs工作台审批/需求/设计只读，共享新增契约限docs/contracts。不得自动提交、推送、部署。

## 中间件部署约定

T003/T004/REQ-018、020：开发集成及未来生产的 PostgreSQL、Redis、RocketMQ（NameServer/Broker）统一使用 Docker 部署，保持环境、网络、数据卷与凭据隔离。配套管理工具使用 pgAdmin、Redis Insight、Apache RocketMQ Dashboard 官方发布镜像，固定版本与 digest；管理端口仅绑定 loopback，远程经 SSH 隧道访问。生产配置独立于集成探针，凭据使用仓库外文件/secret 注入，不得将集成无鉴权设置用于生产；生产部署仍须完成业务、安全、备份恢复等验收，未经用户明确授权不部署。
