# 后台账号与会话操作

T008；REQ-002、014、016、018、019；共享契约0.2.0。当前独立后台子范围，不包含微信、学生会话、身份审核、词库业务或整个T008验收。

## 先决条件与显式启用

用户已明确确认T005/T006验收通过；新增第七版结构和权限必须用正式migrate的 `up`、`permissions` 显式应用。普通启动不迁移、不创建超管、不重置口令。新列login_name允许历史NULL但不自动生成名称；离线初始化遇到任意已有后台账号均拒绝，不接管历史身份。若确需接管须另行明确维护流程。

默认仅健康端点。启用需 `APP_ADMINAUTH_ENABLED=true` 并提供以下仓库外配置，不提供任何生产TTL/阈值默认：

| 环境变量 | 用途 |
| --- | --- |
| APP_ADMINAUTH_DSN | 连接同一业务权威库的独立后台身份连接，实际登录身份只继承liyuanci_admin_maintenance组；不得用超级用户、绕过RLS、属主、可建库/角色或可继承迁移组的身份冒充。与普通API池分离 |
| APP_ADMINAUTH_INITIALPASSWORDFILE | 仓库外仅受控账号可读的初始口令文件；按用户已确认默认策略提供。应用不内置口令、初始化/创建响应不返回口令，普通启动不修改既有摘要 |
| APP_ADMINAUTH_RECEIPTKEYFILE | 仓库外至少32字节独立随机密钥；用于口令请求回执HMAC和限流键摘要，同环境实例保持一致。不同环境分离；轮换需单独规划回执兼容 |
| APP_ADMINAUTH_SESSIONTTL | 后台绝对会话有效期，必须显式为正；无滑动续期/refresh，不推断学生TTL |
| APP_ADMINAUTH_TIMEOUT / APP_ADMINAUTH_LOCKTIMEOUT | 身份操作总时限和数据库锁时限，锁时限至少1ms且不得大于总时限 |
| APP_ADMINAUTH_RECEIPTTTL / APP_ADMINAUTH_MAXATTEMPTS | 幂等回执期限及命令重发现重试上限（1–10） |
| APP_ADMINAUTH_LOGINWINDOW / APP_ADMINAUTH_LOGINLIMIT | 共享固定窗口期限及允许次数。账号和来源分别限流，成功/失败尝试均计数；不做永久账号锁定 |

还必须显式启用并配置APP_REDIS_*。生产继续遵守全中间件及外部资源要求。后台DSN由仓库外部署秘密提供，不在启动命令参数、提交文件或日志中展开；权限配置不会创建实际数据库登录身份/口令。Redis错误返回503，超限返回429；高代价口令计算最多两项同时执行，饱和拒绝请求，不能绕过共享限流。scrypt参数固定N=32768/r=8/p=1、独立16字节随机盐、32字节输出；格式版本受控，不从数据库读取任意计算代价，不截断口令。

这些是技术有界性，测试配置不构成生产阈值。年级字典和后台TTL仍未确定，不由本实现选择。readyz仍按全项目认证/密钥依赖失败关闭，独立后台接口可测不代表整体就绪。

## 初始化与四个操作

完成迁移/权限、仓库外配置后，线下显式设置 `APP_ADMIN_BOOTSTRAP_LOGIN` 并运行 `bin/admin-init -config <配置路径>`（纯环境配置可 `-config ''`）。该入口仅在后台表为空时创建首个超管，拥有与普通服务同等受限DB身份；不打印账号/口令。首个账号初始化和重复拒绝已经服务集成测试验证；CLI二进制已构建，未在实际开发/生产环境初始化账号。

- POST /api/v1/admin/accounts：Bearer超管、Idempotency-Key、login_name/roles/reason；201返回安全Account。登录名精确区分大小写，不自动去空格/改名；空白名/理由拒绝。重复名称409 STATE_CONFLICT，同键同请求重读当前结果，同键异请求409 IDEMPOTENCY_CONFLICT；重放仍检查当前角色。角色可为空，但无创建权限；本人登录/改密不要求拥有特定角色。创建、角色绑定、理由留迹和命令回执同事务。
- POST /api/v1/admin/sessions：login_name/password；200仅本次返回不透明token与安全账号投影。数据库只保存SHA256令牌摘要。未知账号/错误口令统一401，禁用/缺本地摘要拒绝；先共享限流、事务外计算摘要，再锁当前guard重读摘要/代次，防止旧口令在改密后签发可用会话。
- POST /api/v1/admin/password：本人Bearer、Idempotency-Key、current_password/new_password；旧口令错误403。账号guard→会话锁序，事务内复核当前代次/到期/撤销，成功同事务更新口令摘要、推进账号版本和auth_epoch、撤销全部旧后台会话（含当前）、写留迹与独立HMAC请求回执，204后重新登录。旧会话重试401；新会话同键重放重新授权后只返204，不重复改密。留迹失败则全部回滚。不设置强制首登改密或额外复杂度门槛。
- DELETE /api/v1/admin/session：本人Bearer及Idempotency-Key，204撤销当前会话；旧token再次使用401。退出不删除账号，不把旧token重放成有效身份。

敏感响应no-store；JSON拒绝未知/重复字段、null与尾随值。拒绝学生token冒充后台；会话到期/撤销/代次及账号状态由PostgreSQL当前数据判定。HTTP异常只记录request_id，不回显正文；每个身份操作/HTTP请求均有中文结构化完成日志，异常不记成功。账号动作表只记后台创建/本人改密和必要理由，不保存口令、令牌；技术日志不是业务审计，也不是D05独立删除日志。

## 验证及未完成边界

`python3 scripts/check-adminauth.py` 使用固定digest的独立Docker PG/Redis、随机凭据/项目与loopback临时端口；仅删除本次容器与网络，保留卷。以真实独立DB登录身份运行实际HTTP处理器（httptest），合成账号无真实个人资料；CI新增相同检查，远端结果另报。常规单元检查无测试DSN时明确跳过真实服务。

后台账号分页列表和角色修改已交付；后台会话读取、微信适配和学生会话尚未交付；现有数据库角色变更已验证当前授权即时拒绝，但不将SQL夹具当成角色管理API。未进行三端联调、生产账号初始化、生产TTL/安全参数、负载/性能或生产验收。阶段证据见milestones/phase-05.md。

## 超管账号管理

GET /api/v1/admin/accounts：每页重新验证有效后台会话及当前super_admin角色；limit默认20、范围1–50，cursor为绑定主体和列表用途的签名游标。固定UUID升序键集分页，只返回本地账号安全投影和next_cursor，不含摘要、令牌或理由。列表不是冻结全量快照，并发新增可能在后续重新遍历时出现；未知/重复参数、篡改/跨主体游标400。

PUT /api/v1/admin/accounts/{id}/roles：Bearer超管、Idempotency-Key及roles/reason/expected_version。完整替换角色，可为空；未知角色400、无当前超管403、目标不存在404、旧版本409 VERSION_CONFLICT。账号guard按UUID全局顺序取得，角色、账号version递增、理由留迹及幂等回执同事务；留迹失败全部回滚。同键同请求返回当前安全投影，同键异请求409，撤权后重放仍拒绝。不修改auth_epoch或续期会话，每次请求的角色查询即时反映新角色。允许超管修改自身角色，不擅自新增“最后一个超管”政策。

需要显式执行第八版迁移和permissions；已应用前七版保持原字节。存在角色修改留迹时第八版回退拒绝，不能为回退删除审计记录。
