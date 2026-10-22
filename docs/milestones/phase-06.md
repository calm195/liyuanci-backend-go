# 第六阶段：学生卡上传与扫描

2026-10-21，T011/REQ-002、015、018，关联T007/REQ-020。按用户要求从main 3b75525新开codex/student-card-verification，docs新开codex/student-card-contracts；沿用用户先实现可注入适配器/隔离测试、外部接入缺口保留、不做跨端联调及依赖顺序分模块提交的授权。T005/T006已验收、T008注入式学生身份提供可用数据库会话和命令框架。本阶段仅上传/状态与扫描工作子范围，T011/T007整体未完成。

## 已实现流程、职责与取舍

studentcard.service负责会话计算准入、资源边界、无损图像规范化/加密和命令事务；repository负责实体SQL/当前身份读取；worker负责有界领取→外部调用→锁后提交；HTTP负责严格multipart和状态映射；entity按模块新增暂存实体与资产版本/扫描字段，command新增账号之后、互动资源之前的资产锁类型。普通server/cmd/worker不装配合成服务，也不运行DDL或扫描循环。

两个既有API：POST本人学生卡，202只持久接收；GET本人资产返回最小scan_status，未知/非本人404。PNG/JPEG实际解码，限制字节/像素与解码并发，不落明文临时文件；规范化为无损PNG以清除元数据/尾随载荷且不额外损伤JPEG文字。旧全请求1MiB限制继续适用，实际产品参数/大图能力未交付。ReceiptKey HMAC绑定图片语义，同键重放返回原资产当前状态，不重复创建。身份、处罚和幂等在根事务中重新复核，资产/暂存信封/工作/事件/回执共同提交或回滚。Me只在显式StudentCardAssets装配开关开启时提示上传动作，不把静态索引当运行权限。

图片及随机对象key分用途/AAD加密；数据库暂存信封是对外部调用前崩溃的可靠源，会与私有对象短期重复持密文，但完成/拒绝后清除，失败/待办/删除清理前保留。消息只有固定技术引用，不含身份、正文或URL。Process账号→资产→工作锁序领取租约/版本后提交，才在无数据库锁时PutOnce私有信封并Scan规范化图片，最终同序复核当前生命周期/资产版本/租约代次，结果/终态/去重回执共同提交。外部不承诺恰好一次，适配器需同key同密文幂等；重试不得覆盖不同内容。拒绝是终结rejected，故障保持pending并持久重试，预算/缺密文转待办；缺目标无操作取消，晚到注销/旧租约不能恢复卡片。ProcessNext提供到期补偿，Consume核验当前数据库事件关联，可供后续MQ订阅注入；不是实际业务worker装配或PG+MQ领域闭环。

第十版显式迁移维护CHECK/FK/表列中文注释和非空回退保护，GORM同步常规字段；前九版不改，旧资产默认pending不冒充已扫描。API身份维护组/领域worker分别使用受限池；领域回执只回读operation_id，维持列级权限。中文完成日志含operation/request_id/result/duration及扫描applied/replayed/cancelled/deferred，不输出文件/身份/图片/供应商原始错误。技术日志不替代认证访问审计或独立删除意图。

## 本轮验证与真实/合成边界

已执行首轮make fmt/check/build通过，真实check-adminauth.py项目liyuanci-admin-check-a9e438d0caca通过，学生卡1.34s/77.2%；追加并发、缺失源和日志边界后项目liyuanci-admin-check-b5195bb27b41通过，学生卡1.22s/75.4%。PG/Redis、事务/权限及HTTP真实，微信/KMS/对象/扫描为明确合成适配器，无真实学生卡或跨端联调。迁移check-migrations.py --container十版/注释/权限/空库上下行/正式UID10001 CLI通过，初轮制品liyuanci-migrate-check:60f12666c753；随后无损JPEG规范化等最终源码须以最终复验增量为准，不冒称旧制品含后续修改。

实际测试覆盖上传重放/异文件冲突、MIME/大小/像素/截断/尾随载荷、未知/跨账号状态、外部期间NOWAIT取得账号/资产锁、图片通过仍未认证、拒绝、重试稳定对象、MQ缺失时补偿、外部panic、预算待办、旧租约/过期重领、并发有效租约只外发一次、伪造/替代消息、缺密文待办/缺资产取消、注销晚到、严格multipart/no-store和工作写失败原子回滚。初次vet发现工作Attempts为int32、配置为int的类型比较，改显式安全转换后检查通过；不隐瞒初次失败。最终验证/制品另记。

## 状态与后续范围

本阶段是T011上传/扫描子范围，人工认证申请/重提、学号唯一绑定/姓名加密、后台案件/原卡代理/decision和访问留迹尚未实现；图片approved不授予认证。真实D02桶/扫描/KMS、独立执行身份、生产资源/保留参数、实际worker循环/健康/限并发、管理待办/重驱动策略、对象物理清理仍缺，T007整体未完成。共享契约0.2.0仅新增媒体边界说明/实现元数据，静态索引revision 4：6后台opt-in＋6学生身份/学生卡注入库＋71未实现；没有新增Wire字段或激活审核草案。详情见本仓student-card.md与共享contracts/student-card.v0.2.0.md，不用链接替代本阶段范围/流程说明。没有推送、部署或更新只读审批/需求/设计。

## 最终验证与交付快照

最终运行源码（含无损PNG规范化、取消元数据、缺失源恢复及Consume/transportmq.Handler编译一致性断言）执行make fmt、make check、make build通过；最终真实check-adminauth.py项目liyuanci-admin-check-f8c835e85b1c退出0，后台14.33s/82.0%、学生1.20s/84.4%、学生卡1.38s/76.3%，包含JPEG真实上传→规范化PNG→扫描，以及成功/失败/取消/Deferred完成日志断言。无DSN常规检查的跳过不作为真实测试，以上结果来自独立真实Docker PG/Redis项目，外部服务仍是合成适配器；本次容器/网络已清理、卷保留。

最终check-migrations.py --container退出0，正式制品liyuanci-migrate-check:a990bb58ff21；锁定Go1.25.0、正式UID10001 CLI、十版重复up/permissions与空库down/up、数据库表列中文注释、受限权限和恢复库混建拒绝均通过，历史九版SQL未改。该镜像的离线库测试不等于真实供应商或MQ业务往返。服务脚本单元13项通过；未变更启停/监控/Broker配置，不重跑check-services.py、监控专项或原MQ探针，不声称完成T007同一真实PG+MQ领域闭环。

共享contracts/build.py与validate.py通过83操作/130 schema/783请求响应示例及动作正反例；revision 4固定当前文件指纹，r1–r3留作历史。既有LibreSSL/RefResolver告警保留但退出0。本轮没有Go依赖变化，不需要tidy；两仓diff-check通过。提交前阶段记录更新后再次执行make fmt/check。实际私有桶/扫描/KMS、业务进程装配、人工认证和物理删除等未完成边界保持上节，不将可注入模块签为整体完成，也没有推送或部署。

## 后续人工认证模块

第七阶段已实现T011人工认证申请/驳回重提、后台队列/详情/原卡代理/决定、唯一学号绑定/姓名加密和敏感访问留迹的注入式模块，上述“尚未实现”为本阶段原交付快照；当前行为和真实验证见[第七阶段](phase-07.md)。实际供应商、学生API/业务worker装配及物理清理仍未完成，图片approved仍不等于学生资格。
