# 数据库迁移

当前简化分支的迁移、API、初始化与worker共用环境YAML中的唯一一组database分项字段生成的连接。账号由管理员预先创建并拥有专用业务库；禁止超级用户、BYPASSRLS、建库/建角色及复制权限。业务岗位在会话与领域函数检查。

```sh
bin/migrate -config configs/development.yaml up
bin/migrate -config configs/development.yaml permissions
bin/migrate -config configs/development.yaml opaque-blocks
```

up仅在显式命令中执行版本SQL、实体同步及application_permissions.sql，不在普通启动中执行。permissions重放单应用账号的表策略与授权锁函数。历史0001～0029文件和校验和不改写；旧bootstrap_roles.sql及日志库迁移仅用于历史数据库权限回归，新启动入口不调用。

已有多角色数据库需先由管理员显式调整本库对象所有权并撤销不用的LOGIN；程序不隐式提权或删除旧账号。删除恢复现在使用加密文件账本，已有日志库意图必须先迁移核对，不能静默换成空账本。完整用法见[配置说明](../CONFIGURATION.md)。
