# 鲤愿池 Go 后端

Go API 与独立后台任务进程，使用 PostgreSQL 保存业务数据和持久任务，Redis 提供共享限流。当前为 `codex/simplify-backend` 简化分支：仅原生运行，数据库只配置一组 `database` 分项字段，由程序生成唯一连接，业务权限由学生/后台会话、岗位角色及领域函数执行。

业务能力见 [FUNCTIONALITY.md](FUNCTIONALITY.md)，字段、覆盖规则与配置说明见 [CONFIGURATION.md](CONFIGURATION.md)，项目现状见 [后端文档](../docs/development/backend/README.md)。

## 1. 配置结构

```text
configs/
  common.yaml                 # 公共默认值及中文注释
  development.example.yaml    # 开发覆盖模板，无真实秘密
  production.example.yaml     # 生产覆盖模板，无真实秘密
  development.yaml            # 私有开发覆盖，Git忽略
  production.yaml             # 私有生产覆盖，Git忽略
  wordlists.yaml              # 词库种子数据
  resources.yaml              # 公开资料模板，投放前核验
```

所有 Go 进程通过 Viper 先读取环境文件旁的 `common.yaml`，再递归合并指定环境 YAML；映射逐字段覆盖，数组整体替换。口令、密钥、令牌、CA 均内联私有环境文件，不读取环境变量或独立秘密文件。资源和状态的相对路径以配置目录解析。

应用自身只维护顶层 `masterkey`（32字节随机值的Base64），内部按环境和用途派生密钥，不再填写 `sessionkey`、`wrapkey` 等字段。数据库/Redis密码、微信和供应商凭据仍独立填写。所有进程必须使用同一环境及主密钥；已有数据时不可直接更换，详见[配置说明](CONFIGURATION.md)。

## 2. 本地准备

安装 Go 1.25+、Python 3、PostgreSQL 和 Redis。在仓库目录执行：

```sh
python3 -m pip install --target .local/tools/python -r scripts/requirements.txt
make build
make configure
```

`configure` 只新建不存在的私有配置：开发主密钥 `masterkey` 与初始化口令一次生成，已有文件拒绝覆盖。随后填写 `configs/development.yaml` 的 `database.host/port/name/user/password`、Redis 地址和所需凭据。应用账号应拥有专用业务数据库，禁止超级用户、BYPASSRLS、建库/建角色及复制权限；数据库及账号由数据库管理员事先创建。

`make configure CONFIG=configs/production.yaml ENVIRONMENT=production` 只复制空秘密模板，正式主密钥及外部凭据由操作者填写；已有数据不能直接更换主密钥。

## 3. 初始化与启动

```sh
make check-config            # 离线检查，不连接依赖、不创建目录
make migrate                 # 显式迁移及应用账号权限配置
make init                    # 显式初始化检索引用与首个后台账号
make run                     # 前台API；Ctrl-C正常退出
```

在另一个终端执行 `make wordlists`，通过后台鉴权接口发布词库。需要悄悄鲤时填写 `whispers` 的开发配置，并显式启用不透明屏蔽策略：

```sh
bin/migrate -config configs/development.yaml opaque-blocks
```

后台任务分别在终端或服务管理器中运行，所有进程共用同一环境文件：

```sh
make worker KIND=card
make worker KIND=content
make worker KIND=whispers
make worker KIND=account
make worker KIND=evidence
```

开发图片扫描使用明确的本地模拟；普通内容默认人工模式；缺少真实微信、通知、清理供应商的能力保持不可用或待办。普通启动不迁移、不初始化账号、不重置任务或替换密钥。停止 API/worker 不停止 PostgreSQL、Redis。

## 4. 检查与集成

```sh
make check-fast PACKAGES=./internal/config/...
make fmt
make check                    # 格式只读检查、go vet、race/coverage
python3 -m unittest discover -s scripts -p 'test_*.py' -v
make integration              # 真实、一次性的原生PG/Redis与实际Go进程
```

`integration` 需要宿主安装 `initdb`、`pg_ctl`、`psql`、`redis-server`。它创建随机端口及临时集群，使用独立测试账号，退出后清理本轮进程和数据。供应商由隔离 TLS 夹具替代；通过不等于正式供应商、手机或生产容量验收。

集成脚本核对Go结构化结果：既有四个专项的10个顶层必需用例必须实际执行、全部通过且测试包正常完成；用例缺失、顶层或子用例跳过、失败及无法解析的结果均不能报成功。普通`make check`仍允许未提供隔离依赖的集成用例跳过，不能单独作为真实中间件验收。

`/healthz` 表示进程存活，`/readyz` 表示已装配业务依赖可用。`make doctor` 连接依赖检查装配，不监听 HTTP。仅检查健康入口时可运行 `bin/server -config configs/common.yaml`。

## 5. 完整生产装配与上线流程

### 制品配置与进程

生产使用同一个 `configs/production.yaml`，包含唯一一组数据库分项配置、鉴权 Redis、微信、HTTPS 网关、一个应用 `masterkey`、外部 KMS 引用及独立删除账本路径。填写真实版本化资料；默认空秘密与未核验资源不能用于上线。网关实现见 [business.go](internal/adapters/gateway/business.go) 和 [gateway.go](internal/adapters/gateway/gateway.go)，涵盖身份检索、KMS、对象、学校规则、图片/文本分析、通知、脱敏及全副本清理。

