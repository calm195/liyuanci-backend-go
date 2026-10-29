# 鲤源池 Go 后端

## 1. 项目介绍

Go模块化单体API与独立业务worker，模块`github.com/calm195/liyuanci-backend-go`。PostgreSQL是业务权威，Redis负责共享限流等能力，RocketMQ可显式用于学生卡及心愿内容审核唤醒。已有后台认证、学生会话、学生卡、人工认证、心愿、聊天、还愿分享及可选词库/普通内容审核与安全治理核心；真实外部供应商和部分领域消费者尚未齐备，不能作为完整生产服务。

过程记录与模块说明见[后端文档索引](../docs/development/backend/README.md)，共享接口见[契约入口](../docs/contracts/README.md)。

功能全链路、状态流转及简化联调方式见[后端功能说明](FUNCTIONALITY.md)。

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

原文人工联调可在所选`operations.env`设置`LIYUANCI_LOCAL_CONTENT_ANALYZER=manual`，再启动`dev-content-worker`。心愿、应愿留言、私聊和还愿故事全部保留原文并强制转管理端人工复核，不自动批准；批准后使用原文候选，不提供自动隐私脱敏。三类词库仍须显式发布，已发布危机规则命中优先隔离，不进入普通审核。该模式仅限隔离开发，生产拒绝运行。

