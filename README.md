# 鲤源池 Go 后端

## 1. 项目介绍

Go模块化单体API与独立业务worker，模块`github.com/calm195/liyuanci-backend-go`。PostgreSQL是业务权威，Redis负责共享限流等能力，RocketMQ可显式用于学生卡及心愿内容审核唤醒。已有后台认证、学生会话、学生卡、人工认证、心愿、聊天、还愿分享及可选词库/普通内容审核与安全治理核心；真实外部供应商和部分领域消费者尚未齐备，不能作为完整生产服务。

过程记录与模块说明见[后端文档索引](../docs/development/backend/README.md)，共享接口见[契约入口](../docs/contracts/README.md)。

## 2. 项目选项与目录

| 模式 | 依赖与用途 |
| --- | --- |
| `make run` | Go 1.25+，仅健康端点，无中间件 |
| `make dev-native-*` | 宿主API/业务worker连接本地PG/Redis，无MQ时无需Docker |
| `make dev-*` | 宿主API/业务worker，本机Docker运行PG/Redis；MQ、管理工具和监控按需 |
| 完整Docker | 通过scripts启停，集成往返用`make integration` |
| 生产骨架 | 独立卷、网络和凭据；尚无完整业务发布入口，不运行开发适配 |

```text
cmd/                  进程入口及依赖装配
internal/             领域模块、HTTP、配置、平台与迁移
migrations/           隔离探针SQL；业务SQL在internal/migration/sql
configs/
  app/                公共应用配置，只保留集成模式覆盖项
  middleware/         MQ、Redis公共配置
  observability/      日志、指标、看板与告警配置
  examples/           环境文件模板，不含真实凭据
deploy/compose/       按用途组织的Compose及可复用MQ定义
scripts/              开发/运维入口与共用配置；tests/为离线单元测试
.local/               Git和Docker忽略：凭据、对象、进程状态及本机产物
```

应用配置使用`APP_`前缀，脚本使用`LIYUANCI_`。应用基础默认值由`internal/config`维护，避免重复YAML。公共配置只提交模板和必要覆盖项，实际凭据统一放`.local/<模式>/`；显式旧外部路径兼容。`.local`受Git和Docker双重忽略，目录0700，不执行环境文件内容，不输出凭据。生产环境不复用开发配置或数据。

## 3. 启动方式

优先使用已有本地PG/Redis，确保`psql`在PATH中，服务由使用者启动和管理：

```sh
make dev-native-init        # 首次显式建开发业务库、迁移、受限角色及后台账号
make dev-native-infra       # 仅检查本地PG/Redis端口
make dev-native-doctor      # 真实连接、身份和业务结构诊断
make dev-native-api         # 终端一
make dev-native-worker      # 终端二
make dev-native-stop        # 停应用，保留本地PG/Redis
make dev-native-mq          # 可选：仅Docker NameServer/Broker
make dev-native-infra-stop  # 停应用及本入口启动的MQ，本地PG/Redis不动
```

配置默认`.local/native/operations.env`，项目`liyuanci-local-native`，PG5432、Redis6379、API18080，均使用127.0.0.1。首次初始化默认以当前宿主用户名连接PG管理员，需要建角色和建库权限；日常应用使用受限身份。需自定账号、口令或端口时，先复制[原生模板](configs/examples/native.env)到上述私有目录并配置引用文件。后台默认登录名developer，随机口令在本机business.json中查看；重复初始化不会重置。

使用Docker PG/Redis：

```sh
make dev-init
make dev-infra
make dev-doctor
make dev-api
make dev-worker
make dev-stop
make dev-infra-stop
```

配置`.local/local/operations.env`，项目`liyuanci-local`；PG15432、Redis16379、API18080。`dev-mq`、`dev-management`、`dev-monitoring`按需开启，或`make dev-infra DEV_ARGS="--mq --management"`。管理页面pgAdmin15050、Redis Insight15540、MQ Dashboard18081，Grafana13000；监控只覆盖Docker日志，宿主应用日志在终端查看。原生模式只管理可选MQ，不启用管理工具或监控。

两种模式均可通过`DEV_ARGS="--project liyuanci-local-主题 --env-file /实际目录/operations.env"`选择独立配置。端口变量为`LIYUANCI_LOCAL_PG_PORT/REDIS_PORT/API_PORT/NAMESRV_PORT`；Broker10911/10909固定。`make dev-config`或`dev-native-config`刷新宿主派生文件，IDE环境见[当前开发操作说明](../docs/development/backend/host-development.md)。

