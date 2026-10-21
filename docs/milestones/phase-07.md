# 第七阶段：人工认证申请、重提与后台审核

2026-10-21，T011/REQ-002、015、018、019，关联T005/T006/REQ-020。承接第六阶段学生卡上传和扫描，继续codex/student-card-verification，docs使用codex/student-card-contracts。用户明确授权按功能模块提交、沿用可注入适配器与隔离测试、同步共享API文档、不考虑联调。本阶段不修改共享审批/需求/设计，不推送或部署。

## 身份事务及数据库基础模块

command增加IdentityReview资源和对应expected_version白名单，资源顺序为账号之后资产→认证申请→原互动资源；IdentityLock显式声明在任何主体锁之前获取身份全局事务锁，与微信身份解析及双代检索轮换保持同序。identity.Store复用原Bind实现，新增现有根事务内BindStudentNumber，检查事务和身份锁是否真实持有，不在审核中另开独立绑定事务；日志标为pending_commit，根命令日志仅在提交后记录success。唯一占用仍为当前/前一代HMAC，已绑定其他学号不能任意改号。

新增0011迁移，IdentityReview实体新增data_cipher和revision_suggestion；待审材料使用申请ID/版本AAD加密，历史空材料不伪造迁移为可审核。拒绝状态且存在材料时必须有非空建议，既有reason约束继续适用；新增字段中文数据库注释。回退先锁表，存在材料/建议则拒绝丢失数据回退。前十版迁移保持不变，常规字段仍由显式AutoMigrate维护，专用CHECK/注释/回退通过SQL维护，普通API/worker不执行DDL。

权限按学生identity_maintenance/后台admin_api两池隔离；学生可提交审核、改当前verification；后台只补学号检索读取及加密身份/姓名的必要插入权限。资产行锁要求的UPDATE仅授予version列，不授予修改对象键/扫描结果。完整ApplyPermissions可重复应用，恢复及生产身份边界仍遵循既有说明。

该基础模块已执行make fmt、make check、make build；真实check-migrations.py --container十一版迁移、中文注释/权限、空库上下行和UID10001正式CLI通过，首个本轮制品liyuanci-migrate-check:63618ab267ec。随后增加材料驳回CHECK和回退表锁的最终复验另记。基础提交不将待后续模块提交的HTTP能力宣称已交付。

基础模块最终复验：make fmt/check/build通过；check-migrations.py --container退出0，制品liyuanci-migrate-check:27fba097ca79，包含最终材料驳回CHECK和回退表锁。真实隔离项目liyuanci-admin-check-060ed39f53ae通过，已包含双代唯一占用、并发决定、审计失败回滚等后续业务模块在工作区的测试，人工认证1.30s/82.5%；不将未提交的业务模块当作本基础提交的公共API能力。实际供应商仍为合成适配器，生产、跨端和MQ领域联调未执行。

## 人工认证业务与HTTP模块

verification.service实现待审材料验证/版本加密、首次申请和驳回重提、后台版本决定；repository承担GORM和持锁授权；read承担最小本人状态、脱敏队列签名分页及详情/原卡内存所有权；httpapi显式注册六路由，严格JSON、可选版本非null、Bearer与幂等头、错误状态、no-store和PNG响应。学生/后台分实例使用最小权限池；学校Rules、私有Objects.Open、Codec、事务内Binder、独立回执密钥和资源上限全部注入，无默认供应商或学生Mock路由。

首次申请仅接收本人扫描approved且active学生卡；按REQ-002注册即绑定学号，在同事务写学号信封及双代唯一占用，其他有效账号不能重复注册。姓名/学号另以申请ID/版本加密保存待审快照。已有记录仅rejected可重提，必须提供本人GET返回的申请版本，缺400、旧409，不能以Me或资产版本代替。重提沿用随机申请ID并增版本、替换材料、清空意见和处理人；更正学号时查重、新旧占用及身份密文替换同事务提交，失败保留旧占用，已通过身份不得改号。pending/approved用新键重复提交冲突；同键相同材料重新授权后返回当前最小结果，不重复写入，异材料409。回执仅存用途隔离HMAC摘要及结果引用，普通Me/队列不包含完整身份或学生卡引用。

后台当前review_operator/super_admin可访问案件，crisis_operator单独持有不能审核；无案件指派扩展。列表限active账号pending，UUID升序，limit默认20最大50，next_cursor末页为null；游标签名绑定账号及固定队列，不提供历史快照承诺。详情/原卡/决定成功均在数据库访问留迹落库后返回，审计故障失败关闭。失败、撤权及panic由安全中文完成日志记录，不冒充denied/failed业务审计条目。

决定必须pending且期望版本一致；驳回原因和建议均非空白，状态、账号rejected、处理人/时间及审计原子提交。通过时同一根事务复核注册学号绑定、写加密姓名、账号verified及审计；绑定不一致使整个决定回滚，不能留下部分认证。审核状态版本递增后同步重封装待审密文AAD，避免新版本无法解密。已通过认证的身份不经本入口改号。学校规则在当前会话初步准入后调用，写事务重新复核当前生命周期/会话/处罚；供应商故障不明文降级。

