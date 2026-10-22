# 开发业务启动与边界

T004/T008/T011/T014，REQ-002、003、004、005、014、015、018、020。开发业务进程由 `cmd/server` 调用 `devapp.Open`，复用 `studentapi.New` 与 `RegisterAdmin`，不是新建另一套业务逻辑。宿主健康模式保持可独立启动。

## 首次启用与日常使用

```sh
./scripts/init-dev.sh
python3 scripts/init-business-dev.py --admin-login developer
./scripts/start.sh dev --bootstrap-probe --probe
# 此后只需：
./scripts/start.sh dev
```

`--admin-login` 必填，无内置后台账号。默认配置为 `~/.config/liyuanci/dev/operations.env`；初始化器另外支持 `--project NAME --env-file /绝对路径/operations.env`，必须与start的项目和文件相同。脚本要求已有Docker、仓库外基础凭据，不执行环境文件中的shell。初始化是操作者显式运行的迁移入口，日常启动不执行DDL、检索配置或账号初始化。

初始化新建专用 `liyuanci_business` 数据库，保留现有 `liyuanci` 探针库；迁移用 `dev_migrator` 登录，权限矩阵由隔离数据库管理员应用。四个运行登录 `dev_identity_maintenance`、`dev_admin_api`、`dev_api`、`dev_admin_maintenance` 分别属于对应无登录权限组。进程检查登录身份无超级用户、绕过RLS、建库/建角色/建schema、表所有权或其他角色成员资格，不能用超级用户连接冒充最小权限。运行身份不读取迁移账本，只经系统目录验证所需结构。

成功后将 `LIYUANCI_DEV_BUSINESS_FILE` 登记到operations.env；dev启停入口自动加载 `compose.business-dev.yaml`，只读挂载该文件和本项目 `business-objects` 密文卷。未初始化时启动器明确提示命令，基础API只提供健康检查。`APP_DEVAPPFILE` 仅能在 `integration` 环境启用，不能与旧 `adminauth.enabled` 重复装配；生产配置拒绝本地适配器。默认宿主监听18080，容器内8080，可用 `LIYUANCI_DEV_API_PORT` 覆盖宿主端口。

## 凭据与实际资源

同目录 `business.json` 含四个运行DSN、七个独立32字节密钥（JSON标准base64）、`AdminPassword`、`WeChatAppID`、`WeChatSecret` 和容器对象目录。迁移凭据只在独立宿主文件 `business-migration.json`（0600）中，不挂载给API。宿主父目录0700；运行配置文件0644供容器非root用户读取，只挂载单个文件，不挂载整个凭据目录。API环境也不继承探针管理员DSN，共享健康连接使用普通业务运行身份。不要输出或提交文件正文。初始后台口令读取 `AdminPassword`，首次登录后按后台改密接口操作。重复初始化不会覆盖现有密钥、重置已有账号或生成另一个后台账号。

微信配置同时填写真实 `WeChatAppID` 和 `WeChatSecret` 后重启API，通过现有固定微信官方接口交换code，不接受客户端OpenID或本地模拟身份。未配置微信时，合法登录请求返回503；空请求仍是400，不能把缺凭据伪报登录成功。

本地包裹密钥使用AES-GCM包裹随机数据密钥，身份检索使用独立HMAC密钥，固定用途和引用；会话/三个领域回执/后台回执分别用独立密钥，配置拒绝复用。它们是开发文件资源，不是生产KMS。私有对象只保存领域层生成的密文：同目录临时文件写完同步后原子发布，业务随机key哈希成文件名，版本为密文摘要；重复相同写入成功，不同密文拒绝覆盖，读取验证精确版本。

已有业务数据库却缺原配置时拒绝生成新密钥；已有登录角色不会改密。恢复须保留原配置、业务库和对象卷，不能通过删文件重新初始化。stop及remove均保留卷，隔离检查同样保留卷与原凭据。

## 可访问接口

完整注册29条路由，含两条健康检查。`/api/v1` 是前缀，本身仍404。