学生卡worker默认仅轮询数据库。需要消息模式时，先按需启动所选项目MQ，并由该项目Broker显式建立主题`liyuanci_student_card_scan_v1`；复制[消息配置模板](configs/examples/card-worker-mq.yaml)到对应`.local/<模式>/`并核对地址、主题、组和预算。在`operations.env`中设置`LIYUANCI_LOCAL_CARD_MQ_FILE=/绝对路径/card-worker-mq.yaml`，再启动`dev-worker`或`dev-native-worker`。直接运行二进制时使用`APP_CARD_MQFILE`指定文件。消息模式同时运行发件箱转发、订阅和数据库补偿，关闭时等待在途工作后释放数据库；扫描仍是隔离开发模拟，生产拒绝该进程。诊断入口只核对数据库与对象目录，不代表MQ主题和权限可用。

首次初始化会在目标尚不存在时复制旧`~/.config/liyuanci/<模式>/`的配置、口令和对象，重写文件引用；原目录保留，已有目标绝不覆盖，旧应用运行中拒绝导入。仅迁移可执行`make dev-import`或`make dev-native-import`，不连接业务库或重置账号。旧Docker完整环境使用`.local/dev/operations.env`，`./scripts/init-dev.sh`也支持旧目录导入。

完整Docker入口仍为`./scripts/init-dev.sh`、`python3 scripts/init-business-dev.py --admin-login 自定登录名`、`./scripts/start.sh dev`和`./scripts/stop.sh dev`。普通启动不执行迁移或重置账号；停止保留卷，不操作其他项目。Compose已迁入`deploy/compose/`，手工操作需使用新路径。

基础模式监听8080，仅`/healthz`200，`/readyz`503。业务开发模式健康端点200，匿名`/api/v1/wishes`可查询，未登录`/api/v1/me`401。微信缺配置时合法登录请求503，扫描和学校规则仍为开发适配，整体`/readyz`仍503；真实供应商及生产验收缺口保持。

词库及普通内容审核路由需要显式应用业务迁移0013与权限，并由初始化补齐独立`ModerationKey`；普通启动不执行这些操作。可选装配覆盖正式0.2.0已有七项后台接口，草案的`target/allowed_actions`扩展未启用。现有数据库升级请先停止对应宿主应用，再执行所选模式的显式初始化，保留原密钥、口令和对象；缺少新结构时启动拒绝半套服务。

内容扫描核心在`internal/moderation`，需注入实际文本安全/组合信息处理器，未提供默认放行适配。扫描在事务外执行，结果/当前资格复核/领域回执原子提交；普通或涉校命中交人工，危机命中隔离，正常通过只进`ready`，上架仍由作者显式操作。日常`card-worker`继续只处理学生卡，不会自动消费心愿审核；运行`make dev-content-worker`（原生模式为`make dev-native-content-worker`）启动独立内容进程，默认仅数据库补偿，无供应商时失败关闭。正式供应商仍需接入。

隔离链路调试可在所选`operations.env`中显式设置`LIYUANCI_LOCAL_CONTENT_ANALYZER=synthetic`：此模式将正文替换为固定合成文本并清空标签，不提供隐私检测能力；生产拒绝该开发进程。规则仍必须通过后台显式创建/发布，普通/涉校/危机命中逻辑继续读取原始正文，不自动初始化词库。需MQ时，先显式建立`liyuanci_content_review_v1`主题，再复制[内容消息配置模板](configs/examples/content-worker-mq.yaml)到忽略目录，设置`LIYUANCI_LOCAL_CONTENT_MQ_FILE`；直接运行二进制对应`APP_CONTENT_ANALYZER`、`APP_CONTENT_MQFILE`。两个worker分别使用独立主题和消费组，`dev-stop`等待两者退出并保留中间件及卷。正式规则和安全分析供应商未交付时，隔离测试中的合成样例不构成生产审核验收。

## 4. 开发规范与验证

I/O首参context.Context，依赖显式注入；HTTP负责校验和状态映射，service处理规则，repository通过GORM WithContext查询。运行身份不执行DDL；迁移、RLS、锁函数和数据库中文对象注释按版本管理。日志中文、结构化、成功后记录完成，不写凭据、正文或个人数据。

```sh
make check-fast PACKAGES=./internal/相关模块/... # 日常相关用例
make fmt
make check    # 交付前只读格式、vet、race/cover
make build    # Go源码或依赖变化
python3 -m unittest discover -s scripts -p 'test_*.py' -v
# 显式提供隔离项目和测试口令后执行真实中间件往返。
make integration
```

