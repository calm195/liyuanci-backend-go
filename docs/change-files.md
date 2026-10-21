# T001–T004 实施新增文件（含T004续验）

原有.gitignore及Git历史未修改；bin为本仓库构建制品且被忽略。批准材料、共享契约版本0.1.0及另两个业务仓库未修改。T004续验具体修改见t004-followup.md。

本仓库新增文件：

- `.dockerignore`
- `.github/workflows/ci.yml`
- `AGENTS.md`
- `Dockerfile`
- `Makefile`
- `README.md`
- `cmd/probe/main.go`
- `cmd/server/main.go`
- `cmd/worker/main.go`
- `compose.integration.yaml`
- `configs/broker.conf`
- `configs/config.yaml`
- `configs/integration.yaml`
- `docs/change-files.md`
- `docs/environment.md`
- `docs/foundation-manifest.json`
- `docs/implementation.md`
- `docs/input-verification.json`
- `docs/t004-followup.md`
- `docs/t004-integration-evidence.json`
- `docs/validation.md`
- `go.mod`
- `go.sum`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/httpapi/router.go`
- `internal/httpapi/router_test.go`
- `internal/platform/logger.go`
- `internal/platform/logger_test.go`
- `internal/platform/nameservers.go`
- `internal/platform/nameservers_test.go`
- `internal/platform/resources.go`
- `internal/platform/resources_test.go`
- `internal/worker/worker.go`
- `internal/worker/worker_test.go`
- `migrations/README.md`
- `migrations/integration/0001_probe.up.sql`
- `scripts/check-api.py`
- `scripts/check-connections.sh`
- `scripts/check-container-api.py`
- `scripts/integration.sh`
- `scripts/verify-inputs.py`

共享目录首批新增（本次续验未改）：

- `../docs/contracts/README.md`
- `../docs/contracts/acceptance.md`
- `../docs/contracts/build.py`
- `../docs/contracts/errors.md`
- `../docs/contracts/examples.v0.1.0.json`
- `../docs/contracts/openapi.v0.1.0.json`
- `../docs/contracts/operations.md`
- `../docs/contracts/scenarios.json`
- `../docs/contracts/semantics.md`
- `../docs/contracts/validate.py`

本次版本决定新增：`docs/middleware-decision.md`；同步README及环境/实施/验证/续验文档和foundation-manifest.json，未改运行配置（已固定5.3.2）。