本地危机后台由`make dev-init`（原生模式为`make dev-native-init`）显式补齐独立`dev_crisis_api`登录及`CrisisDSN`，复用已有凭据且不重置后台账号。初始化后重启API，具有`crisis_operator`或`super_admin`角色的账号可联调`/api/v1/admin/crisis-cases`及人工升级、处置、误报回退接口；普通启动只转换宿主连接地址，不创建身份或迁移。`manual`模式不提供真实危机识别，本地不配置正式关怀资料或真实通知，相关投递保留待办。详见[危机后台本地联调](../docs/development/backend/host-development.md#危机案件与关怀)。

### 本地业务测试模式

已有业务环境完成0026、权限、测试词库与opaque切换后，可运行 `make dev-stop`、`make dev-business-init`、`make dev-doctor` 显式启用单DSN与进程内人工审核适配。API和旧worker仍用原入口，另运行 `make dev-whispers-worker`；账号/证据worker使用相同树洞配置。密钥随机生成一次并复用，配置和日志保存在.local，联系方式可留空；不要求HTTPS网关、外部KMS或正式资料。MQ可复用现有Broker，初始树洞仅用数据库轮询。

该模式只允许integration，完整边界、重启及配置说明见[功能说明](FUNCTIONALITY.md#14-本地业务测试模式)。不代表外部通知、学生卡全版本清理或独立灾备验收。原配置保留，停止不删除数据卷。

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

专项验收脚本按用户要求已删除；CI保留完整Go质量检查、离线入口单测及Make直接声明的真实中间件往返。变更需要的额外验证临时执行并记录实际结果，不新增长期专项脚本。`make integration`在原探针往返后，以锁定Go构建的独立测试镜像运行真实PG迁移与树洞资源锁、持久工作/重驱动审计、学生卡领域链及内容扫描/人工复核核心；覆盖提交后NACK重投、重复、取消晚到、外部失败、过期租约恢复，以及词库发布并发、人工案件版本、隐私屏障和审计回滚。外部扫描、密钥和对象均为合成夹具，故障用例不等于真实杀进程或生产验收。该入口会迁移并清理隔离业务测试数据，仅用于独立测试项目，调用者显式维护其凭据与停止清理；不连接日常业务库或生产。

同一任务复用未固定记录，一次完整交付新增阶段记录和证据；历史记录保持只读。后端源码使用`develop`，docs使用`backend-develop`分支，提交需用户授权，不自动推送、合并或部署。完整规则见[AGENTS.md](AGENTS.md)。

应愿入口随显式心愿应用装配：提交/本人列表、许愿人有效申请列表、撤销、同意及普通回退。留言先加密持久化并送审，内容worker复用同一内容主题处理心愿和应愿留言；未配置真实分析供应商时失败关闭。许愿人只收到当前有效申请的已审留言及最小公开资料，待审不能同意。匹配创建独立会话，回退重新招募时旧会话保持只读，新匹配新建会话；刷新期限需新内容版本重新审核。升级显式应用迁移0014并重放bootstrap权限，运行进程不执行DDL。

聊天与站内通知由显式`studentapi.Config.Chat`启用，并依赖心愿模块及独立用途密钥；开发装配使用64KiB正文技术字节预算，无500字符产品限制。提供原参与者会话列表、已送达消息增量读取、提交、本人状态、通知列表及版本化已读六项接口。消息审核通过且最终资格复核成功才在同一事务分配`delivery_seq`并写站内通知，空页不推进`after_seq`。原会话终结后只读，新匹配使用新会话；任一方注销立即禁止历史读取。

升级需显式应用0015并重放bootstrap权限。私聊使用独立`MessageAnalyzer`分类接口，保留主动发送的联系方式，普通违规交人工，危机隔离且普通审核不可见；缺少私聊分类器失败关闭。开发合成模式对私聊仅产生合成分类，保留原文，不能代表真实安全能力。外部微信通知尚未接入，站内通知不依赖外部发送结果。

完成、还愿与分享由`wish.ServiceConfig.MaxStoryBytes`正值显式启用，开发设64KiB技术字节预算。确认实现原子关闭会话写入并永久取消待审消息；提交故事即已还愿，公开仍需当前故事版本审核通过。改稿立即隐藏旧版，撤回不回退心愿状态。改稿/撤回的`expected_version`使用本人心愿聚合`version`，故事`content_version`独立递增；本人状态返回`story_review_result`。分享仅保存目标引用，每次打开重验当前公开资格，不保留正文快照。升级显式应用0016并重放权限；微信实机分享仍需外部联调。


公开主页 `GET /api/v1/users/{id}/public-profile` 随心愿应用启用，按公开时间与ID分页返回当前可见心愿及已审故事；故事部分依赖显式启用故事模块。升级前停止对应应用，由专用迁移身份显式应用0020并重放权限，再重启API和内容worker；普通启动不执行DDL。历史已审故事使用迁移切换时间作为排序起点，后续版本在审核批准事务内记录公开时间。主页资料只含昵称与标识，身份标签按每条已审内容及当前隐藏设置返回。

安全治理通过`studentapi.Config.Safety`显式启用，提供拉黑、举报、固定等级处罚、服务器30秒警告及受限本人申诉。开发装配使用独立派生用途密钥与64KiB说明/申诉技术预算；升级须显式应用0017并重放权限，普通启动不执行DDL。举报类型来自`report_types`中运营显式发布的数据，未交付时返回空清单；不预置正式运营类型。黑名单在显式匿名切换前兼容旧条目版本；切换后使用随机条目接口，旧接口仅返回升级错误，Me移除旧动作。接口与版本边界见[安全治理接入记录](../docs/development/backend/milestones/2026-10-22-safety-governance-r1.md)。

危机命中由内容worker在原事务内写入独立加密证据、隔离及关怀/授权人员通知待办。升级须显式应用0018并重放权限；后台危机路由通过`studentapi.Config.Crisis`和独立`CrisisDB`池启用，宿主开发使用私有凭据文件内可选`CrisisDSN`，登录仅属于`liyuanci_crisis_api`。普通审核池仍无证据域权限。援助资料由`WorkerConfig.Care`显式注入，开发文件可选`Care`；没有资料或`CrisisNotifier`时保留重试待办。误报创建新的普通人工复核任务，不直接恢复公开；关怀通知引用本人卡片，不暴露后台案号。接入和边界见[后端阶段记录](../docs/development/backend/milestones/2026-10-23-crisis-care-r1.md)。

注销及证据删除由`internal/privacy`提供：先持久独立恢复日志，再原子撤销会话、设置公开屏障并处理存活方关系；清理失败保持`processing`，匿名回执不含身份或正文。显式应用0019并重放权限后，通过`studentapi.Config.Privacy/EvidenceDeletion`及独立运行池启用；普通启动不迁移。宿主开发凭据可选`PrivacyDSN/EvidenceDeletionDSN/EvidencePurgerDSN/RecoveryWriterDSN/RecoveryReaderDSN/RecoveryWrapKey`，日志包裹密钥必须独立，初始化不创建这些资源或替换旧密钥。`make build`同时构建`privacy-worker`；直接运行时显式提供`APP_ENVIRONMENT=integration`、`APP_DEVAPPFILE`和`APP_PRIVACY_SCOPE=account`或`evidence`。此开发入口未配置外部全版本清理及脱敏适配器，只维持持久待办；正式宿主须注入`privacy.Eraser`、`privacy.Redactor`和独立日志后调用`Service.Run`，不能把开发进程用于生产。接入、恢复重删及验收边界见[隐私清理阶段记录](../docs/development/backend/milestones/2026-10-23-privacy-deletion-r1.md)。

本地隐私注销与证据删除需先 `make dev-stop`，再显式运行 `make dev-privacy-init`（Docker宿主模式），初始化独立恢复 PostgreSQL、专用身份和用途密钥。随后分别运行 `make dev-api`、`make dev-worker`、`make dev-content-worker`、`make dev-privacy-worker` 与 `make dev-evidence-worker`；五个进程均由 `make dev-stop` 停止，中间件与卷保留。恢复库端口默认仅绑定127.0.0.1:15433。初始化后 `make dev-infra` 同时启动恢复库，普通启动不执行迁移。缺少外部全版本清理或脱敏适配器时，隐私任务保持processing，不声称清理完成。配置和联调范围见[宿主开发说明](../docs/development/backend/host-development.md)。

树洞基础通过显式迁移0021建立独立实体、内容约束和最小权限，0022允许审核任务保存树洞初始零限制代次，0023保留业务生命周期内的最小幂等回执；旧黑名单关系原位生成不透明`block_id`。升级此版本的业务API／worker前须显式应用至0026并重放权限；迁移由专用身份执行，普通启动不执行DDL。`internal/whisper`提供文字草稿、三档身份、昵称、公开／本人查询及送审边界，`httpapi.RegisterWhispers`只供显式依赖装配；当前进程尚未启用树洞，正式依赖装配与供应商验收完成前不得启用。缺字典时半匿名不可用，缺援助资料返回unavailable；送审必须注入支持新类型的持久审核适配，不能交给旧消费者。Redis限频需注入独立用途密钥并失败关闭；取消／撤回独立于发布预算。`make integration`包含隔离子库迁移、权限、资源锁、文字事务与公开遮蔽、Redis滚动预算验证；外部审核、字典及KMS测试夹具不代表生产验收。

树洞审核使用独立的 `moderation.WhisperReviewSink/WhisperWorker` 和 `WhisperReviewRoute`；公开分析器显式区分帖子、暖评与昵称，胶囊使用独立私密分类器，不接受公开脱敏正文替换。必须显式注入三类已发布词库、安全分析与关怀／通知供应商。`httpapi.RegisterWhisperGovernance` 分别注入普通后台、危机后台、学生举报和独立证据删除池；0024增加受限原稿读取函数、危机来源快照与删除回执命名空间，普通后台不能整表读取胶囊或昵称原稿。默认 API 和内容进程尚未注册这些能力；完整删除图已接入独立隐私服务，生产装配仍需后续实现。升级须由迁移身份显式应用至0026并重放bootstrap权限，不能仅替换二进制；隔离验证依赖真实PostgreSQL，完整 `make integration` 另需Redis和RocketMQ，分析、KMS及通知使用合成夹具。

暖评、抱抱和本模块通知通过 `httpapi.RegisterWhisperInteractions` 显式注册，需与文字及审核服务共用账号 guard 和父帖锁协议。暖评允许未实名正常账号；改稿重新绑定父版本和评论代次，关闭评论保留既有公开评论。抱抱使用独立版本和取消记录，计数由 PostgreSQL 过滤注销／受罚主体；评论首次公开在批准事务内产生唯一站内通知，旧通知入口隔离新类型。部署此适配需显式重放 `permissions`，授予人工批准通知引用及学生改稿清除公开时间的最小权限；互动结构建立于0024及以前迁移。当前业务进程仍未启用树洞。

本人时空胶囊通过 `httpapi.RegisterWhisperCapsules` 显式注册五个操作；仅正常账号可封存，受罚本人仍可读取合法到期内容和清理。创建与列表不返回正文，详情依数据库时间、安全批准及当前屏障决定解密。`whisper.NewCapsuleUnlockWorker` 复用持久消费、完成回执和有界补偿，不解密正文；到期未审的工作持久挂起，批准唤醒或补偿后生成一次站内通知。迁移0025允许仅胶囊解锁工作使用waiting，存在挂起工作时拒绝回退；升级须显式迁移并重放权限。取消／删除清普通密文、工作及普通通知，保留最小终态及独立危机证据。真实文本安全、外部清理及生产进程装配验收完成前保持入口关闭。

匿名黑名单通过 `httpapi.RegisterInteractionBlocks` 显式注册；迁移0026建立独立单向策略，默认不切换。完成两端升级后，使用迁移身份和外部 `APP_DATABASE_DSN` 显式运行 `go run ./cmd/migrate opaque-blocks`，验证历史随机标识并切换。切换后旧 GET／POST／DELETE 黑名单经认证统一返回 `409 CLIENT_UPGRADE_REQUIRED`，新接口只返回随机条目或最小受理确认；关闭树洞也不回退，已切换数据库拒绝 down。先部署迁移与兼容代码，再切换，最后完成正式装配验收才可启用树洞；前端回滚须保持服务端匿名保护。

隐私服务沿用独立 `liyuanci_privacy_maintenance` 身份、独立恢复日志和显式 `Redactor`／`Eraser`：注销受理先撤销会话、隐藏整串并取消审核／解锁／通知。帖主的他人评论保持隐私屏障；受控脱敏及外部清理确认后才提供本人恢复候选，原 body 与旧父帖标识始终隐藏，重新发布使用新评论。缺少真实适配器时保持 processing。恢复调度必须先调用 `privacy.Service.Recover` 读取包含已确认删除的独立日志并完成重删，再开放读取及外发；普通启动不替代此恢复门禁，也不宣称完成备份全副本清理。

树洞正式依赖装配由 `APP_WHISPERSFILE` 指向仓库 `.local` 忽略目录或外部私有 JSON 文件，模板为 `configs/examples/whispers-runtime.json`；所有空凭据／密钥／资源必须显式补齐，期限使用纳秒整数，七个用途密钥为互不相同的32字节Base64值。`KeyRefs` 与 `RecoveryKeyRefs` 分别列出业务及恢复KMS允许的当前和历史引用，首项用于新包裹；不得生成替代历史密钥。API只打开API／普通后台／危机／隐私／证据删除五个独立运行池和恢复日志池；内容进程使用domain-worker，隐私进程按scope使用privacy-maintenance或evidence-purger。每个进程私有文件只需提供自身使用的业务DSN，恢复读写池与公共约束必须显式提供。

`server -config <应用YAML>` 读取 `whispersfile`（环境覆盖为 `APP_WHISPERSFILE`），支持 `-check` 只装配与诊断而不监听；应用YAML仍显式设置environment、数据库driver、Redis及可选MQ。仅树洞组件运行时复用数据库中的有效学生／后台会话，登录仍由既有身份入口提供；不会生成模板账号或模拟登录。新旧API实例应使用同一版本的会话与Me投影。宿主已装配的安全和注销路由不重复注册；树洞新增50操作整体注册。基础 `make run` 不读取该文件，仍只提供健康端点。

`content-worker` 在设置 `APP_WHISPERSFILE` 时启动树洞审核、胶囊解锁及危机补偿，未设置时保持原开发内容入口。审核／解锁的MQ文件分别使用 `configs/examples/whispers-review-mq.yaml`、`whispers-unlock-mq.yaml` 的独立topic／group，主题必须由运维提前建立；运行进程不创建主题。仅隔离integration可显式 `DatabasePolling=true` 且不设置MQ文件；生产不接受无鉴权MQ或数据库轮询。旧内容进程与树洞进程分别消费各自领域，危机投递按来源隔离；关闭模块仍可处理已持久工作与取消，不清除任务或幂等历史。

`privacy-worker` 使用同一显式入口及 `APP_PRIVACY_SCOPE=account|evidence`；`APP_PRIVACY_RECOVER=true` 先回放所有独立日志（含已确认删除）并重删，只有待办全部完成且目标不存在才成功退出。恢复流程为：隔离读取和外发→分别完成账号／证据恢复清理→API和内容进程启动门禁通过→再由运维开放访问。冷启动发现日志目标仍存在时拒绝启动，普通清理中断也遵守此门禁；失败后由隐私进程继续重试，不手工删日志绕过。90天备份与真实全版本清理仍须由正式供应商验收。

供应商网关必须是经系统CA或显式 `GatewayCAFile` 验证的HTTPS地址，禁止重定向；令牌从 `GatewayTokenFile` 读取。请求为 `{Operation,Version,Input}` JSON；health校验ProviderVersion及能力；wrap／unwrap限定scope和允许密钥引用；public_analysis返回公开安全候选，private_classify只接受分类；redact返回幸存作者安全内容；erase必须同时确认Completed与WriteBarrierConfirmed；notify按DeliveryID去重。网关协议是本端私有适配边界，未修改三端固定契约。不得记录网关请求／响应、原稿、密钥、DSN或令牌。

`CatalogFile` 为版本化公共资源 `{Version,Catalog:{Grades,Reports,Help},Care}`，使用领域相同字段；援助与关怀须由资料方提供来源、版本和核验日期，不提供模板联系方式。API每次读取当前资料，坏版本失败关闭；内容进程启动核验关怀资料。正式外部依赖模式启用前须已完成0026和permissions、发布三类词库、执行opaque单向切换、提供全部正式依赖并完成恢复清理；本地业务测试模式使用[功能说明](FUNCTIONALITY.md#14-本地业务测试模式)中的简化适配。当前生产 `Enabled=true` 会被拒绝，所有模板保持false；本地或隔离通过不表示生产启用或三端验收。
