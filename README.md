# 鲤愿池 Go 后端

面向校园用户的心愿互助、匿名交流与资料服务。API 与后台任务独立运行，PostgreSQL 保存业务权威数据及持久任务，Redis 提供共享限流。工程使用 Go 1.25+

开发入门与工程约定见 [开发者指南](DEVELOPER_GUIDE.md)，配置字段见 [配置说明](CONFIGURATION.md)，详细业务状态见 [功能链路](FUNCTIONALITY.md)，实际验证与交付边界见 [后端现状](../docs/development/backend/README.md)。

## 目录结构

```text
cmd/                          # 4个命令入口：参数、信号与退出状态
configs/                      # 公共YAML、环境覆盖模板、词库与公开资料
internal/
  app/                        # API/worker/初始化/迁移装配与资源生命周期
  config/                     # 严格配置校验与应用密钥派生
  httpapi/                    # HTTP处理器、输入校验与响应映射
  modules/                    # 账号、心愿、聊天、树洞、审核、安全、隐私与资料
  execution/                  # 授权事务、锁、幂等、持久任务与调度
  platform/                   # 中间件客户端、日志、YAML与加密叶子能力
  adapters/                   # HTTPS供应商、本地运行与Redis业务适配
  entity/                     # 每表独立的ORM模型
  migration/                  # 显式迁移、模型清单及版本SQL
  testconfig/                 # 隔离集成测试配置与SQL夹具
migrations/                   # 专用SQL与迁移操作说明
scripts/                      # 统一管理入口、词库发布与原生集成
Makefile                      # 开发与检查命令
DEVELOPER_GUIDE.md             # 阅读路线、开发流程与详细运行说明
CONFIGURATION.md              # 配置字段、覆盖规则与供应商设置
FUNCTIONALITY.md              # 业务链路、状态与权限规则
```

请求沿 `httpapi → modules → execution/存储` 执行，由 `app` 注入依赖。业务模块不依赖装配或具体适配器；表结构由版本 SQL 管理，普通 API/worker 不执行 DDL。模块检索和依赖边界见 [代码组织](DEVELOPER_GUIDE.md#7-代码组织与重构边界)。

## 启动方式

### 本地业务环境

安装 Go 1.25+、Python 3，以及已启动的原生 PostgreSQL、Redis。数据库管理员须先创建专用业务库和拥有本库对象的普通应用账号，禁止超级用户、BYPASSRLS 或集群管理权限。

```sh
python3 -m pip install --target .local/tools/python -r scripts/requirements.txt
make build
make configure               # 首次生成私有开发配置，已有文件拒绝覆盖
# 编辑 configs/development.yaml：填写数据库、Redis及所需凭据
make check-config            # 离线校验
make migrate                 # 显式创建结构并配置应用账号权限
make init                    # 显式初始化首个后台账号和检索引用
make doctor                  # 连接依赖诊断
make run                     # 前台API，默认127.0.0.1:8080
```

API 启动后，在另一个终端执行 `make wordlists`，通过后台鉴权接口发布开发词库。后台初始账号和口令从私有 YAML 读取。首次启用树洞还须填写 `whispers` 开发配置，并执行 `bin/migrate -config configs/development.yaml opaque-blocks`。

后台任务独立运行，按功能分别启动，每条命令占用一个终端或独立进程：

| 命令 | 职责 |
| --- | --- |
| `make worker KIND=card` | 学生卡扫描 |
| `make worker KIND=content` | 心愿、应愿、故事、聊天等普通内容审核 |
| `make worker KIND=whispers` | 树洞、昵称、暖评审核及胶囊任务 |
| `make worker KIND=account` | 账号注销清理 |
| `make worker KIND=evidence` | 独立证据清理 |

构建只生成 `server`、`worker`、`init`、`migrate` 四个命令。`worker -kind …` 与 `init` 在开发和生产共用入口，适配器由 YAML 模式决定；检查及恢复参数见 [进程与动作](CONFIGURATION.md#进程与动作)。

所有进程读取同一环境 YAML，使用同一组数据库分项字段及 `masterkey`。配置为 `configs/common.yaml` 合并私有环境覆盖，不读取环境变量或独立秘密文件；已有数据时不能直接更换主密钥或环境名。普通启动不迁移、不初始化、不重置数据，也不自动启停 PostgreSQL/Redis。

### 健康入口与生产运行

只检查程序启动时，构建后执行 `bin/server -config configs/common.yaml`。公共配置默认使用 `mode: foundation`、空 `database.driver` 和关闭的Redis，无需中间件或业务凭据；仅提供 `/healthz`、`/readyz`，没有业务路由。`/healthz` 返回存活状态，未装配业务依赖时 `/readyz` 返回503。开发与生产覆盖均须显式设置 `database.driver: postgres`、完整连接字段及 `redis.enabled: true`。

生产使用私有 `configs/production.yaml`，为上述 Make 命令指定 `CONFIG=configs/production.yaml`，并配置真实服务。当前只采用原生二进制，由宿主服务管理器管理 API 及五类 worker。首次装配、词库与屏蔽策略初始化、删除账本及恢复前两域重删步骤见 [生产流程](DEVELOPER_GUIDE.md#5-完整生产装配与上线流程)和[原生运行与恢复](DEVELOPER_GUIDE.md#6-无mq原生生产装配)。

## 功能实现

| 模块 | 当前实现 | 关键规则 |
| --- | --- | --- |
| 认证与账号 | 微信登录、学生卡上传与扫描、人工认证、后台登录及岗位会话 | 学生与后台身份隔离；登录不等于认证通过；材料须属于本人 |
| 心愿与应愿 | 草稿、送审、上架、公开查询、应愿、唯一匹配、撤销、重招募与实现 | 审核通过进入 `ready`，作者另行上架；版本、资格和额度在服务端复核 |
| 聊天与还愿 | 匹配会话、消息审核送达、通知、还愿故事与分享引用 | 会话结束后只读；未审消息不可送达；故事审核后才公开 |
| 悄悄鲤 | 文字树洞、三档身份、已审昵称、暖评、抱抱及本人时空胶囊 | 匿名标识不关联账号；胶囊须到期且审核通过才可读，列表不返回正文 |
| 审核与治理 | 词库分流、人工复核、危机隔离、举报、处罚、申诉与屏蔽 | 岗位权限分开；解除处罚不恢复已隔离内容；晚到审核复核当前版本 |
| 平台用户 | 用户查询、详情与处罚历史，超管封禁/解封、禁言/取消禁言 | 必填理由，校验版本与幂等，保留加密审计 |
| 资料广场 | 认证用户查询、复制与四类反馈，后台分类及资料维护 | 下线后才能编辑；链接和提取码加密；后端不访问外部网盘 |
| 隐私与恢复 | 注销屏障、匿名回执、账号/证据异步清理与恢复前重删 | 受理不等于清理完成；独立加密账本保留删除意图；回执不涵盖外部对象或备份 |

学生卡图片扫描使用腾讯云数据万象 CI：通过才批准，疑似/违规拒绝并提示重新上传，故障重试；学校校验按当前规则直接通过。文字使用已发布词库，命中或显式重审进入人工复核。腾讯云 COS 保存密文并按摘要精确读取；微信登录使用真实接口。

业务规则、状态及回退细节见 [FUNCTIONALITY.md](FUNCTIONALITY.md)。源码能力与正式验收分别记录，真实供应商、目标 Linux、真机、容量和独立灾备的剩余事项见 [缺陷与依赖](../docs/development/backend/defects.md)及[后续计划](../docs/development/backend/plan.md)。
