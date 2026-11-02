# 后端配置与启动说明

## 唯一配置入口

应用只维护 `configs/common.yaml` 和环境覆盖文件。开发指定 `configs/development.yaml`，生产指定 `configs/production.yaml`；实际覆盖文件被 Git 忽略，仓库提供对应 `.example.yaml`。所有 CLI 的 `-config` 都指向环境覆盖，公共文件固定为它旁边的 `common.yaml`。

Go入口统一通过独立的 Viper 实例读取、合并并映射到结构体；不绑定环境变量、命令行配置值或文件热更新。使用 `UnmarshalExact` 拒绝未知字段，关闭弱类型转换，单独处理带单位时长和 Base64 密钥。业务代码不直接导入 go-yaml；它仅作为 Viper 的间接依赖保留。应用不会回写源 YAML，原注释和私有值保持不变。

顺序为公共值 → 环境值。嵌套映射递归合并；标量、数组整体替换；空字符串可显式清空可选字段；`null`、重复键（包括大小写重复）、YAML 别名/合并、多文档、未知字段均拒绝。旧环境变量、JSON 配置、分角色 DSN、MQ 与文件秘密入口不再有效。输入不会展开 `$VARIABLE`，口令中的字符按原值读取。

应用只配置一个顶层 `masterkey`，填写32字节密码学随机值的 Base64。Viper读取后，程序通过 Go 标准库 HKDF-SHA256，按固定版本、环境和用途派生会话、加密、检索、回执等子密钥；派生值只在内存中使用，不写回 YAML。所有 API/worker 使用同一环境及主密钥，重启保持一致。旧 `sessionkey`、`wrapkey`、`receiptkey` 等字段已删除，继续填写会被严格校验拒绝。

`masterkey` 只管理应用自身密钥；数据库/Redis密码、微信 AppSecret、后台初始化口令、供应商令牌和外部 KMS 引用仍按原服务要求填写。KMS引用是远端密钥标识，不是另一份本地应用密钥。不同环境应使用不同主密钥；单主密钥泄漏会影响该环境的全部应用用途。主密钥、环境名和派生用途标签一旦用于持久数据，不能直接修改，否则旧密文、会话和签名不再兼容。已有数据切换必须先设计迁移；普通启动不会生成主密钥或迁移密文。

时长必须有单位，如 `5s`、`1m`、`168h`。所有相对资源/数据路径均相对于配置文件所在目录。

## 字段分组

| 配置块 | 用途 |
| --- | --- |
| `masterkey` | 唯一应用主密钥；公共配置和模板留空，实际值仅保存在私有环境覆盖 |
| `environment` / `mode` | 环境及装配模式；生产仅允许 `business`，开发可使用本地适配器 |
| `server` / `log` | HTTP 地址、读写/退出期限及日志级别 |
| `database` | 主机、端口、库名、账号、口令、TLS、时区和连接超时，以及 `maxopen` / `maxidle` / `maxlifetime` 预算 |
| `redis` | 地址、ACL 用户、内联原口令、逻辑库；生产必须鉴权 |
| `bootstrap` | 首个后台登录名与词库数据路径，仅显式初始化使用 |
| `worker` | 默认职责、开发人工/合成内容分析模式；`-kind` 选择执行哪种进程动作 |
| `development` | 初始后台口令、微信凭据、对象及删除账本路径；密钥统一由顶层派生 |
| `whispers` | 开发悄悄鲤适配；`localtest: true` 只在开发允许；生产使用下行配置 |
| `business` | 正式微信、账号、上传约束及会话期限；密钥统一由顶层派生 |
| `business.runtime` | 共享任务预算、网关令牌/内联 PEM CA、KMS 引用、资源与删除账本 |

详细中文注释直接写在 [common.yaml](configs/common.yaml)、[开发覆盖模板](configs/development.example.yaml) 和 [生产覆盖模板](configs/production.example.yaml)。`wordlists.yaml` 和 `resources.yaml` 是词库、公开资料数据，不是另一套应用参数。

## 数据库分项配置与权限

每个环境只有一组 `database` 字段，程序验证后在内存中生成唯一连接，API、任务、显式迁移/初始化使用同一个数据库账号。旧 `database.dsn` 字段不再接受；业务模块不能单独配置连接串。应用运行账号必须是专用业务库账号，不能拥有超级用户、BYPASSRLS、建库、建角色或复制权限。它拥有本库对象，以便显式迁移可以使用同一条连接。

学生与后台分域、普通审核/危机/超管岗位、本人归属、版本与幂等规则由业务服务检查；复用连接池不会跳过账号及会话授权。数据库锁函数保留事务内的当前状态复核。旧分角色 SQL 保留供历史迁移回归，新入口不创建岗位数据库角色。