原卡先完成当前权限/案件准入，释放数据库锁后以随机私有key与精确对象版本读取、有界验证整图密文，随后再次同序锁定并复核角色/会话、账号生命周期、申请/资产版本，提交留迹后返回image/png。读取期间注销/重提/撤权不输出旧图，明文仅在内存、HTTP写出后清零，不返回下载URL。外部桶/扫描仍由既有学生卡模块负责，不在人工认证中重扫或伪造供应商。

Studentauth.VerificationRequests为默认false装配开关，只有显式注册认证路由后才开启；未认证/驳回且当前允许交互时提示postMeVerificationRequests，pending/verified不提示。通过后同一学生会话的Me立即反映verified；没有因此开放尚未装配的心愿、聊天等路由。README保持四部分，仅同步能力、目录及十一版迁移入口。

## 本端验证及交付边界

make fmt、make check（只读格式、vet、race-cover）、make build已执行通过。普通未配置中间件测试会明确skip，不能作为真实PG验证；真实check-adminauth.py最终主体功能项目liyuanci-admin-check-060ed39f53ae退出0：后台13.66s/82.0%、学生1.19s/84.1%、学生卡1.26s/76.3%、人工认证1.30s/82.5%。涵盖非本人/未扫描资产、首次/同键/异材料、缺版本/旧版本重提、拒绝和重提意见清理、旧审核不覆盖新材料、同版本并发仅一次决定、唯一学号冲突原子回滚、双代绑定并完成轮换、Me当前动作/资格、审计失败材料读取与决定回滚、角色撤销后回执拒绝、原卡外部读取中注销、签名分页和本端HTTP/no-store/日志脱敏。仅新增显式null HTTP用例与错误映射依赖清理的最后复验另记。

check-migrations.py --container最终退出0，制品liyuanci-migrate-check:27fba097ca79；十一版/中文表列注释/重复permissions/空库up-down-up/恢复库混建拒绝/正式Go1.25.0及UID10001迁移CLI通过。服务脚本13项单元通过。没有依赖变更，无需go mod tidy；未变更启停/监控/Broker，不运行无关真实服务、观测专项或MQ往返。本轮最初Go缓存被沙箱拒绝，按所需权限重跑通过；早期真实测试发现资源行锁列权限、期望版本白名单缺口，修正后通过，不隐瞒初次失败。

共享contracts同步六接口implemented-injected、原卡image/png、版本/状态/错误和外部边界，0.2.0静态索引revision5：6后台opt-in、12身份/学生卡/人工认证注入操作、65未实现；r1–r4指纹保留历史，不激活moderation-extension草案。使用现有/private/tmp/liyuanci-contract-validator独立环境验证build.py/validate.py：83操作/130schema/783请求响应示例及动作正反例通过，新增驳回缺建议/空建议、显式null版本及列表无敏感字段边界；系统python起初缺依赖，旧Binary断言初次失败已修正，既有LibreSSL/RefResolver警告退出0。

本阶段完成人工认证申请、重提、后台审核及访问留迹的可注入模块范围；真实KMS、学校规则、私有对象读取执行身份/供应商、学生API/领域worker进程装配、生产资源/保留和物理清理仍缺。T011不能据此宣称真实外部/部署整体验收，T007领域MQ闭环与T008实际学生启动装配也未被本轮关闭。没有跨端联调、推送或部署，共享台账由文档协调会话维护。完整接入说明见本仓verification.md及共享contracts/verification.v0.2.0.md，本阶段保留上述必要实现细节。

## 注册学号绑定的最终复核与结果

最终复核REQ-002原文“注册绑定学号”后，将学号占用前移至申请事务，未采用早期工作区的仅通过时绑定方案。identity.ReserveStudentNumber在身份全局锁→账号guard之下复用首次/同值绑定，对驳回更正先核验新值双代占用，再原子更新递增内容版本的身份密文并替换新旧检索占用；任何失败保留原学号。申请、材料、账号pending、占用和命令回执同提交，审核通过再核验注册绑定。未通过修正与已通过禁止改号分开，注销释放仍依赖后续隐私模块，不宣称已实现完整注销链路。

修正后的真实最终项目liyuanci-admin-check-69a0755863fd退出0：后台13.60s/82.0%、学生1.30s/84.1%、学生卡1.32s/76.3%、人工认证1.31s/82.6%。包含不同账号并发注册相同学号仅一个申请提交、双代改号及旧值释放后另一账号可绑定、材料被错误存储操作改成别人的学号时通过失败回滚，以及上节全部原卡/审计/重提/动作/HTTP边界。更早3135b651cc12项目1.32s/82.5%包含最后显式null用例；其后注册修正以上述69a0755863fd为最终证据。make fmt/check/build再次通过。迁移未再变更，27fba097ca79是最终十一版DDL/权限制品快照，后续注册业务代码以本地最终Go构建和此真实测试为准，不冒称旧镜像包含后续源码。
