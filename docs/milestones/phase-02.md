# 第二阶段：数据与安全基础

2026-10-21 分功能整理提交；T005，REQ-002、003、005、007、018、020。已批准设计保持只读，不推送或部署。当前记录仅描述本提交已包含的范围，后续功能按依赖继续提交。

## SQL 注释规范

按用户要求，AGENTS.md 强制 PostgreSQL 表与全部字段使用中文 COMMENT ON 数据库注释；补齐隔离探针的表和字段注释，不改变投递行为或初始化账号。不在此提交添加业务迁移或密钥适配。D02/D03/D05 真实资源与资料尚未交付。

提交快照验证：make fmt、make check（只读格式/vet/race/cover）、make build、git diff --cached --check 通过；不把源码注释当数据库注释，不改写已应用业务迁移。

## 信封与有界对象加密

internal/securedata 通过显式 KeyService/zap 依赖提供 AES-256-GCM 信封、用途/对象/版本 AAD、独立检索密钥 HMAC 原语及有界对象接口。逐次随机数据密钥/nonce；失钥不降级明文。对象先完整认证再输出，无明文临时文件；正字节上限由调用方给定，不定义学生卡产品限制。输入输出生命周期仍由调用方负责，底层读取需自行设置超时。

合成测试覆盖往返、篡改/跨对象替换、历史引用、失钥、超限/取消/短写/空转输入和完成日志；无真实 KMS/桶、无默认主密钥。提交快照 make fmt/check/build 与差异校验通过。对象/字段库尚未接入 API，默认仍仅健康端点。

## 显式迁移、业务结构与恢复日志

internal/migration 内嵌五个版本，覆盖账号/身份结构、审核/后台、心愿/申请/匹配/聊天/故事、持久工作/普通清理与隔离 evidence schema。事务锁串行化版本前缀/哈希核验，非空表或未知依赖拒绝整批回退；全部表/字段有中文数据库注释。当前是数据结构，不是业务事务/认证/审核/删除流程。

cmd/migrate 提供 up/down/permissions 及独立 APP_RECOVERY_DATABASE_DSN 的 journal 操作，拒绝业务与恢复同库。权限组 NOLOGIN，不初始化真实账号；探针/领域/后台/隐私/证据角色分离，sessions RLS 隔离学生与后台，未知策略/运行组所有权异常拒绝。所有权、schema 权及最小表权限显式维护，日志仅结果/耗时，不输出 DSN。

Makefile/Dockerfile 构建迁移二进制；scripts/check-migrations.py 随机专用 Docker 库验证迁移、业务/证据/恢复表、注释、并发唯一、回滚计数器、实际 SET ROLE/RLS 正反权限和误授权撤销。--container 构建正式 Go1.25 镜像/非 root CLI，验证重复操作、回退、混建拒绝及日志脱敏；当期 builder 只测试已提交 securedata。提交快照 make fmt/check/build、--container 和差异校验通过。恢复库只验证同容器不同逻辑库，不能冒称独立物理持久或恢复演练；D02/D03/D05 与实际登录身份未交付。
