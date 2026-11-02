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

工程使用单个Go module。`cmd/`保存8个进程入口，`internal/httpapi`负责HTTP协议，`internal`下的业务包负责领域规则；`command`、`durable`、`securedata`提供事务/幂等、持久任务与加密能力。测试随源码放置，`configs`与`scripts`分别维护配置和原生运维入口。

目录优化已完成适配器分离、应用装配集中与配置依赖解耦。正式网关已放在`adapters/gateway`，本地供应商放在`adapters/local`，适配器不再依赖应用配置或装配。应用装配已统一由`internal/app`协调，按服务构造、正式/开发装配、API、worker、初始化和迁移职责分文件；8个`cmd`入口只保留参数、信号和退出状态。配置层使用独立的命令/任务预算与资源数据结构，只依赖yamlconfig、securedata；app显式转换为领域参数和资源快照。业务构造器继续复核准入，资源转换保留可选字段、nil/空数组区别，并复制指针与切片。稳定派生由securedata共享，标签与固定测试向量保持兼容。

当前主要目录：

```text
cmd/                       # 8个薄入口：参数、信号、退出状态
internal/app/              # 服务构造、运行装配、资源生命周期和集成测试
internal/adapters/gateway/ # 正式HTTPS供应商协议
internal/adapters/local/   # 明确隔离的本地供应商
internal/config/           # 独立配置值、严格校验和稳定派生
internal/yamlconfig/       # Viper YAML读取与合并
internal/httpapi/          # HTTP路由、协议校验与状态映射
internal/platform/         # 数据库/Redis连接、日志及心跳
internal/<业务模块>/       # 认证、心愿、聊天、悄悄鲤、审核、安全、隐私、资料
internal/command/          # 事务、锁、幂等
internal/durable/          # 持久任务
internal/workrunner/       # 任务扫描生命周期
internal/securedata/       # 加密、对象读取和兼容用途派生
internal/entity/           # 按模块分文件的纯ORM表模型
internal/migration/        # 业务模型注册、显式版本SQL与实体同步
configs/                   # 公共配置、私有环境覆盖及种子资料
scripts/                   # 原生运行、配置和临时集成入口
```

资料广场在同一包内按授权事务、游标、加密、查询、写入、投影、存取和治理屏障分文件；审核模块抽出共享授权和游标，其余保留已有功能文件。共享类型和方法仍在所属业务包内，避免为文件大小创建无必要的子包。HTTP路由入口保持现有协议边界。

实体是跨域锁和业务存取共享的底层模型，继续按模块分文件，不拆到业务服务包内。65项业务模型注册清单归属`internal/migration/models.go`，实体与SQL映射、注释和隔离表排除检查随清单维护；增加表时同时更新模型、清单和版本SQL。恢复日志与集成探针不参与业务AutoMigrate。

迁移保持8个命令名称及参数、HTTP路由和响应、YAML字段与相对路径规则、密钥用途标签、任务租约/重试/取消、删除恢复门禁不变。版本SQL及其校验和不改写，普通启动不执行DDL。

资源关闭遵循所有权：宿主先停止HTTP和后台调度，等待在途操作结束，再释放自己打开的数据库、Redis和供应商连接；借用资源的服务不得自行关闭。新建包和导出符号使用中文注释说明职责，涉及恢复、密钥兼容及失败关闭的逻辑保留决策依据。

每阶段先检查相关包，完整交付执行`make fmt`、`make check`、`make build`；装配或入口变化额外运行Python测试和`make integration`。集成脚本中的Go测试包路径须随装配测试同步迁移，临时PG/Redis的清理边界保持不变。

`internal/app/architecture_test.go`约束cmd仅依赖标准库和app，以及业务、HTTP、平台和适配器的导入方向，也防止业务导入迁移包、模型反向依赖服务，纳入`make check`。`make integration`顺序执行完整业务、本地组合、独立树洞三组进程测试，以及资料广场15个操作、审核核心和迁移专项。专项入口为`make integration INTEGRATION_SUITE=runtime`、`resourceplaza`、`moderation`或`migration`，共用临时原生PG/Redis清理流程。迁移专项为各上下行/升级场景创建独立空库，检查并发迁移、历史升级、表字段注释及恢复库隔离。历史权限与恢复库夹具仅验证兼容性，当前单连接配置、文件账本与岗位隔离由runtime验证；外部供应商仍为合成测试实现。
