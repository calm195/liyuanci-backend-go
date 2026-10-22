# 心愿领域、生命周期与查询

T014；REQ-003、004、005、008、014、015、018、020。采用正式 OpenAPI 0.2.0 的 WishInput/WishUpdate/WishResult/PublicContent 与既有状态及动作ID，不改变传输形状。完整过程和真实验证见[第九阶段](milestones/phase-09.md)。

## 依赖与显式装配

`wish.NewService`要求普通业务运行角色 `liyuanci_api` 的PostgreSQL池、注入的zap.Logger、securedata.Codec、可选Grades及ServiceConfig。超时、锁等待、重试、回执保留期和至少32字节独立回执密钥必须显式提供；密钥从仓库外注入，不能复用学生会话/学生卡/认证材料用途。未填年级不依赖字典，填了但字典未交付或故障时失败关闭。

迁移身份先通过既有显式入口应用版本0012及权限引导；API不执行DDL。新字段为wishes.draft_deleted_at、wish_versions.review_task_id和moderation_tasks.restriction_epoch，均带中文数据库注释。历史任务新代次为空时不授予上架许可。学生API只有使旧任务/工作失效的必要列更新权，没有审核结论、审核人或DDL权限。

独立模块在httpapi.New的带完成日志/恢复路由上调用`httpapi.RegisterWishes`；它包含创建，不可重复调用RegisterWishCreation。只需要原创建子模块时仍可单独注册RegisterWishCreation。完整学生应用可配置 `studentapi.Config.Wish` 并提供 `Dependencies.WishDB`、`Grades`；业务池必须独立于身份维护及后台审核池，密钥用途不能复用。配置为空保持原十四路由；启用后共二十三路由。构造失败不返回半套应用，不拥有或关闭宿主资源。Me仅在同次装配、当前认证和未受交互处罚时提示postWishes；GET操作不塞入allowed_actions。

cmd/server尚未配置真实学生身份、密钥、对象等资源；普通默认启动继续只开放已有基础能力。此注入模块不以测试密钥冒充真实KMS，也不提供默认管理员、业务Mock或启动时迁移。

## 请求与操作

写入使用学生Bearer、Idempotency-Key和application/json。全部响应no-store/no-cache并带X-Request-ID。顶层/标签字段精确白名单，拒绝重复键、null、大小写错误、未知字段、尾随JSON、错误类型、非法UTF-8及超1MiB请求。正文1–500Unicode码点且非全空白，期限1–30整数天，省略报答方式/期限补sincere_thanks/7；显式空值或0不使用默认。

| 方法与路径（前缀/api/v1） | 输入 | 实际行为 |
| --- | --- | --- |
| POST /wishes | WishInput | 201私有draft，不占活跃额度、不计时、不自动送审 |
| PUT /wishes/{id}/content | 完整WishInput及refresh_expiry、expected_version | 200新内容版本回draft，旧审核及已上架未终结申请失效 |
| DELETE /wishes/{id}/draft | expected_version | 204；仅draft，重复同命令仍204；删除标记与密文清除原子提交 |
| POST /wishes/{id}/submit-review | expected_version、content_version | 202 pending_review，同事务落审核任务、持久工作及最小唤醒 |
| POST /wishes/{id}/publish | expected_version | 200 published/ready/expired；复核当前审核/资格/屏障/额度及数据库期限 |
| POST /wishes/{id}/withdraw | expected_version | 200 withdrawn，未终结申请失效；不承担匹配退出 |
| GET /wishes | category?、campus?、cursor?、limit? | 游客只看当前有效已审公开投影，按published_at/id降序 |
| GET /wishes/{id} | 可选学生Bearer | 本人当前私有视图或游客公开视图；显式坏token401，无权限404 |
| GET /me/wishes | state?、cursor?、limit? | 本人状态列表、精确当前review_result、资格动作；不混入他人正文 |

列表默认20，范围1–100；拒绝重复/未知/空查询参数。游标HMAC绑定用途、主体及筛选，禁止篡改和跨查询复用。命令返回当前元数据及空动作数组，客户端通过当前详情/本人列表获取最新可执行动作，不把历史命令回执当授权。错误使用既有400/401/403/404/409/500/503映射，原始数据库/供应商错误不出现在响应。

## 事务与公开边界

命令按统一锁序持参与者guard、心愿、申请、任务，重发现集合变化则回滚重试；工作源在业务资源锁之后按ID锁定。聚合/密文版本/注册/任务取消/申请失效/持久工作/回执同根事务提交。用户标签与正文共同加密，公开列只允许后续审核写入。删除不破坏历史技术外键，不代表D05全副本清理或证据删除。重放逐次复核当前资格和归属，不解密/重建历史结果。

送审工作路由由wish.ReviewRoute固定，目标是精确任务ID、版本是内容版本；消费者必须复核当前送审绑定、作者资格、限制/隐私代次与删除标记。T016扫描/人工复核消费者和真实供应商未交付，本模块不合成approved、不注册审核决定入口。取消的工作由持久源阻止再转发/消费。正常上架只从当前ready且精确批准版本显式执行，涉校需要人工审核；matching跨原期限仍占额，最多三条。额度释放不自动上架，满额不启动时钟，截止时刻公开立即失效。

查询在有界只读可重复读事务内复核当前会话、作者、版本、审核、限制与屏障。公开只返回审核正文和最小公开资料，不解密原文；隐藏设置立即影响标签。屏障期间本人也不解密旧正文。本人published超时直接展示expired；matching不因原期限变为expired。匹配参与者视图由T019实现，故事/分享由T023等后续任务实现。

## 验证与剩余

运行 `make fmt`、`make check`、`make build`；`python3 scripts/check-migrations.py`验证增量迁移、权限、表字段中文注释与既有框架回归；`python3 scripts/check-wish.py`以锁定Docker PostgreSQL、随机隔离容器/测试库及最小权限运行角色验证真实事务和HTTP。普通Go检查无显式测试DSN时会跳过数据库测试，不能据此报告数据库已测。密钥/身份/审核批准样例为明确合成夹具；没有实际供应商、MQ领域消费、跨端、生产或性能验收。

本轮可独立开发的生命周期、广场/本人查询、HTTP与可选应用装配已实现。剩余T016真实审核消费者、T019匹配视图、实际资源及默认进程接线按对应依赖推进；正式共享索引实施元数据仍由契约发布流程收口，不把本端注入实现等同跨端联调或整项用户验收。

## 开发进程接线更新

T014/REQ-003/004/005/018：现已通过devapp在显式初始化后的Docker开发API进程注册完整心愿路由，使用独立业务登录/池和回执密钥。以上cmd/server未装配描述为原阶段状态。当前未提供年级字典，带年级输入按领域规则失败关闭；送审仍不伪造审核通过，真实审核消费者未接入。详见[开发业务装配](development-business.md)及[第十阶段](milestones/phase-10.md)。