| 功能 | 入口示例 | 未登录或缺配置时 |
| --- | --- | --- |
| 后台登录/账号/角色/改密/退出 | `POST /api/v1/admin/sessions`、`GET /api/v1/admin/accounts` | 登录可用，受保护入口401 |
| 学生登录/本人/资料/退出 | `POST /api/v1/auth/wechat-sessions`、`GET /api/v1/me` | 未配置微信时合法登录503，本人401 |
| 学生卡上传及状态 | `POST /api/v1/me/student-card-assets`、`GET /api/v1/me/student-card-assets/:id` | 未登录401 |
| 本人认证申请及状态 | `POST /api/v1/me/verification-requests`、`GET /api/v1/me/verification-request` | 未登录401；开发学校规则模拟通过，保留领域校验和人工审核 |
| 后台认证队列/详情/原卡/决定 | `GET /api/v1/admin/identity-reviews` 等 | 未登录401，实际权限仍按数据库会话复核 |
| 心愿创建/编辑/删草稿/送审/上架/撤下 | `/api/v1/wishes` 及 `/:id/...` | 未登录401；审核未完成不能上架 |
| 心愿公开详情/广场/本人列表 | `GET /api/v1/wishes`、`GET /api/v1/wishes/:id`、`GET /api/v1/me/wishes` | 广场可匿名查询，本人401，非公开资源404 |

业务错误继续使用各模块现有协议，HTTP成功、失败及异常恢复仍有request_id/status/duration完成日志。启动先完成资源验证和全部路由注册再监听；关闭先等待HTTP请求结束，再逆序关闭四个业务池，最后关闭共享中间件。装配失败逆序关闭已开启资源，只记录安全错误和完成结果。

## 尚未完成与检查

学生卡扫描在隔离开发环境默认模拟通过，独立card-worker每轮处理一条到期数据库任务；普通worker仍是探针。学校规则在开发环境模拟通过，真实学校规则及心愿审核消费者未配置，心愿送审不会自动进入可上架状态。未提供在读年级字典，带年级输入按现有领域规则失败关闭。整体readyz保留503，不把健康200解释为全部业务就绪。真实微信联调、生产KMS/私有存储/扫描/学校权威规则、领域消费者、跨端/性能/生产验收仍缺。

`python3 scripts/check-business-dev.py` 在独立Docker项目与随机宿主API端口验证迁移、权限、重复初始化不改凭据、HTTP鉴权与校验、真实PG/Redis后台登录/授权、匿名心愿列表、微信缺配置503，以及普通停止/重启保留会话。结束只移除本项目容器/网络，保留卷和打印出的原凭据目录。标准单元检查无需中间件；执行结果和环境限制见[第十阶段](milestones/phase-10.md)。

## 开发学生卡扫描模拟

T011/REQ-002、015、018、020：仅 integration 环境的 `card-worker` 默认模拟图片安全通过，保留上传校验、密文写入、账号/资产/租约复核和最终事务。每轮处理一条到期任务，结束后等待一秒。使用 PostgreSQL 补偿入口，不启动 MQ 消费或转发，outbox 事件保持未投递；不代表 MQ 业务闭环。普通探针 worker 保留。生产环境拒绝运行该入口，整体 readyz 仍503。

已有环境需手动执行 `python3 scripts/init-business-dev.py --admin-login 原登录名`，仅补建 WorkerDSN 及独立 dev_domain_worker 身份，不更换历史密钥或重置账号；随后 `./scripts/start.sh dev` 构建并启动 card-worker。新环境同样先显式初始化。没有 WorkerDSN 时拒绝启动，不回退管理员连接。普通启动不初始化身份或迁移。停止沿用项目级入口，等待处理结束后关闭池，保留卷。

手动验证：上传合法图片获取202/pending，再查询本人资产，正常应变 approved；随后提交人工认证并走后台审核。图片安全模拟通过不自动改变学生资格。历史 pending/retry_wait 到期工作可继续处理，attention 不自动重置。通过日志 simulated=true 辨识模拟，不能将本环境作为真实扫描或生产验收证据。

## 开发学校规则模拟

T011/REQ-002、015、018、020：仅由 devapp.Open 在 integration 环境注入学校姓名/学号规则模拟适配器，不查询学校权威数据，正常上下文直接通过，取消或超时仍失败。日志包含 operation、simulated=true、result、duration，不输出姓名或学号。生产仍拒绝开发装配，真实学校校验必须显式注入，通用认证服务没有默认放行实现。

本改动不绕过输入校验、学号唯一占用、会话授权、学生卡归属/扫描状态、版本、幂等或后台审核权限。联调顺序为上传图片并等待 approved、提交认证申请、后台查询队列和详情、按当前版本通过或驳回、查询本人认证状态；驳回后重提须携带当前 expected_version。学校模拟不会自动批准学生资格。需由操作者重建开发 API 后生效，无新增配置或迁移，整体 readyz 继续503。

按用户要求本次只修改文件，未运行格式、测试、构建或真实联调，未重建/重启容器；完整流程待操作者验证。
