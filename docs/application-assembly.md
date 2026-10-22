# 学生应用与学生卡 worker 显式装配

T007/T008/T011，REQ-002、014、015、018、019、020。这里提供可由宿主调用的应用构造入口，不新增默认供应商、开发密钥或自动迁移。当前 cmd/server 和 cmd/worker 的默认启动行为保持原有范围，正式供应商接入及进程配置尚未交付。

## 学生 API

宿主先通过显式迁移入口应用十一版结构及 permissions，再以独立执行身份构造 StudentDB（identity_maintenance）及 ReviewDB（admin_api）。studentapi.New 拒绝同一个底层连接池，即使传入不同 GORM Session 也不能混用。此检查不代替数据库角色验权，实际执行身份仍须由宿主核验。池及供应商生命周期归宿主，构造器不执行 SQL、Ping 或供应商调用。

Dependencies 显式注入日志、Ready、Exchange、Identity、Limiter、Codec、Objects、Scanner、Rules。Identity 同时承担微信身份解析和学生学号绑定，复用同序锁与双代查重；Objects 同时实现 PutOnce 和精确版本 Open，避免上传与原卡代理指向不同存储。没有创建对象桶、设置密钥或生成合成身份的默认路径。

Config 分别提供 Auth、Card、Verification 参数；SessionKey、学生卡 ReceiptKey、人工认证 ReceiptKey 必须各自独立，不能取相同字节。审核图片上限必须覆盖上传模块的最大图片上限。其余期限、锁、并发和预算沿用各领域构造器校验，不把测试值当作生产政策。

全部领域构造成功后注册四项学生身份、两项学生卡及六项人工认证操作，加两项健康路由，共十四路由。后台登录可由宿主另外显式调用 RegisterAdmin 注册，人工认证审核使用单独后台池及现有数据库会话授权。任何构造失败均不返回局部路由；构造器只在这套完整注册内开启 Me 的学生卡/认证动作提示，不修改调用方 Config。默认不开放心愿或其他未装配操作；可选心愿装配见下文。

Ready 必须显式提供真实依赖检查；构造成功本身不意味着密钥或外部资源已就绪。宿主通过 HTTP Server 提供返回的 Engine，关闭时先停止 HTTP 接收并等待请求结束，再关闭两个池及供应商。

## 学生卡 worker

宿主使用独立 domain_worker 池构造 studentcard.Service，并在任何 MQ 客户端启动前调用 transportmq.ConfigureSDKLogging。随后调用 cardworker.Open(ctx, db, scanner, logger, cfg)，其中 scanner 必须同时支持 Consume 和 ProcessNext。此模块仅处理固定 student_card.scan_requested 事件，不复用探针主题，不创建 MQ 主题或数据库结构。

Config 显式提供 MQ 通道/鉴权、Relay 租约/重试预算、RelayInterval、ScanInterval、OperationTimeout。Open 创建生产者、数据库 Relay 及订阅器；后续启动失败逆序关闭已创建生产者。成功后启动两个独立单线程循环，一个调用 Relay.Once，一个调用 ProcessNext。每轮完成后等待配置间隔，空队列、失败、panic 和超时均不会热循环；每轮独立 context 受 OperationTimeout 限制。消息消费并发单独由 MQ.Concurrency 控制，数据库租约仍是最终并发和重复执行屏障。

数据库补偿持续存在，即使 MQ 唤醒缺失或 Deferred 已 ACK，仍能扫描到期工作。转发失败和扫描失败分别记录 operation/result/duration 中文完成日志，不输出供应商错误正文；调度成功日志仅表示一次调用结束，不代表整条队列清空或所有扫描通过。领域状态、预算耗尽、取消、重试和幂等继续由 durable/studentcard 持久逻辑决定。

Open 的 context 仅用于启动，不成为整个生命周期的期限。宿主必须显式调用 Close：先取消调度，停止订阅并等待在途消息处理，再等待两个调度循环退出，最后关闭生产者。Close 并发/重复调用只释放一次自有资源；某个 Close 返回错误仍继续释放其他资源。依赖适配器必须遵守 context，宿主只在 Close 返回后关闭数据库和供应商；本模块不强杀不响应取消的 goroutine。

## 本轮验证范围

按用户要求不启动跨端联调或真实中间件测试。单元测试覆盖完整路由注册、未授权拒绝、readyz 失败、缺配置不返回局部入口、共池/共密钥/图片预算冲突拒绝，以及调度 panic/故障恢复、超时、取消、关闭等待与并发关闭。测试中的供应商只会 panic，不提供合成成功业务；GORM 仅创建禁用 Ping 的本地连接池，不执行真实数据库操作。

实际检查结果见[第八阶段记录](milestones/phase-08.md)。此交付不关闭 T007/T008/T011 整体任务、真实 MQ+PG 领域闭环、真实供应商或部署验收。

## 可选心愿模块

T014、REQ-003/004/005/008/018：Config.Wish非空时必须注入独立WishDB业务运行池，不能复用StudentDB/ReviewDB或会话/材料密钥。全部依赖构造成功后注册九个心愿命令/查询入口，共二十三路由；Me只在同次装配及当前资格允许时提示postWishes。Grades可不提供，但填写年级时失败关闭。普通启动不执行DDL、外部交换或后台工作；资源仍由宿主按原顺序关闭。未提供真实资源，不宣称cmd/server或生产已接线。详见[心愿模块](wish.md)和[第九阶段](milestones/phase-09.md)。

## 开发进程接线更新

2026-10-21：以上未装配进程的描述保留原阶段语境。当前新增仅限integration的devapp资源宿主，经显式初始化后，start dev装配后台、学生、学生卡、人工认证及心愿路由。开发文件密钥/私有密文对象已接入，真实微信需提供配置；学生卡扫描现由独立开发card-worker模拟通过，学校姓名/学号规则也在开发宿主模拟通过；真实扫描供应商、学校规则、年级字典及心愿审核消费者仍缺，整体readyz保持503。原cmd/worker仍是探针。详见[开发业务装配](development-business.md)及[第十阶段](milestones/phase-10.md)。
