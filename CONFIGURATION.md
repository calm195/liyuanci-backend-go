# 后端配置与启动说明

## 唯一配置入口

应用只维护 `configs/common.yaml` 和环境覆盖文件。开发指定 `configs/development.yaml`，生产指定 `configs/production.yaml`；实际覆盖文件被 Git 忽略，仓库提供对应 `.example.yaml`。所有 CLI 的 `-config` 都指向环境覆盖，公共文件固定为它旁边的 `common.yaml`。

Go进程通过 `config.Load` 一次读取统一配置快照，装配时只投影和校验，不按运行模式重复读文件。加载器通过独立的 Viper 实例读取、合并并映射到结构体；不绑定环境变量、命令行配置值或文件热更新。使用 `UnmarshalExact` 拒绝未知字段，关闭弱类型转换，单独处理带单位时长和 Base64 密钥。业务代码不直接导入 go-yaml；它仅作为 Viper 的间接依赖保留。应用不会回写源 YAML，原注释和私有值保持不变。

顺序为公共值 → 环境值。嵌套映射递归合并；标量、数组整体替换；空字符串可显式清空可选字段；`null`、重复键（包括大小写重复）、YAML 别名/合并、多文档、未知字段均拒绝。旧环境变量、JSON 配置、分角色 DSN、MQ 与文件秘密入口不再有效。输入不会展开 `$VARIABLE`，口令中的字符按原值读取。

应用只配置一个顶层 `masterkey`，填写32字节密码学随机值的 Base64。Viper读取后，程序通过 Go 标准库 HKDF-SHA256，按固定版本、环境和用途派生会话、加密、检索、回执等子密钥；派生值只在内存中使用，不写回 YAML。所有 API/worker 使用同一环境及主密钥，重启保持一致。旧 `sessionkey`、`wrapkey`、`receiptkey` 等字段已删除，继续填写会被严格校验拒绝。

`masterkey` 只管理应用自身密钥；数据库/Redis密码、微信 AppSecret、后台初始化口令、可选外部对象服务令牌仍按服务要求填写。密钥管理和身份摘要均在本机执行，不再配置远端KMS引用；旧keyrefs/recoverykeyrefs/lookupkeyrefs字段会被拒绝。不同环境应使用不同主密钥；单主密钥泄漏会影响该环境的全部应用用途。主密钥、环境名和派生用途标签一旦用于持久数据，不能直接修改，否则旧密文、会话和签名不再兼容。已有数据切换必须先设计迁移；普通启动不会生成主密钥或迁移密文。

时长必须有单位，如 `5s`、`1m`、`168h`。所有相对资源/数据路径均相对于配置文件所在目录。

## 字段分组

| 配置块 | 用途 |
| --- | --- |
| `masterkey` | 唯一应用主密钥；公共配置和模板留空，实际值仅保存在私有环境覆盖 |
| `environment` / `mode` | 环境及装配模式；生产仅允许 `business`，开发可使用本地适配器 |
| `server` / `log` | HTTP 地址、读写/退出期限及日志级别 |
| `database` | 主机、端口、库名、账号、口令、TLS、时区和连接超时，以及 `maxopen` / `maxidle` / `maxlifetime` 预算 |
| `redis` | 地址、ACL 用户、内联原口令、逻辑库；生产必须鉴权 |
| `content` | 公开内容读限流、后台导出限流、Redis 候选缓存及可信代理 |
| `bootstrap` | 首个后台登录名与词库数据路径，仅显式初始化使用 |
| `worker` | 默认职责 `kind`；`-kind` 选择执行哪种进程动作，内容审核策略不再提供切换项 |
| `whispers` | 开发悄悄鲤适配；`localtest: true` 只在开发允许；生产使用下行配置 |
| `business` | 开发与正式环境共用的微信、后台口令、上传约束及会话期限；密钥统一由顶层派生 |
| `business.runtime` | 共享事务、腾讯云对象服务、资源文件、删除账本与隔离集成本地对象目录 |
| `business.runtime.tasks` | 扫描间隔、整次任务期限、消费者短事务和分析预算 |