```sh
make check-config CONFIG=configs/production.yaml
make migrate CONFIG=configs/production.yaml
make init CONFIG=configs/production.yaml
bin/migrate -config configs/production.yaml opaque-blocks
make doctor CONFIG=configs/production.yaml
make run CONFIG=configs/production.yaml
# 分别启动card/content/whispers/account/evidence，以下为其中一个进程：
make worker CONFIG=configs/production.yaml KIND=content
```

数据库账户权限不区分学生或后台岗位；请求必须经过当前会话、账号状态及岗位授权函数。普通审核不能取得危机权限，学生不能调用后台方法。DB 账号拥有本库结构以支持同 DSN 显式迁移，普通 API/worker 不自动执行 DDL。

已有多账号数据库切换时，管理员先将业务对象所有权转给应用账号，并撤销不用的历史 LOGIN；当前迁移入口不会自动修改服务器或删除旧数据库。历史版本 SQL 与校验和不改写。

## 6. 无MQ原生生产装配

当前分支已删除 MQ、Dockerfile、Compose、镜像构建和旧多进程凭据生成器；不保留兼容通道。仅构建 Go 二进制，并通过前台命令或目标机已有服务管理器管理进程。所有任务使用 PostgreSQL 到期扫描，保留租约、重试、幂等及失败待办。

删除意图存放在独立加密文件账本。API、相关 worker 必须使用相同账本路径与恢复密钥；目录权限 `0700`、文件 `0600`。账本使用跨进程锁、原子替换和 fsync，不清空已确认记录。恢复业务数据库时先运行：

```sh
bin/business-worker -config configs/production.yaml -kind account -recover
bin/business-worker -config configs/production.yaml -kind evidence -recover
```

恢复完成前不开放读取和外发。文件账本与业务库是否具有独立故障域、灾备和真实全副本删除仍需目标环境验收。本分支没有执行部署、服务管理器安装或生产迁移。

## 7. 代码组织与重构边界

工程使用单个Go module，`cmd/`的8个入口只负责参数、信号和退出状态。`internal`按职责归为10个一级目录；`modules`和`execution`只作路径分类，不包含汇总包。

```text
internal/app/                  # 服务构造、入口装配、资源生命周期
internal/config/               # 应用配置值、严格校验及稳定派生入口
internal/httpapi/              # HTTP路由、请求校验、响应映射
internal/modules/              # 账号认证、心愿、聊天、树洞、治理、资料
internal/execution/command/    # 授权复核、事务、跨域锁序和幂等
internal/execution/durable/    # 持久任务、租约、重试及恢复
internal/execution/workrunner/ # 扫描调度、取消和排空
internal/platform/             # 数据库/Redis客户端、日志、心跳
internal/platform/yamlconfig/  # 独立Viper实例、YAML读取与合并
internal/platform/securedata/  # 加密、对象读取和兼容用途派生
internal/adapters/gateway/     # 正式HTTPS供应商
internal/adapters/local/       # 本地运行适配
internal/adapters/redis/       # 业务限流接口的Redis实现
internal/entity/               # 纯ORM表模型
internal/migration/            # 显式迁移、业务模型清单和版本SQL
internal/testconfig/           # 隔离集成测试的YAML输入
```

业务包保留原包名，按实际职责使用service、query、commands、repository和worker等文件；复杂功能以crisis、capsule等前缀聚合。每个业务包的doc.go说明入口、数据归属和协作边界。资料广场与审核继续在所属包内按职责分文件。

| 查找内容 | 起点与协作 |
| --- | --- |
| 学生/后台登录、资格与材料 | modules/studentauth、adminauth、identity、studentcard、verification |
| 心愿、申请、匹配、还愿和私聊 | modules/wish、chat；审核通过与业务上架分开 |
| 树洞、暖评、抱抱、胶囊 | modules/whisper；审核和安全策略显式协作 |
| 审核、危机、处罚、申诉 | modules/moderation、safety；跨模块效果保持同事务与锁顺序 |
| 删除、脱敏、恢复前重删 | modules/privacy；独立文件账本保存删除意图 |
| 资料查询、分类、维护、反馈 | modules/resourceplaza |
| 匿名黑名单协议切换 | modules/blockpolicy；仅显式初始化切换 |

配置只依赖两个指定平台叶子包，由app显式转换领域参数和资源快照，保留可选字段、nil/空数组与复制隔离。业务接口由使用方定义，具体适配器不依赖配置或app；app负责选择实现。执行包不反向依赖具体业务模块。

完整路径白名单见[架构检查](internal/app/architecture_test.go)，逐包登记跨模块协作，禁止业务回引app、config、httpapi、adapters和migration。entity保持叶子包，65项业务模型清单位于[models.go](internal/migration/models.go)；恢复日志与探针不参与业务AutoMigrate。新增表同步模型、清单、版本SQL和中文对象注释。

宿主拥有自己打开的数据库、Redis和供应商资源，借用服务不得自行关闭；停止时先排空请求和后台任务，再逆序释放。普通启动不执行DDL。命令参数、HTTP契约、YAML字段与相对路径规则、密钥派生标签、任务协议、删除账本和恢复门禁属于兼容边界。

相关迭代使用`make check-fast PACKAGES=./internal/modules/模块/...`；完整交付执行`make fmt`、`make check`、`make build`。装配与入口变动增加Python入口测试和受影响的原生集成。`make integration`通过临时PG/Redis执行runtime、resourceplaza、moderation、migration四类专项，逐项拒绝缺失、跳过或失败的必需用例，退出清理本轮实例和数据；供应商使用合成夹具，不代表生产或真机验收。