专项验收脚本按用户要求已删除；CI保留完整Go质量检查、离线入口单测及Make直接声明的真实中间件往返。变更需要的额外验证临时执行并记录实际结果，不新增长期专项脚本。`make integration`在原探针往返后，以锁定Go构建的独立测试镜像运行真实PG持久工作/重驱动审计、学生卡领域链及内容扫描/人工复核核心；覆盖提交后NACK重投、重复、取消晚到、外部失败、过期租约恢复，以及词库发布并发、人工案件版本、隐私屏障和审计回滚。外部扫描、密钥和对象均为合成夹具，故障用例不等于真实杀进程或生产验收。该入口会迁移并清理隔离业务测试数据，仅用于独立测试项目，调用者显式维护其凭据与停止清理；不连接日常业务库或生产。

同一任务复用未固定记录，一次完整交付新增阶段记录和证据；历史记录保持只读。两仓库使用同名`codex/<主题>`分支，提交需用户授权，不自动推送、合并或部署。完整规则见[AGENTS.md](AGENTS.md)。

应愿入口随显式心愿应用装配：提交/本人列表、许愿人有效申请列表、撤销、同意及普通回退。留言先加密持久化并送审，内容worker复用同一内容主题处理心愿和应愿留言；未配置真实分析供应商时失败关闭。许愿人只收到当前有效申请的已审留言及最小公开资料，待审不能同意。匹配创建独立会话，回退重新招募时旧会话保持只读，新匹配新建会话；刷新期限需新内容版本重新审核。升级显式应用迁移0014并重放bootstrap权限，运行进程不执行DDL。

聊天与站内通知由显式`studentapi.Config.Chat`启用，并依赖心愿模块及独立用途密钥；开发装配使用64KiB正文技术字节预算，无500字符产品限制。提供原参与者会话列表、已送达消息增量读取、提交、本人状态、通知列表及版本化已读六项接口。消息审核通过且最终资格复核成功才在同一事务分配`delivery_seq`并写站内通知，空页不推进`after_seq`。原会话终结后只读，新匹配使用新会话；任一方注销立即禁止历史读取。

升级需显式应用0015并重放bootstrap权限。私聊使用独立`MessageAnalyzer`分类接口，保留主动发送的联系方式，普通违规交人工，危机隔离且普通审核不可见；缺少私聊分类器失败关闭。开发合成模式对私聊仅产生合成分类，保留原文，不能代表真实安全能力。外部微信通知尚未接入，站内通知不依赖外部发送结果。

完成、还愿与分享由`wish.ServiceConfig.MaxStoryBytes`正值显式启用，开发设64KiB技术字节预算。确认实现原子关闭会话写入并永久取消待审消息；提交故事即已还愿，公开仍需当前故事版本审核通过。改稿立即隐藏旧版，撤回不回退心愿状态。改稿/撤回的`expected_version`使用本人心愿聚合`version`，故事`content_version`独立递增；本人状态返回`story_review_result`。分享仅保存目标引用，每次打开重验当前公开资格，不保留正文快照。升级显式应用0016并重放权限；微信实机分享仍需外部联调。

安全治理通过`studentapi.Config.Safety`显式启用，提供拉黑、举报、固定等级处罚、服务器30秒警告及受限本人申诉。开发装配使用独立派生用途密钥与64KiB说明/申诉技术预算；升级须显式应用0017并重放权限，普通启动不执行DDL。举报类型来自`report_types`中运营显式发布的数据，未交付时返回空清单；不预置正式运营类型。接口与版本边界见[安全治理接入记录](../docs/development/backend/milestones/2026-10-22-safety-governance-r1.md)。

危机命中由内容worker在原事务内写入独立加密证据、隔离及关怀/授权人员通知待办。升级须显式应用0018并重放权限；后台危机路由通过`studentapi.Config.Crisis`和独立`CrisisDB`池启用，宿主开发使用私有凭据文件内可选`CrisisDSN`，登录仅属于`liyuanci_crisis_api`。普通审核池仍无证据域权限。援助资料由`WorkerConfig.Care`显式注入，开发文件可选`Care`；没有资料或`CrisisNotifier`时保留重试待办。误报创建新的普通人工复核任务，不直接恢复公开；关怀通知引用本人卡片，不暴露后台案号。接入和边界见[后端阶段记录](../docs/development/backend/milestones/2026-10-23-crisis-care-r1.md)。
