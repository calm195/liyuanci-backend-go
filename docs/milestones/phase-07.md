# 第七阶段：人工认证申请、重提与后台审核

2026-10-21，T011/REQ-002、015、018、019，关联T005/T006/REQ-020。承接第六阶段学生卡上传和扫描，继续codex/student-card-verification，docs使用codex/student-card-contracts。用户明确授权按功能模块提交、沿用可注入适配器与隔离测试、同步共享API文档、不考虑联调。本阶段不修改共享审批/需求/设计，不推送或部署。

## 身份事务及数据库基础模块

command增加IdentityReview资源和对应expected_version白名单，资源顺序为账号之后资产→认证申请→原互动资源；IdentityLock显式声明在任何主体锁之前获取身份全局事务锁，与微信身份解析及双代检索轮换保持同序。identity.Store复用原Bind实现，新增现有根事务内BindStudentNumber，检查事务和身份锁是否真实持有，不在审核中另开独立绑定事务；日志标为pending_commit，根命令日志仅在提交后记录success。唯一占用仍为当前/前一代HMAC，已绑定其他学号不能任意改号。

新增0011迁移，IdentityReview实体新增data_cipher和revision_suggestion；待审材料使用申请ID/版本AAD加密，历史空材料不伪造迁移为可审核。拒绝状态且存在材料时必须有非空建议，既有reason约束继续适用；新增字段中文数据库注释。回退先锁表，存在材料/建议则拒绝丢失数据回退。前十版迁移保持不变，常规字段仍由显式AutoMigrate维护，专用CHECK/注释/回退通过SQL维护，普通API/worker不执行DDL。

权限按学生identity_maintenance/后台admin_api两池隔离；学生可提交审核、改当前verification；后台只补学号检索读取及加密身份/姓名的必要插入权限。资产行锁要求的UPDATE仅授予version列，不授予修改对象键/扫描结果。完整ApplyPermissions可重复应用，恢复及生产身份边界仍遵循既有说明。

该基础模块已执行make fmt、make check、make build；真实check-migrations.py --container十一版迁移、中文注释/权限、空库上下行和UID10001正式CLI通过，首个本轮制品liyuanci-migrate-check:63618ab267ec。随后增加材料驳回CHECK和回退表锁的最终复验另记。基础提交不将待后续模块提交的HTTP能力宣称已交付。

基础模块最终复验：make fmt/check/build通过；check-migrations.py --container退出0，制品liyuanci-migrate-check:27fba097ca79，包含最终材料驳回CHECK和回退表锁。真实隔离项目liyuanci-admin-check-060ed39f53ae通过，已包含双代唯一占用、并发决定、审计失败回滚等后续业务模块在工作区的测试，人工认证1.30s/82.5%；不将未提交的业务模块当作本基础提交的公共API能力。实际供应商仍为合成适配器，生产、跨端和MQ领域联调未执行。