详细中文注释直接写在 [common.yaml](configs/common.yaml)、[开发覆盖模板](configs/development.example.yaml) 和 [生产覆盖模板](configs/production.example.yaml)。`wordlists.yaml` 和 `resources.yaml` 是词库、公开资料数据，不是另一套应用参数。

开发与生产共用 `business` / `business.runtime` 字段，已删除独立 `development:` 配置块和 `internal/config/development.go`。旧文件须将后台口令、微信凭据移至 `business`，账本、对象目录移至 `business.runtime`，关怀资料统一放入 `resourcesfile` 指向的版本化资源文件；旧块即使为空也会被拒绝。`mode: development` 仍选择开发适配器，其 `development/*` 密钥派生标签保持不变；配置字段归组不要求清空数据或更换主密钥。

## 内容接口限流与预热

`content.readlimit` 默认每个主体每分钟 120 次，覆盖心愿、还愿、悄悄话的公开列表和详情，以及热点榜单。`content.exportlimit` 默认每分钟 12 次，后台两种导出共享独立额度。`content.window` 可调整时间窗口。Redis 短时不可用时公开读继续查询数据库，后台导出暂时返回 503。超限返回 429 和 `Retry-After`。

限流按有效会话所属账号分桶，匿名请求用来源 IP。默认只信任直连地址。经反向代理部署时，在环境覆盖的 `content.trustedproxies` 列出实际连接 API 的代理 IP 或 CIDR，代理须清理来自客户端的伪造转发头。

`content.cachettl` 默认 45 秒。API 启动后异步预热，周期补齐热点与默认公开列表的排序候选。Redis 只存定位与排序字段，正文、评论、匿名身份和可见性仍以 PostgreSQL 当前状态为准。

## 后台账号口令

在私有环境 YAML 的 `business` 下配置两个至少 12 字符的独立单行口令：

- `adminpassword` 用于首次显式初始化超级管理员，也是词库初始化脚本登录该账号时读取的口令。
- `admincreatepassword` 用于此后通过后台账号管理创建的账号，包括在页面新建的超级管理员。

升级时保留现有 `adminpassword`，添加 `admincreatepassword` 后执行 `make check-config CONFIG=配置路径`。重启 API 后，新创建的后台账号使用新字段。已有账号的密码变更通过后台改密入口完成。

## 配置项清理与层级

共享运行结构使用 `config.Runtime`，公开资料结构使用 `config.Resources`；应用装配使用 `RuntimeSettings`，不再以树洞名称表示完整业务共用参数。资源文件读取位于 `internal/config/resources.go`，运行预算与校验位于 `internal/config/runtime.go`。

| 原字段 | 当前字段或处理 |
| --- | --- |
| `runtime.catalogfile` | `runtime.resourcesfile`，承载字典、援助目录和关怀资料 |
| `runtime.worker.consumer` | `runtime.tasks.consumer`，仅控制短事务和重试 |
| `runtime.worker.analysistimeout` | `runtime.tasks.analysistimeout` |
| `runtime.interval` | `runtime.tasks.scaninterval` |
| `runtime.operationtimeout` | `runtime.tasks.operationtimeout`，须覆盖分析与前后两次事务 |
| `runtime.gatewayurl/gatewaytoken/gatewayca/providerversion` | 删除，正式对象服务统一腾讯云COS/CI |
| `runtime.contact` / `runtime.care` | 删除，关怀资料只从版本化资源文件读取，不再用一个联系方式生成资料 |
| `worker.contentanalyzer` | 删除，内容策略固定为词库筛选及命中/重审时交人工 |

此表的 `runtime` 同时适用于 `business.runtime` 和开发独立 `whispers` 块；旧字段即使为空也拒绝，不设置兼容别名。本机已有私有覆盖已迁移字段位置，主密钥、凭据与原有用途派生标签未变更，其他环境的私有文件须同步调整。`runtime.enabled` **只控制树洞与胶囊模块**，不关闭其他业务、对象服务或持久任务；`redis.enabled` 与 `tencent.enabled` 分别控制对应客户端。开发与生产示例均显式启用Redis，避免继承公共配置的关闭值。

## 数据库分项配置与权限

