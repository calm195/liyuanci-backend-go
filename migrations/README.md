# T004 / REQ-020 显式迁移

`integration/0001_probe.up.sql` 仅用于隔离中间件探针。通过 Compose 的 `migrate` 显式执行，API/worker 启动均不迁移。
该表不是 T005 领域模型。T005 将单独引入版本迁移工具、独立迁移账号和业务DDL；不得把探针回执当成事务发件箱或可靠消费验收。