```yaml
# 仅在私有环境文件填写实际值，不把示例当作可用口令。
database:
  driver: postgres
  host: 127.0.0.1          # 单个主机名或IP；IPv6不加方括号，不含端口
  port: 5432               # 1至65535的整数
  name: liyuanci           # 已创建的专用业务库
  user: liyuanci           # 拥有本库对象的普通账号
  password: '填写原口令'   # 原值填写，不做DSN/URL转义；YAML单引号用两个单引号表示
  sslmode: disable         # 本机开发；生产跨主机建议verify-full
  timezone: UTC            # 数据库会话时区，也可填Asia/Shanghai
  connecttimeout: 5s       # 1s至60s的整秒时长
  maxopen: 10              # 连接池上限，1至100
  maxidle: 2               # 空闲连接上限，0至maxopen
  maxlifetime: 30m         # 正的连接复用寿命
```

公共配置提供5432、UTC、5s及池预算；私有覆盖填写主机、库名、账号和非空口令。端口必须是整数，时长必须带单位。程序统一转义空格、单引号、反斜杠等字符，原口令不应自行做连接串转义。旧私有开发DSN已拆分，口令和主密钥保持原值。

`sslmode` 支持 `disable`、`allow`、`prefer`、`require`、`verify-ca`、`verify-full`。`allow/prefer` 可回退明文，`require` 仅加密，`verify-ca` 校验证书链，`verify-full` 还校验主机名；验证使用系统信任根，不从用户目录加载证书或口令文件。生产模板默认 `verify-full`，实际主机名须与证书匹配。删除恢复不再要求日志库 DSN，而使用独立的加密文件账本；保留历史恢复密钥和全部删除意图。旧日志库已有记录时，须先将意图转入账本并核对，不能用空账本冒充迁移完成；本次未连接或迁移生产日志库。

## 进程与动作

| 入口 | 行为 |
| --- | --- |
| `server` | 按模式装配 API；`-check-config` 离线验证，`-check` 验证依赖 |
| `business-worker -kind …` | 生产 `card/content/whispers/account/evidence` 五类任务；可 `-check-config`、`-check` 或隐私 `-recover` |
| `card-worker` / `content-worker` / `privacy-worker` | 开发适配器；`content-worker -kind whispers` 为开发树洞，隐私以 `-kind account/evidence` 选择范围 |
| `business-init` / `dev-init` | 显式初始化后台账号和检索引用；普通启动不会调用 |
| `migrate … up` | 显式版本迁移及应用账号权限设置；`permissions` 单独重放权限，`opaque-blocks` 显式切换屏蔽策略 |

常用命令由 Make 转交 `scripts/manage.py`，始终传入同一配置文件，不生成每个进程的副本：

```sh
make build
make configure                              # 已有私有文件时拒绝覆盖
# 填写私有YAML后执行：
make check-config
make migrate
make init
make run
make worker KIND=content                    # 另一个终端
make wordlists                              # API运行时显式发布词库
```

生产设置 `CONFIG=configs/production.yaml`。按需将同样命令交给宿主服务管理器，服务退出使用 SIGTERM；普通停止只结束选中的 Go 进程。完整首次启动与恢复顺序见 [README](README.md#5-完整生产装配与上线流程)。

## 本次清理与验证边界

2026-11-02 在主仓库、后端与 docs 创建 `codex/simplify-backend`，保留前序 YAML 改造的有效代码。按用户要求删除旧配置、备份、Docker/MQ 兼容入口及本地旧制品；相关 Docker 容器、网络、卷和镜像已删除。用户随后明确允许清空当前开发数据并切换单主密钥：本机私有开发覆盖已移除18个旧密钥字段，生成一个32字节随机主密钥，保留其他外部凭据；`.local/data` 中剩余两个旧对象文件已清除，无旧账本文件或本轮备份。此操作未连接或清空任何远程/生产数据库。由于 Docker 数据库已删除，开发 DSN 仍需指向操作者提供的原生 PostgreSQL；配置检查不会擅自创建数据库。

`make integration` 使用一次性原生 PG/Redis，覆盖单账号完整业务、岗位权限、持久任务、删除和重启恢复；供应商为合成 TLS 夹具。目标机启停、真实供应商、手机、容量、灾备与正式告警不属于这次通过范围。

数据库分项改造后，完整Go格式/vet/race检查、构建、三组原生集成、Python入口测试及本机私有配置离线诊断已重跑通过。配置回归覆盖旧DSN字段拒绝、主机/端口/时区/超时校验、IPv6、特殊字符口令和环境值不覆盖显式字段；原生集成同时验证UTC与Asia/Shanghai时区下的业务装配。

## 配置与业务的代码边界

`internal/config`维护YAML数据结构、字段约束和稳定密钥派生，不导入业务服务。`internal/app/config.go`将资源资料转换为领域快照，命令和任务预算也在装配时显式转换；资料的可选值与空数组含义不变。目录重构不调整任何配置字段、配置路径或密钥用途标签，现有私有YAML可继续使用。

配置错误由config自身定义，不携带原始秘密；HTTP错误仍由业务与处理器边界映射。`securedata`维护隐私回执和安全用途的兼容派生，固定向量防止实现搬移导致历史数据静默换钥。
