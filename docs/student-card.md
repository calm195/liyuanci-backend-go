# 学生卡上传和扫描模块

T011/REQ-002、015、018，关联T007/REQ-020。模块只提供可注入的上传/本人扫描状态与外部工作处理，不提供人工认证、公开图片URL或默认供应商。

## 显式装配

先显式migrate up/permissions至第十版。身份维护组API池构造studentcard.New，显式注入studentauth.Service、Codec、Objects、Scanner、zap及Config；领域worker用独立domain_worker组池分别构造。资源关闭归外层，先停止HTTP/任务接收并等待在途结束再关闭池和供应商客户端。构造器不迁移、不创建桶、不初始化密钥或启动循环。

Config要求DecodeSlots（1–16）、MaxBytes、MaxPixels、独立至少32字节ReceiptKey、Command超时/锁/幂等参数、Lease>=Command.Timeout、RetryDelay和MaxAttempts（1–1000）。这些是显式技术配置，不视为生产参数决定。上传源与无损规范化输出都受MaxBytes约束，既有HTTP入口1MiB全请求限制也继续生效，含multipart开销。大于该边界的生产上传能力、部署资源预算和真实供应商仍待交付。

同时显式RegisterStudentCards，studentauth.Config.StudentCardAssets=true才在未受罚Me动作中提示postMeStudentCardAssets。默认server没有学生或学生卡装配；cmd/worker仍是原有隔离探针，不把本库实现当成进程接线完成。前后端共享接口详见docs仓库contracts/student-card.v0.2.0.md。

## HTTP与数据流程

POST /api/v1/me/student-card-assets要求有效学生Bearer、Idempotency-Key和唯一file multipart部分；PNG/JPEG类型/实际头部一致，拒绝额外字段、多个文件、无效解码、超字节/像素。使用MultipartReader，不将明文落盘。先解码再无损编码PNG，去除非像素元数据/尾随载荷，不进行二次JPEG有损压缩。上传解码/加密并发有界，耗尽429；受罚403，身份失败401，格式400，依赖503，全部no-store。

图片与随机对象key分别以资产UUID及用途/AAD绑定加密。命令摘要绑定独立ReceiptKey HMAC，不记录文件名/原图/裸摘要。账号guard后复核资格，资产（pending）、暂存信封、work_items/outbox_events及命令回执同根事务提交；任何失败全部回滚。202只表示已持久接收，body为asset_id/scan_status。成功重放仍重新授权，返回原资产和当前状态，不重复创建工作。同键异摘要409。

GET /api/v1/me/student-card-assets/{id}先按guard→学生会话锁序核验主体/代次/到期/撤销及生命周期，再返回本人asset_id与pending/approved/rejected。未知/非本人/删除状态404，无对象key/密文/URL。受罚主体允许读取最小状态，不授予上传或人工认证权限。

## 外部调用与可靠工作

任务使用固定student_card.scan工作、asset目标和student_card.scan_requested技术事件；事件只有技术引用。Process从数据库读持久源，账号→资产→工作锁序领取，推进工作版本/尝试预算并提交租约，释放所有数据库锁后才调用Objects.PutOnce和Scanner.Scan。Objects必须同随机key/相同密文幂等并返回同一不可变版本；不允许覆盖不同密文。写入内容是信封密文，Scanner收到有界内存中的规范化PNG；供应商必须遵守context、拒绝保留原图或记录正文。

最终事务按同序重锁，检查当前账号生命周期、资产版本和租约代次/有效期。扫描通过写active/approved，明确拒绝写failed/rejected；工作终态与消费者回执同事务提交，之后才允许运输层ACK。两种终结均清除数据库暂存密文；图片安全通过不会更新accounts.verification。重复终态不重复外发；有效租约返回Deferred。外部操作不是恰好一次，确认前崩溃可能重复PutOnce/Scan，供应商适配必须保持幂等且无额外业务副作用。

外部故障/panic持久重试，预算耗尽或源密文缺失转attention，保留待人工处理密文，不伪报approved；父context取消或数据库无法写确认时保留租约，过期可重新领取。旧持有者不能覆盖新周期。ProcessNext按数据库到期工作显式补偿，不依赖MQ；Consume先核对数据库事件关联并在工作锁后复核，不从伪造/替代事件重建工作。目标已物理清理则无操作取消，不恢复内容。领取前或外部成功后注销均取消并标记deleting，保留已知对象版本以供后续清理。

## 资源和未完成范围

第十版添加assets.version/scan_status及student_card_payloads，全部中文数据库注释，前九版不改；GORM实体与业务注册表同步，版本SQL维护CHECK/FK/中文表注释和非空回退保护。旧资产scan_status默认pending，不从旧active状态推断安全扫描通过。API维护组只有必要读取/创建/工作追加，领域worker独立领取/修改/清理暂存；普通后台不能读取暂存图像密文。

真实私有桶/图片安全服务、生产密钥/权限、业务worker循环/健康/限并发装配、异常待办管理/安全重驱动策略、被拒绝/取消/孤立对象的物理清理及保留期仍未交付；不提供原卡代理读取。人工认证申请/重提、学号唯一/姓名加密及后台审核/访问留迹是T011后续范围。该部分状态保持未实现，不把测试夹具当正式规则或真实资源。

人工认证申请、重提和后台审核的可注入模块已在后续交付，见[人工认证说明](verification.md)及[第七阶段](milestones/phase-07.md)；本文件原“人工认证未实现”仅表示上传/扫描模块当时的阶段边界，当前外部供应商和进程装配缺口仍保留。