每个环境只有一组 `database` 字段，程序验证后在内存中生成唯一连接，API、任务、显式迁移/初始化使用同一个数据库账号。旧 `database.dsn` 字段不再接受；业务模块不能单独配置连接串。应用运行账号必须是专用业务库账号，不能拥有超级用户、BYPASSRLS、建库、建角色或复制权限。它拥有本库对象，以便显式迁移可以使用同一条连接。

学生与后台分域、普通审核/危机/超管岗位、本人归属、版本与幂等规则由业务服务检查；复用连接池不会跳过账号及会话授权。数据库锁函数保留事务内的当前状态复核。运行迁移只配置当前单账号权限，不创建岗位数据库角色；受限角色矩阵只位于隔离测试夹具，不进入运行制品。

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
| `worker -kind …` | 统一 `card/content/whispers/account/evidence` 五类任务，按YAML模式选择适配器；`-kind` 省略时读取 `worker.kind` |
| `init` | 显式初始化后台账号和检索引用；`-check-config` 离线检查；普通启动不会调用 |
| `migrate … up` | 同一事务内执行版本 SQL、字段检查和应用账号权限；`permissions` 只重放权限，`opaque-blocks` 显式切换屏蔽策略 |

`make build` 仅构建当前四个入口；既有构建目录中的旧 worker 和初始化二进制需手动清理，已有服务管理配置须改为 `worker -kind …` 或 `init`。

所有命令默认 `-config configs/development.yaml`，接收 SIGINT/SIGTERM 并在失败时返回非零状态。`server` 与 `worker` 支持 `-check-config`（离线）和 `-check`（装配依赖，不监听/消费）；`worker -kind account/evidence -recover` 仅执行删除恢复，未完成则失败退出。这三种运行模式互斥；未知职责、额外位置参数和不支持的参数均被拒绝。`init` 仅支持离线检查，`migrate` 必须指定一个操作。迁移和初始化仍为显式动作。

脚本对应参数为 `--check-config`、`--check`、`--recover`，例如：

```sh
python3 scripts/manage.py worker --kind content --check-config
python3 scripts/manage.py worker --kind account --recover
python3 scripts/manage.py init --check-config
```

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

生产设置 `CONFIG=configs/production.yaml`。按需将同样命令交给宿主服务管理器，服务退出使用 SIGTERM；普通停止只结束选中的 Go 进程。完整首次启动与恢复顺序见 [开发者指南](DEVELOPER_GUIDE.md#5-完整生产装配与上线流程)。

## 本次清理与验证边界

2026-11-02 在主仓库、后端与 docs 创建 `codex/simplify-backend`，保留前序 YAML 改造的有效代码。按用户要求删除旧配置、备份、Docker/MQ 兼容入口及本地旧制品；相关 Docker 容器、网络、卷和镜像已删除。用户随后明确允许清空当前开发数据并切换单主密钥：本机私有开发覆盖已移除18个旧密钥字段，生成一个32字节随机主密钥，保留其他外部凭据；`.local/data` 中剩余两个旧对象文件已清除，无旧账本文件或本轮备份。此操作未连接或清空任何远程/生产数据库。由于 Docker 数据库已删除，开发 DSN 仍需指向操作者提供的原生 PostgreSQL；配置检查不会擅自创建数据库。

`make integration` 使用一次性原生 PG/Redis，覆盖单账号完整业务、岗位权限、持久任务、删除和重启恢复；供应商为合成 TLS 夹具。目标机启停、真实供应商、手机、容量、灾备与正式告警不属于这次通过范围。

数据库分项改造后，完整Go格式/vet/race检查、构建、三组原生集成、Python入口测试及本机私有配置离线诊断已重跑通过。配置回归覆盖旧DSN字段拒绝、主机/端口/时区/超时校验、IPv6、特殊字符口令和环境值不覆盖显式字段；原生集成同时验证UTC与Asia/Shanghai时区下的业务装配。

## 配置与业务的代码边界

`internal/config`维护YAML数据结构、字段约束和稳定密钥派生，不导入业务服务。`internal/app/config.go`将资源资料转换为领域快照，命令和任务预算也在装配时显式转换；资料的可选值与空数组含义不变。目录重构不调整任何配置字段、配置路径或密钥用途标签，本轮外部能力简化移除了远端密钥引用字段；有此字段的私有YAML须显式移除，不在普通启动中回写。

配置错误由config自身定义，不携带原始秘密；HTTP错误仍由业务与处理器边界映射。`securedata`维护隐私回执和安全用途的兼容派生，固定向量防止实现搬移导致历史数据静默换钥。

## SQL 结构维护

项目尚未上线，初始化基线已整理为9组模块 SQL，不保留旧开发库自动升级入口。实体不再执行 AutoMigrate；普通重启不修改结构。`up` 与权限配置共用事务，`down` 只允许锁定并检查后的整体空库回退；独立资料记录及 RLS 隐藏数据也会阻止回退。字段、约束、函数和脚本归属见[迁移说明](migrations/README.md)。开发库一次性同步必须单独授权，不由启动命令隐式重置。

## 当前外部能力策略

密钥包裹和OpenID/学号摘要使用本机派生密钥；业务与恢复用途隔离。正式图片扫描使用腾讯云CI，未配置或故障失败关闭；学校权威校验直接通过，仍保留图片解码、上传上限、归属、学号占用及人工认证。公开/私密内容使用已发布词库：未命中原文通过，命中或显式重审原文人工复核；危机命中继续隔离并进入站内案件。旧synthetic/unavailable分析模式不再接受。

旧自定义对象协议及 `gatewayurl/gatewaytoken/gatewayca/providerversion` 配置已删除，正式对象读写只使用腾讯云COS，未配置时操作不可用。对象服务不承担审核、身份、通知或全副本清理。外部危机通知和重试动作已删除，站内关怀保留。账号及证据删除保留数据库清理、屏障及独立恢复账本，完成回执不涵盖外部对象和备份；COS对象及CI审核数据的保留/删除策略仍待云资源准备时确认。脱敏使用移除原文、要求重新编辑的本机策略。

## 腾讯云 COS 与数据万象 CI

在 Git 忽略的 `configs/development.yaml` 或 `configs/production.yaml` 中填写 `business.runtime.tencent`。字段位置和中文说明见公共配置及生产示例；不要将实际凭据填写到公共文件。

```yaml
business:
  runtime:
    tencent:
      enabled: false # 填写完整并准备好云资源后改为true
      bucket: '' # 完整桶名，含APPID
      region: '' # 例如ap-shanghai
      secretid: '' # 最小权限子账号
      secretkey: '' # 原密钥，仅私有文件保存
      sessiontoken: '' # 使用临时凭据时填写，过期前更新并重启
      biztype: '' # 已绑定该桶的CI审核策略
      objecturl: '' # 通常留空，使用官方COS桶域名
      imageurl: '' # 通常留空，使用官方CI桶域名
      ca: '' # 专用HTTPS代理CA完整PEM，通常留空
```

COS 使用专用私有桶，必须**从未开启版本控制**。每次上传先查询桶版本状态，开启或暂停均拒绝；`x-cos-forbid-overwrite: true` 保障同键不可覆盖。应用版本为 `sha256:<完整密文摘要>`，不是 COS versionId；重复写入与精确读取均下载限长密文并核对完整摘要，不以 ETag 或可改写元数据作为版本依据。不同密文、篡改及预算超限均失败关闭。普通启动不修改桶配置。应用账号按桶限定 `cos:GetBucketVersioning`、`cos:PutObject`、`cos:GetObject` 和 `ci:CreateAuditingPictureJob`；用桶策略强制写入携带禁止覆盖头，不授予删除对象、修改版本配置、ACL或桶管理权限。管理员也须保持版本控制关闭，并核实生命周期与保留规则。

CI 须提前开通、绑定桶并创建审核策略。worker 在事务外提交解密后的规范化 JPEG/PNG Base64，显式同步审核，不向COS写入图片明文，不发送用户身份字段。仅匹配请求标识、`State=Success` 且显式 `Result=0` 才批准；`Result=1`（违规）与 `Result=2`（疑似）均持久化为 `scan_status=rejected`，本次任务结束，提示用户重新上传；两者均不自动重试、不进入人工图片复核，不能用于提交学生认证。超时、取消、缺字段、未知状态、场景错误和HTTP失败保留现有租约、重试与耗尽转人工关注语义。未启用CI的正式装配同样失败关闭，development开发模式同样使用上述真实COS/CI，只有integration隔离测试使用本地文件和模拟扫描。

所有请求使用腾讯云官方 Go SDK 签名、HTTPS、显式超时和响应长度上限，拒绝重定向。专用代理只能通过私有YAML的HTTPS源站与内联CA显式设置。云端审核数据留存、访问权限和删除规则须另行确认；当前删除回执只覆盖数据库，不承诺清除COS对象、CI留存或备份。官方接口依据：[PUT Object](https://cloud.tencent.com/document/product/436/7749)、[图片批量审核](https://cloud.tencent.com/document/product/436/119479)。

`configs/development.example.yaml`及现有私有`configs/development.yaml`已列出腾讯云字段，开发覆盖预设`enabled: true`，填写专用开发桶/地域/凭据/BizType后再启动；占位尚未填写时校验会失败。development API和worker读取同一`business.runtime.tencent`投影，不需要切换为business模式。开发模式未启用腾讯云时对象/扫描不可用，不回退本地文件或直接通过；隔离integration测试继续使用显式合成夹具。

## 健康模式、上传与删除恢复边界

公共 `common.yaml` 默认foundation，数据库驱动留空、Redis关闭，可以直接启动健康端点；开发和生产示例分别显式启用postgres及Redis。已有业务私有覆盖若曾继承公共驱动，须补 `database.driver: postgres`；本机私有开发覆盖已补齐，原主密钥和凭据保持不变。`-check-config` 仍只校验配置，不连接或读取状态账本。

HTTP普通请求保持1MiB上限；已注册的学生卡上传路由由装配显式传入图片预算（开发4MiB，完整业务使用 `business.card.maxbytes`，默认4MiB、配置安全上限8MiB），额外预留64KiB multipart头部与边界。图片服务仍校验原图和规范化图片大小、像素、类型及唯一file部分；不能通过改变Content-Type放宽其他入口。

4MiB 学生卡部署预算：小程序上传总计 45s；Go HTTP 读取 45s、写入 55s（写入期限也覆盖读取），普通接口的 1MiB 请求体边界保持有效。生产及开发示例的共用供应商 HTTP 上限为 30s。学生卡租约 90s，扣除 5s 结果确认预算后外部阶段最多 85s；完整业务 worker 单轮上限 100s，共用该设置的其他任务也会继承该上限。覆盖旧私有 YAML 中相应的显式值，新上传统一使用 4MiB。正式 card worker 和后台审核读取保留历史 8MiB 兼容，以便处理上限调整前已受理的图片。

如由 Nginx 代理，在 `/api/v1/me/student-card-assets` 上传路径保留原有代理及安全设置，并确保 `client_max_body_size` 至少 5m，以容纳 4MiB 图片及 multipart 开销。`client_body_timeout`、`proxy_send_timeout` 和 `proxy_read_timeout` 可分别保留或设为至少 60s，避免代理提前截断。审核由后台执行，上传接口在暂存入队后返回，无需把扫描任务预算设成上传等待时间。

开发API与完整业务API共享两域历史删除门禁，扫描包含已确认意图。恢复快照带回账号或证据目标时，先运行 `worker -kind account -recover` 和 `worker -kind evidence -recover`，重删并检查通过后才启动API；只恢复一个域不能放行另一个域。普通初始化不代替恢复，不清空账本。

加密文件账本的完整JSON快照上限为32MiB（包含编码末尾换行），读取和发布使用同一边界。临时快照编码后超限则拒绝原子替换，记录 `capacity_exceeded=true` 的失败完成日志，原账本和历史意图保留；本次新增删除意图未受理，业务事务回滚，不能提示注销成功。已受理意图的确认、读取与恢复仍可继续。容量边界已用本地精确32MiB、超限一字节、确认及重启读取回归验证，这不代表目标机吞吐或灾备验收。需要更多容量时先设计保留全部历史意图、原密钥、跨进程锁和fsync语义的显式分段迁移；当前不自动分段、截断或清理历史，也不将已有损坏/超限账本当作空账本。
