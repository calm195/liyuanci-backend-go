# 鲤源池 Go 后端

## 1. 项目介绍

Go模块化单体API与独立业务worker，模块`github.com/calm195/liyuanci-backend-go`。PostgreSQL是业务权威，Redis负责共享限流等能力，RocketMQ可显式用于学生卡及心愿内容审核唤醒。已有后台认证、学生会话、学生卡、人工认证、心愿、聊天、还愿分享、悄悄鲤及安全治理。完整生产宿主已装配API和五类worker，正式供应商、运营资料、目标环境与真机验收仍须完成后才能上线。

后台安全治理装配同时提供普通用户列表、详情及处罚历史三个只读GET，支持普通昵称包含搜索、状态筛选和受保护键集游标。使用前由专用迁移入口应用0027并刷新权限；普通启动不执行DDL。用户页使用脱敏投影，处罚与申诉仍从授权案件工作台执行，接入和验证边界见[后端文档索引](../docs/development/backend/README.md)。

资料广场通过 `studentapi.Config.Resources` 显式启用；完整开发应用和业务运行装配提供15个接口。使用前显式应用0029迁移并刷新权限，启动只检查结构。学生须认证且未封禁，后台限运营/超级管理员；链接和提取码加密，四类反馈随换版及注销清理，外部网盘不会被服务端访问。

现状、缺陷与后续计划见[后端文档索引](../docs/development/backend/README.md)，共享接口见[契约入口](../docs/contracts/README.md)。

功能全链路、状态流转及简化联调方式见[后端功能说明](FUNCTIONALITY.md)。

## 2. 项目选项与目录

| 模式 | 依赖与用途 |
| --- | --- |
| `make run` | Go 1.25+，仅健康端点，无中间件 |
| `make dev-native-*` | 宿主API/业务worker连接本地PG/Redis，无MQ时无需Docker |
| `make dev-*` | 宿主API/业务worker，本机Docker运行PG/Redis；MQ、管理工具和监控按需 |
| 完整Docker | 通过scripts启停，集成往返用`make integration` |
| 完整生产装配 | `APP_BUSINESSFILE`、独立角色/恢复日志、HTTPS供应商、鉴权Redis/MQ；悄悄鲤纳入发布，缺依赖拒绝启动 |

```text
cmd/                  进程入口及依赖装配
internal/             领域模块、HTTP、配置、平台与迁移
migrations/           隔离探针SQL；业务SQL在internal/migration/sql
configs/
  app/                公共应用配置，含集成覆盖和生产模板
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

两种模式均可通过`DEV_ARGS="--project liyuanci-local-主题 --env-file /实际目录/operations.env"`选择独立配置。端口变量为`LIYUANCI_LOCAL_PG_PORT/REDIS_PORT/API_PORT/NAMESRV_PORT`；Broker10911/10909固定。`make dev-config`或`dev-native-config`刷新宿主派生文件，IDE环境见[本地业务入口](FUNCTIONALITY.md#14-本地业务测试模式)。

学生卡worker默认仅轮询数据库。需要消息模式时，先按需启动所选项目MQ，并由该项目Broker显式建立主题`liyuanci_student_card_scan_v1`；复制[消息配置模板](configs/examples/card-worker-mq.yaml)到对应`.local/<模式>/`并核对地址、主题、组和预算。在`operations.env`中设置`LIYUANCI_LOCAL_CARD_MQ_FILE=/绝对路径/card-worker-mq.yaml`，再启动`dev-worker`或`dev-native-worker`。直接运行二进制时使用`APP_CARD_MQFILE`指定文件。消息模式同时运行发件箱转发、订阅和数据库补偿，关闭时等待在途工作后释放数据库；扫描仍是隔离开发模拟，生产拒绝该进程。诊断入口只核对数据库与对象目录，不代表MQ主题和权限可用。

首次初始化会在目标尚不存在时复制旧`~/.config/liyuanci/<模式>/`的配置、口令和对象，重写文件引用；原目录保留，已有目标绝不覆盖，旧应用运行中拒绝导入。仅迁移可执行`make dev-import`或`make dev-native-import`，不连接业务库或重置账号。旧Docker完整环境使用`.local/dev/operations.env`，`./scripts/init-dev.sh`也支持旧目录导入。

完整Docker入口仍为`./scripts/init-dev.sh`、`python3 scripts/init-business-dev.py --admin-login 自定登录名`、`./scripts/start.sh dev`和`./scripts/stop.sh dev`。普通启动不执行迁移或重置账号；停止保留卷，不操作其他项目。Compose已迁入`deploy/compose/`，手工操作需使用新路径。

基础模式监听8080，仅`/healthz`200，`/readyz`503。业务开发模式健康端点200，匿名`/api/v1/wishes`可查询，未登录`/api/v1/me`401。微信缺配置时合法登录请求503，扫描和学校规则仍为开发适配，整体`/readyz`仍503；真实供应商及生产验收缺口保持。

词库及普通内容审核路由需要显式应用业务迁移0013与权限，并由初始化补齐独立`ModerationKey`；普通启动不执行这些操作。可选装配覆盖正式0.2.0已有七项后台接口，草案的`target/allowed_actions`扩展未启用。现有数据库升级请先停止对应宿主应用，再执行所选模式的显式初始化，保留原密钥、口令和对象；缺少新结构时启动拒绝半套服务。

内容扫描核心在`internal/moderation`，先读取三类已发布词库的一致快照；未命中且不是隐私/危机重审时，保留原文和显式标签自动通过，不调用分析供应商。命中或重审继续调用原文本安全/组合信息处理器；缺词库或该分支供应商故障仍失败关闭。扫描在事务外执行，结果/当前资格复核/领域回执原子提交；普通或涉校命中交人工，危机命中隔离，正常通过只进`ready`，上架仍由作者显式操作。日常`card-worker`继续只处理学生卡，不会自动消费心愿审核；运行`make dev-content-worker`（原生模式为`make dev-native-content-worker`）启动独立内容进程，默认仅数据库补偿，命中或重审时无供应商失败关闭。正式供应商仍需接入。

隔离链路调试可在所选`operations.env`中显式设置`LIYUANCI_LOCAL_CONTENT_ANALYZER=synthetic`：此模式在词库命中或重审的分析分支将正文替换为固定合成文本并清空标签，不提供隐私检测能力；生产拒绝该开发进程。规则仍必须通过后台显式创建/发布，普通/涉校/危机命中逻辑继续读取原始正文，不自动初始化词库。需MQ时，先显式建立`liyuanci_content_review_v1`主题，再复制[内容消息配置模板](configs/examples/content-worker-mq.yaml)到忽略目录，设置`LIYUANCI_LOCAL_CONTENT_MQ_FILE`；直接运行二进制对应`APP_CONTENT_ANALYZER`、`APP_CONTENT_MQFILE`。两个worker分别使用独立主题和消费组，`dev-stop`等待两者退出并保留中间件及卷。正式规则和安全分析供应商未交付时，隔离测试中的合成样例不构成生产审核验收。

原文人工联调可在所选`operations.env`设置`LIYUANCI_LOCAL_CONTENT_ANALYZER=manual`，再启动`dev-content-worker`。心愿、应愿留言、私聊和还愿故事未命中自动通过；命中或重审保留原文并交管理端人工复核，批准后使用原文候选，不提供自动隐私脱敏。三类词库仍须显式发布，私聊涉校词只转人工不生成公开标签，已发布危机规则命中优先隔离，不进入普通审核。该模式仅限隔离开发，生产拒绝运行。

本地危机后台由`make dev-init`（原生模式为`make dev-native-init`）显式补齐独立`dev_crisis_api`登录及`CrisisDSN`，复用已有凭据且不重置后台账号。初始化后重启API，具有`crisis_operator`或`super_admin`角色的账号可联调`/api/v1/admin/crisis-cases`及人工升级、处置、误报回退接口；普通启动只转换宿主连接地址，不创建身份或迁移。`manual`模式不提供真实危机识别，本地不配置正式关怀资料或真实通知，相关投递保留待办。详见[危机后台本地联调](../docs/development/backend/README.md)。


常见词库可显式执行 `make dev-wordlists-init`，默认连接本机18080，私有账号口令读取 `.local/local/business.host.json`，种子见 `configs/examples/moderation-wordlists.json`；自定义路径使用 `WORDLIST_ARGS="--business-file /实际私有路径 --api-url http://127.0.0.1:18080 --login 实际登录名"`。此操作通过真实后台认证及完整快照创建/发布，合并保留当前生效词，不重写旧快照；重复执行无新增词时不再发布，并发代次冲突退出后由操作者重试。种子只提供常见起点，危机求助优先隔离进入关怀流程，不等同于普通违规拒绝，不替代正式词库维护。普通启动不隐式初始化或发布词库。

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

docs本端仅滚动维护现状、缺陷与后续计划；历史阶段材料从Git快照追溯，不再新增阶段报告或日志目录。源码与docs的本地和远程仅保留`develop`、`main`，日常开发均使用`develop`；提交需用户授权，不自动推送、合并或部署。完整规则见[AGENTS.md](AGENTS.md)。

应愿入口随显式心愿应用装配：提交/本人列表、许愿人有效申请列表、撤销、同意及普通回退。留言先加密持久化并送审，内容worker复用同一内容主题处理心愿和应愿留言；未配置真实分析供应商时失败关闭。许愿人只收到当前有效申请的已审留言及最小公开资料，待审不能同意。匹配创建独立会话，回退重新招募时旧会话保持只读，新匹配新建会话；刷新期限需新内容版本重新审核。升级显式应用迁移0014并重放bootstrap权限，运行进程不执行DDL。

聊天与站内通知由显式`studentapi.Config.Chat`启用，并依赖心愿模块及独立用途密钥；开发装配使用64KiB正文技术字节预算，无500字符产品限制。提供原参与者会话列表、已送达消息增量读取、提交、本人状态、通知列表及版本化已读六项接口。消息审核通过且最终资格复核成功才在同一事务分配`delivery_seq`并写站内通知，空页不推进`after_seq`。原会话终结后只读，新匹配使用新会话；任一方注销立即禁止历史读取。

升级需显式应用0015并重放bootstrap权限。私聊使用独立`MessageAnalyzer`分类接口，保留主动发送的联系方式，普通违规交人工，危机隔离且普通审核不可见；缺少私聊分类器失败关闭。开发合成模式对私聊仅产生合成分类，保留原文，不能代表真实安全能力。外部微信通知尚未接入，站内通知不依赖外部发送结果。

完成、还愿与分享由`wish.ServiceConfig.MaxStoryBytes`正值显式启用，开发设64KiB技术字节预算。确认实现原子关闭会话写入并永久取消待审消息；提交故事即已还愿，公开仍需当前故事版本审核通过。改稿立即隐藏旧版，撤回不回退心愿状态。改稿/撤回的`expected_version`使用本人心愿聚合`version`，故事`content_version`独立递增；本人状态返回`story_review_result`。分享仅保存目标引用，每次打开重验当前公开资格，不保留正文快照。升级显式应用0016并重放权限；微信实机分享仍需外部联调。


公开主页 `GET /api/v1/users/{id}/public-profile` 随心愿应用启用，按公开时间与ID分页返回当前可见心愿及已审故事；故事部分依赖显式启用故事模块。升级前停止对应应用，由专用迁移身份显式应用0020并重放权限，再重启API和内容worker；普通启动不执行DDL。历史已审故事使用迁移切换时间作为排序起点，后续版本在审核批准事务内记录公开时间。主页资料只含昵称与标识，身份标签按每条已审内容及当前隐藏设置返回。

安全治理通过`studentapi.Config.Safety`显式启用，提供拉黑、举报、固定等级处罚、服务器30秒警告及受限本人申诉。开发装配使用独立派生用途密钥与64KiB说明/申诉技术预算；升级须显式应用0017并重放权限，普通启动不执行DDL。举报类型来自`report_types`中运营显式发布的数据，未交付时返回空清单；不预置正式运营类型。黑名单在显式匿名切换前兼容旧条目版本；切换后使用随机条目接口，旧接口仅返回升级错误，Me移除旧动作。接口与版本边界见[安全治理现状](../docs/development/backend/README.md)。

危机命中由内容worker在原事务内写入独立加密证据、隔离及关怀/授权人员通知待办。升级须显式应用0018并重放权限；后台危机路由通过`studentapi.Config.Crisis`和独立`CrisisDB`池启用，宿主开发使用私有凭据文件内可选`CrisisDSN`，登录仅属于`liyuanci_crisis_api`。普通审核池仍无证据域权限。援助资料由`WorkerConfig.Care`显式注入，开发文件可选`Care`；没有资料或`CrisisNotifier`时保留重试待办。误报创建新的普通人工复核任务，不直接恢复公开；关怀通知引用本人卡片，不暴露后台案号。接入和边界见[后端现状](../docs/development/backend/README.md)。

注销及证据删除由`internal/privacy`提供：先持久独立恢复日志，再原子撤销会话、设置公开屏障并处理存活方关系；清理失败保持`processing`，匿名回执不含身份或正文。显式应用0019并重放权限后，通过`studentapi.Config.Privacy/EvidenceDeletion`及独立运行池启用；普通启动不迁移。宿主开发凭据可选`PrivacyDSN/EvidenceDeletionDSN/EvidencePurgerDSN/RecoveryWriterDSN/RecoveryReaderDSN/RecoveryWrapKey`，日志包裹密钥必须独立，初始化不创建这些资源或替换旧密钥。`make build`同时构建`privacy-worker`；直接运行时显式提供`APP_ENVIRONMENT=integration`、`APP_DEVAPPFILE`和`APP_PRIVACY_SCOPE=account`或`evidence`。此开发入口未配置外部全版本清理及脱敏适配器，只维持持久待办；正式宿主须注入`privacy.Eraser`、`privacy.Redactor`和独立日志后调用`Service.Run`，不能把开发进程用于生产。接入、恢复重删及验收边界见[隐私清理现状](../docs/development/backend/README.md)。

本地隐私注销与证据删除需先 `make dev-stop`，再显式运行 `make dev-privacy-init`（Docker宿主模式），初始化独立恢复 PostgreSQL、专用身份和用途密钥。随后分别运行 `make dev-api`、`make dev-worker`、`make dev-content-worker`、`make dev-privacy-worker` 与 `make dev-evidence-worker`；五个进程均由 `make dev-stop` 停止，中间件与卷保留。恢复库端口默认仅绑定127.0.0.1:15433。初始化后 `make dev-infra` 同时启动恢复库，普通启动不执行迁移。缺少外部全版本清理或脱敏适配器时，隐私任务保持processing，不声称清理完成。配置和联调范围见[本地业务入口](FUNCTIONALITY.md#14-本地业务测试模式)。

树洞基础通过显式迁移0021建立独立实体、内容约束和最小权限，0022允许审核任务保存树洞初始零限制代次，0023保留业务生命周期内的最小幂等回执；旧黑名单关系原位生成不透明`block_id`。升级此版本的业务API／worker前须显式应用至0026并重放权限；迁移由专用身份执行，普通启动不执行DDL。`internal/whisper`提供文字草稿、三档身份、昵称、公开／本人查询及送审边界，`httpapi.RegisterWhispers`只供显式依赖装配；基础健康入口不启用树洞；完整业务宿主可在正式依赖与恢复门禁通过后启用，实际发布仍须供应商和目标环境验收。缺字典时半匿名不可用，缺援助资料返回unavailable；送审必须注入支持新类型的持久审核适配，不能交给旧消费者。Redis限频需注入独立用途密钥并失败关闭；取消／撤回独立于发布预算。`make integration`包含隔离子库迁移、权限、资源锁、文字事务与公开遮蔽、Redis滚动预算验证；外部审核、字典及KMS测试夹具不代表生产验收。

树洞审核使用独立的 `moderation.WhisperReviewSink/WhisperWorker` 和 `WhisperReviewRoute`；公开分析器显式区分帖子、暖评与昵称，胶囊使用独立私密分类器，不接受公开脱敏正文替换。必须显式注入三类已发布词库、安全分析与关怀／通知供应商。`httpapi.RegisterWhisperGovernance` 分别注入普通后台、危机后台、学生举报和独立证据删除池；0024增加受限原稿读取函数、危机来源快照与删除回执命名空间，普通后台不能整表读取胶囊或昵称原稿。基础健康入口不注册这些能力；完整业务宿主已组合治理路由与独立隐私服务，并由专属worker处理审核和清理。升级须由迁移身份显式应用至0026并重放bootstrap权限，不能仅替换二进制；隔离验证依赖真实PostgreSQL，完整 `make integration` 另需Redis和RocketMQ，分析、KMS及通知使用合成夹具。

暖评、抱抱和本模块通知通过 `httpapi.RegisterWhisperInteractions` 显式注册，需与文字及审核服务共用账号 guard 和父帖锁协议。暖评允许未实名正常账号；改稿重新绑定父版本和评论代次，关闭评论保留既有公开评论。抱抱使用独立版本和取消记录，计数由 PostgreSQL 过滤注销／受罚主体；评论首次公开在批准事务内产生唯一站内通知，旧通知入口隔离新类型。部署此适配需显式重放 `permissions`，授予人工批准通知引用及学生改稿清除公开时间的最小权限；互动结构建立于0024及以前迁移。完整业务宿主可显式启用树洞，仍须通过正式依赖和恢复门禁。

本人时空胶囊通过 `httpapi.RegisterWhisperCapsules` 显式注册五个操作；仅正常账号可封存，受罚本人仍可读取合法到期内容和清理。创建与列表不返回正文，详情依数据库时间、安全批准及当前屏障决定解密。`whisper.NewCapsuleUnlockWorker` 复用持久消费、完成回执和有界补偿，不解密正文；到期未审的工作持久挂起，批准唤醒或补偿后生成一次站内通知。迁移0025允许仅胶囊解锁工作使用waiting，存在挂起工作时拒绝回退；升级须显式迁移并重放权限。取消／删除清普通密文、工作及普通通知，保留最小终态及独立危机证据。完整业务宿主统一装配胶囊审核/解锁；目标环境的文本安全、外部清理与真机验收仍是发布前置。

匿名黑名单通过 `httpapi.RegisterInteractionBlocks` 显式注册；迁移0026建立独立单向策略，默认不切换。完成两端升级后，使用迁移身份和外部 `APP_DATABASE_DSN` 显式运行 `go run ./cmd/migrate opaque-blocks`，验证历史随机标识并切换。切换后旧 GET／POST／DELETE 黑名单经认证统一返回 `409 CLIENT_UPGRADE_REQUIRED`，新接口只返回随机条目或最小受理确认；关闭树洞也不回退，已切换数据库拒绝 down。先部署迁移与兼容代码，再切换，最后完成正式装配验收才可启用树洞；前端回滚须保持服务端匿名保护。

隐私服务沿用独立 `liyuanci_privacy_maintenance` 身份、独立恢复日志和显式 `Redactor`／`Eraser`：注销受理先撤销会话、隐藏整串并取消审核／解锁／通知。帖主的他人评论保持隐私屏障；受控脱敏及外部清理确认后才提供本人恢复候选，原 body 与旧父帖标识始终隐藏，重新发布使用新评论。缺少真实适配器时保持 processing。恢复调度必须先调用 `privacy.Service.Recover` 读取包含已确认删除的独立日志并完成重删，再开放读取及外发；普通启动不替代此恢复门禁，也不宣称完成备份全副本清理。

树洞正式依赖装配由 `APP_WHISPERSFILE` 指向仓库 `.local` 忽略目录或外部私有 JSON 文件，模板为 `configs/examples/whispers-runtime.json`；所有空凭据／密钥／资源必须显式补齐，期限使用纳秒整数，七个用途密钥为互不相同的32字节Base64值。`KeyRefs` 与 `RecoveryKeyRefs` 分别列出业务及恢复KMS允许的当前和历史引用，首项用于新包裹；不得生成替代历史密钥。API只打开API／普通后台／危机／隐私／证据删除五个独立运行池和恢复日志池；内容进程使用domain-worker，隐私进程按scope使用privacy-maintenance或evidence-purger。每个进程私有文件只需提供自身使用的业务DSN，恢复读写池与公共约束必须显式提供。

`server -config <应用YAML>` 读取 `whispersfile`（环境覆盖为 `APP_WHISPERSFILE`），支持 `-check` 只装配与诊断而不监听；应用YAML仍显式设置environment、数据库driver、Redis及可选MQ。仅树洞组件运行时复用数据库中的有效学生／后台会话，登录仍由既有身份入口提供；不会生成模板账号或模拟登录。新旧API实例应使用同一版本的会话与Me投影。宿主已装配的安全和注销路由不重复注册；树洞新增50操作整体注册。基础 `make run` 不读取该文件，仍只提供健康端点。

`content-worker` 在设置 `APP_WHISPERSFILE` 时启动树洞审核、胶囊解锁及危机补偿，未设置时保持原开发内容入口。审核／解锁的MQ文件分别使用 `configs/examples/whispers-review-mq.yaml`、`whispers-unlock-mq.yaml` 的独立topic／group，主题必须由运维提前建立；运行进程不创建主题。仅隔离integration可显式 `DatabasePolling=true` 且不设置MQ文件；生产不接受无鉴权MQ或数据库轮询。旧内容进程与树洞进程分别消费各自领域，危机投递按来源隔离；关闭模块仍可处理已持久工作与取消，不清除任务或幂等历史。

`privacy-worker` 使用同一显式入口及 `APP_PRIVACY_SCOPE=account|evidence`；`APP_PRIVACY_RECOVER=true` 先回放所有独立日志（含已确认删除）并重删，只有待办全部完成且目标不存在才成功退出。恢复流程为：隔离读取和外发→分别完成账号／证据恢复清理→API和内容进程启动门禁通过→再由运维开放访问。冷启动发现日志目标仍存在时拒绝启动，普通清理中断也遵守此门禁；失败后由隐私进程继续重试，不手工删日志绕过。90天备份与真实全版本清理仍须由正式供应商验收。

供应商网关必须是经系统CA或显式 `GatewayCAFile` 验证的HTTPS地址，禁止重定向；令牌从 `GatewayTokenFile` 读取。请求为 `{Operation,Version,Input}` JSON；health校验ProviderVersion及能力；wrap／unwrap限定scope和允许密钥引用；public_analysis返回公开安全候选，private_classify只接受分类；redact返回幸存作者安全内容；erase必须同时确认Completed与WriteBarrierConfirmed；notify按DeliveryID去重。网关协议是本端私有适配边界，未修改三端固定契约。不得记录网关请求／响应、原稿、密钥、DSN或令牌。

`CatalogFile` 为版本化公共资源 `{Version,Catalog:{Grades,Reports,Help},Care}`，使用领域相同字段；援助与关怀须由资料方提供来源、版本和核验日期，不提供模板联系方式。API每次读取当前资料，坏版本失败关闭；内容进程启动核验关怀资料。正式外部依赖模式启用前须已完成0026和permissions、发布三类词库、执行opaque单向切换、提供全部正式依赖并完成恢复清理；本地业务测试模式使用[功能说明](FUNCTIONALITY.md#14-本地业务测试模式)中的简化适配。旧独立 `APP_WHISPERSFILE` 入口继续拒绝生产 `Enabled=true`；下述完整 `APP_BUSINESSFILE` 宿主允许启用，模板默认包含悄悄鲤。模板中的空凭据不能启动；本地或隔离通过不表示已部署或完成三端验收。


## 5. 完整生产装配与上线流程

本节对应 `cmd/server`、`cmd/business-worker`、`cmd/business-init`，覆盖认证、心愿/应愿/聊天/还愿、资料、安全/隐私以及悄悄鲤的帖子、昵称、暖评、抱抱、通知、本人胶囊。普通启动只读配置和检查依赖，不迁移、不建账号、不换历史密钥。开发入口与旧独立树洞入口不承担本次生产发布。

2026-11-01用户选定 **kurous.cn小规模原生试运行，保留PostgreSQL/Redis，使用持久数据库任务，不部署RocketMQ及完整监控栈**。按[第6节](#6-无mq原生生产装配)执行；本节的业务身份、正式资料、供应商和隐私恢复约束继续适用。以下MQ/Compose配置保留用于既有兼容与隔离回归，不是本次部署依赖，不能直接照搬启动。

### 制品、配置与进程

原生制品与配置由第6节入口生成。既有容器方案从同一源码构建最终镜像并固定digest，使用 [业务JSON](configs/examples/business-runtime.json)、[应用YAML](configs/app/production.yaml)、[生产环境变量](configs/examples/production.env)及四份 `production-*-mq.yaml`。配置中时长是纳秒整数、用途密钥是32字节Base64；空值必须来自正式配置。`Runtime.Enabled=true` 包含悄悄鲤；生产继续拒绝 `LocalTest`、本地包裹密钥/对象、文件恢复日志、旧 `DatabasePolling=true` 和无鉴权MQ。新的 `Runtime.TaskTransport="postgres"` 仅由完整业务宿主支持，不能混入任何MQ配置文件。

| 进程 | 业务数据库身份（独立LOGIN只继承对应NOLOGIN角色） | 进程专属输入 |
| --- | --- | --- |
| API | `liyuanci_api`、`liyuanci_identity_maintenance`、`liyuanci_admin_maintenance`、`liyuanci_admin_api`、`liyuanci_crisis_api`、`liyuanci_privacy_maintenance`、`liyuanci_evidence_delete_api` | 微信AppID/Secret、后台初始化口令文件、13个不同用途密钥、身份检索引用、鉴权Redis |
| card-worker | `liyuanci_domain_worker` | CardKey、图片/对象能力、card-mq |
| content-worker | `liyuanci_domain_worker` | 普通内容/私聊处理、关怀/通知、content-mq |
| whispers-worker | `liyuanci_domain_worker` | 树洞审核、危机补偿、胶囊解锁，review/unlock两个MQ配置 |
| privacy-worker | `liyuanci_privacy_maintenance` | 账号清理与恢复 |
| evidence-worker | `liyuanci_evidence_purger` | 证据清理与恢复 |
| business-init（显式一次性工具） | `liyuanci_identity_maintenance`、`liyuanci_admin_maintenance`；初始化词库时另需 `liyuanci_admin_api` | 身份检索引用、首个超管、可选三类词库；初始化Redis及供应商依赖 |

同角色不同进程也使用独立LOGIN。每进程只挂自己的私有目录，目录之间不得重叠，删除模板中不需要的DSN和额外用途密钥；Runtime的七个共同用途密钥及业务/恢复引用保持一致，API和card的CardKey、API和init的AdminKey等同用途保持一致。所有进程需要独立恢复日志读写池、HTTPS网关、版本化资料与共同运行约束；恢复库角色使用既有 `journal-permissions` 输出，和业务数据库处于独立故障域。主业务各池必须指向同一业务库，禁止复用迁移超级账号。

私有目录不纳入Git，文件应只供运维与容器UID 10001读取；不要将密钥、DSN、二维码联系人或学校身份发到测试日志。新建环境的密钥生成后纳入备份；升级保留历史包裹/检索引用，禁止用新随机密钥替换历史密钥。每份目录内部文件引用统一为 `/run/business/...`，API及init另有 `app.yaml` 和Redis口令文件。使用Compose时环境文件中的secret路径填写绝对路径。

供应商需要实现本仓库的私有HTTPS网关协议，只有云账号还不够。请求 `{Operation,Version,Input}`；版本及能力必须可核验，凭据放 `GatewayTokenFile`，TLS校验系统CA或显式CA，不接受重定向。既有操作为wrap/unwrap、public_analysis、private_classify、redact、erase、notify；完整业务另需：

| 操作 | 关键输入/输出约束 |
| --- | --- |
| identity_digest | 输入KeyRef/Purpose/Value；目的为openid或student_number，输出固定32字节Digest；当前与历史引用均可用 |
| object_put | 输入Key/SHA256/Data（加密字节），同键幂等，输出不可变Version及相同SHA256 |
| object_open | 按Key和精确Version读取，输出相同Version及有界加密Data；不接受最新版本替换 |
| image_scan | 输入Data/MediaType；必须显式输出Approved布尔值及DetectorVersion |
| school_validate | 输入Name/StudentNumber；必须显式输出Valid布尔值及RulesVersion |
| content_analysis | 输入wish.Content；输出经过处理的Body、Tags、分类结果及DetectorVersion |

所有二进制字段按JSON Base64。完整字段以 [gateway](internal/whisperapp/gateway.go) 和 [business_gateway](internal/whisperapp/business_gateway.go) 为准。erase须同时确认Completed与WriteBarrierConfirmed，覆盖历史版本/副本和写屏障；notify按DeliveryID去重，失败可重试。供应商必须证明这些语义，不能靠固定成功响应通过验收。

### 按顺序执行

1. 完成[外部交付清单](../docs/development/backend/defects.md)、供应商联调及目标环境准备；固定镜像/三端版本、备份点、回滚版本与上线窗口。API只映射loopback，正式HTTPS由目标反向代理终止；管理端同源代理、history fallback与敏感API禁止缓存；小程序使用正式AppID与合法域名。
2. 建立专用PG/Redis和外部恢复日志库；本次不部署RocketMQ。使用专用迁移身份显式执行 `migrate up`（截至0029）、`migrate permissions`，对恢复库执行 `migrate journal-up` 和 `migrate journal-permissions`。业务DSN从 `APP_DATABASE_DSN`，恢复DSN从 `APP_RECOVERY_DATABASE_DSN` 注入；普通进程不拿到这些迁移凭据。本次必须重放permissions，新增的最小授权仅为identity-maintenance读取匿名黑名单全局开关。
3. 建立受限LOGIN及Redis独立凭据。本次数据库任务模式跳过全部MQ操作；仅既有MQ部署才提前创建四个主题和各进程ACL：`liyuanci_student_card_scan_v1`、`liyuanci_content_review_v1`、`liyuanci_whisper_review_v1`、`liyuanci_whisper_unlock_v1`，禁止复用探针主题或无鉴权配置。普通worker只访问自身主题/消费组；生产单Broker模板不承诺高可用。
4. 导入正式 `CatalogFile`（`{Version,Catalog:{Grades,Reports,Help},Care}`）。年级、举报类型、援助与关怀不得用合成联系人代替；Help须ready、有版本与核验日期。用迁移身份受控导入 `report_types`，其已发布code/label须和Catalog.Reports完全一致，记录来源和变更。初始化三类词库JSON格式为 `{"crisis":[...],"school":[...],"ordinary_violation":[...]}`，每类非空；使用 `business-init -config /run/business/app.yaml -login 正式超管名 -wordlists /run/business/wordlists.json` 显式发布。该工具不重置已有账号，不覆盖不同的生效词库，正常运营变更走后台审计流程。
5. 完成兼容版本三端升级后，迁移身份执行 `migrate opaque-blocks`。该切换单向；正式悄悄鲤启用前必须通过。分别运行 `business-worker -config /run/business/runtime.json -environment production -kind account -recover` 与 `-kind evidence -recover`，恢复日志重删完成后才允许API读取及worker外发。
6. 执行下面的离线核对，再在目标网络逐进程执行API的 `-check`、五类worker的 `-check`。离线检查不连接网络；在线诊断检查角色、KMS、资料、结构和恢复门禁，不启动消费者，**不证明MQ消息鉴权或正式通知已成功**。实际启动后必须完成学生卡、普通内容、悄悄鲤审核、胶囊解锁四类专用测试工作并核对数据库消费回执。
7. 经本次上线授权后按第6节启动原生API及五类worker；初始化始终显式执行。既有MQ容器方案才组合 `production.yaml` 和 `business-production.yaml`。检查readyz、worker心跳、积压/重试、真实TLS和告警。先内部验收，再按发布窗口开放入口；小程序另完成体验版真机及平台审核发布。
8. 验收认证→心愿→匹配→聊天→还愿、资料、普通/树洞治理和隐私。悄悄鲤必须逐项验帖子/三档身份/昵称/暖评/评论关闭/抱抱/通知/胶囊/危机/举报/匿名屏蔽；手机A/B、跨号404、旧版本409、撤权/晚到与重试同时覆盖。本次先按第6节限定试运行负载验收；REQ-021的300在线、约87请求/秒另作正式容量目标，未完成前不得宣称达标。
9. 回滚先隔离入口和外发、保留任务/回执/恢复日志，再切回已验兼容镜像或原生二进制制品；不自动down迁移，不撤销opaque切换。数据库恢复后先两域recover再放行；清理失败保持屏障。停止只按本项目服务执行，不删卷。

离线检查（材料未齐时应失败，不把模板填成假成功）：

```sh
python3 scripts/release.py --env-file /绝对私有路径/operations.env
python3 scripts/release.py --env-file /绝对私有路径/operations.env --validate-containers
# 单进程离线入口；不会连接数据库、供应商或执行初始化
bin/server -config /绝对私有路径/app.yaml -check-config
bin/business-worker -config /绝对私有路径/runtime.json -environment production -kind whispers -check-config
bin/business-init -config /绝对私有路径/app.yaml -check-config
```

release.py核对固定digest、七个独立目录、共享用途配置；`--validate-containers` 只用本地已有镜像，在无网络、只读、非root容器中逐进程校验，不拉镜像、不迁移、不部署。Compose组合入口为 `docker compose --env-file /绝对私有路径/operations.env -p 正式项目名 -f deploy/compose/production.yaml -f deploy/compose/business-production.yaml ...`，使用前核对项目/目录，避免带入开发环境。

本轮隔离验收使用真实PG、受限角色、鉴权Redis和实际进程，外部学校/图片/KMS/对象/通知为TLS合成；真实微信登录、云供应商、独立故障域、90天备份、正式告警投递、真机与容量仍未替代。当前结果及资源清单以[后端现状](../docs/development/backend/README.md)和[缺口](../docs/development/backend/defects.md)为准。

## 6. 无MQ原生生产装配

目标为Ubuntu 22.04、[systemd 249](https://www.freedesktop.org/software/systemd/man/249/systemd.exec.html)及以上、x86_64小规模试运行；不要求服务器安装Go、Docker、RocketMQ、Prometheus、Grafana或Loki。以下命令是经部署授权后在目标机器执行的流程，本轮未连接或修改服务器。已有MQ实现与固定设计材料保留兼容，新部署只选择postgres任务通道，不需要MQ账号/主题/端口。

### 制品与私有配置

在构建机Go 1.25环境执行，输出路径必须不存在：

```sh
python3 scripts/native.py bundle --output .local/native-release --arch amd64
python3 scripts/native.py verify --bundle .local/native-release
python3 scripts/native.py scaffold --output .local/native-private
```

制品包含四个Linux静态二进制、[systemd单元](deploy/systemd)、[公开模板](configs/native)、运维脚本和SHA-256清单；清单注明源码HEAD及dirty状态，工作区构建不能冒称已提交版本。发布时另存清单自身摘要到可信制品记录，校验和不代替来源真实性。私有配置单独传输，不打进发布包；`scaffold`不生成凭据、不覆盖已有文件，空模板必须检查失败。

每份JSON均设置 `Runtime.TaskTransport="postgres"`、`DatabasePolling=false`、`Enabled=true`、连接池2/1、扫描间隔1秒；四个MQ路径为空。共享七个Runtime用途密钥、网关版本和KMS引用保持一致，其余只填各自所需DSN/用途密钥，见第5节角色表。`Card.DecodeSlots=1` 限制图片解码并发。API/init各自提供Redis口令、后台初始口令；API另有微信secret。全部供应商仍为真实HTTPS能力，外部恢复日志仍需独立故障域，不能同机另建一个库冒充。正式Catalog和词库按第5节导入。

`work_items`和关联事务事件是持久任务来源；学生卡、普通内容、悄悄鲤审核和胶囊解锁均沿用版本、租约、重试、取消和消费回执。账号/证据清理继续扫描各自持久待办。进程停止期间不丢任务，重启扫描到期任务与过期租约；重试耗尽进入attention，必须经业务治理处理，不清空回执或手改完成。未来胶囊和待审核waiting不计为普通到期积压。无MQ时不写`sent_at`，不能把未发送outbox总量当积压，也不批量伪造发送成功或删除历史；保留既有隐私清理语义，另监测数据库增长。

每类扫描一次有界处理，成功、失败和空队列后都等待Interval，不热循环。默认1秒间隔使单扫描器处理速率不高于约1次/秒（实际还受处理耗时影响），不适合作为高并发承诺；扩容需连同连接池、供应商配额和幂等竞争验收。

### 目标机显式准备与初始化

1. 安排系统维护窗口，确认同名`lyc-*`身份、`/etc/liyuanci/native`、`/opt/liyuanci`、`/var/lib/liyuanci`和单元均归本项目；保留其他业务。根据[PostgreSQL Ubuntu官方源](https://www.postgresql.org/download/linux/ubuntu/)和[Redis官方安装说明](https://redis.io/docs/latest/operate/oss_and_stack/install/archive/install-redis/install-redis-on-linux/)准备受信软件源/离线包的PostgreSQL 18.x（本地验证18.6）、Redis 7.2.x（本地验证7.2.12）、psql、Python3、curl、CA证书。PG二进制路径须为`/usr/lib/postgresql/18/bin`，Redis为`/usr/bin/redis-server`。发行版旧Redis包不能未经核对直接替代；包安装可能启动默认实例，核实后只使用本项目独立端口/目录，不停其他实例。PG/Redis仅监听127.0.0.1:15432/16379，API为127.0.0.1:18081；先核对端口，zuxing.net此前18081有冲突。
2. 将制品安装为root拥有、其他身份只读的`/opt/liyuanci/releases/<版本>`，校验清单，再在业务停止时更新`/opt/liyuanci/current`链接。运行 `systemd-sysusers /opt/liyuanci/current/templates/sysusers.conf` 创建专属身份。复制私有配置至`/etc/liyuanci/native`，父目录root:root/0711，子目录root:对应lyc组/0750，配置文件root:对应lyc组/0640。monitor的pgpass是例外：lyc-monitor:lyc-monitor/0600；不共享整个私有目录给所有进程。bootstrap不常驻。首次生成的密钥交付后纳入备份，升级绝不替换历史密钥。
3. 在私有`redis/users.acl`中保留`user default off`，另加命名用户记录：`user liyuanci on #<实际口令SHA256> ~admin-login:* ~whisper-rate:* +ping +hello +client|setinfo +eval +incr +pexpire +time +zremrangebyscore +zcard +zrange +zadd +zrem`。用实际64位哈希替换尖括号占位，API/init的redis.password保存对应原口令；ACL文件不支持普通注释行。默认用户关闭，密码不出现在命令行或日志。连接失败和内存不足均失败关闭；禁止以驱逐限频状态“修复”可用性。
4. 创建专属PG数据目录，仅新建空目录执行以下初始化；initdb自身拒绝非空目录，不加删除/覆盖参数：

```sh
sudo install -d -m 0755 /var/lib/liyuanci
sudo install -d -m 0700 -o lyc-postgres -g lyc-postgres /var/lib/liyuanci/postgres
sudo -u lyc-postgres /usr/lib/postgresql/18/bin/initdb -D /var/lib/liyuanci/postgres --username=lyc-postgres --auth-local=peer --auth-host=scram-sha-256 --encoding=UTF8
# 单元必须来自已校验制品；systemctl link遇到已有同名文件先停下核查，不强制覆盖。
sudo systemctl link /opt/liyuanci/current/systemd/*.service /opt/liyuanci/current/systemd/*.target /opt/liyuanci/current/systemd/*.timer /opt/liyuanci/current/systemd/*.slice
sudo install -m 0644 /opt/liyuanci/current/templates/journald.conf /etc/systemd/journald@liyuanci.conf
sudo systemd-analyze verify /opt/liyuanci/current/systemd/*.service /opt/liyuanci/current/systemd/*.target /opt/liyuanci/current/systemd/*.timer /opt/liyuanci/current/systemd/*.slice
sudo systemctl daemon-reload
sudo systemctl start liyuanci-postgres.service liyuanci-redis.service
sudo -u lyc-postgres /usr/lib/postgresql/18/bin/createdb -h /run/liyuanci-postgres -p 15432 liyuanci
sudo -u lyc-postgres env APP_DATABASE_DSN='host=/run/liyuanci-postgres port=15432 user=lyc-postgres dbname=liyuanci sslmode=disable' /opt/liyuanci/current/bin/migrate up
sudo -u lyc-postgres env APP_DATABASE_DSN='host=/run/liyuanci-postgres port=15432 user=lyc-postgres dbname=liyuanci sslmode=disable' /opt/liyuanci/current/bin/migrate permissions
```

5. 迁移完成后，按第5节为每进程创建不同LOGIN，仅继承其对应NOLOGIN角色，无SUPERUSER/CREATEDB/CREATEROLE/REPLICATION/BYPASSRLS；口令用受控psql `\password`交互设置，不放shell历史。业务DSN指向本机专用库；外部恢复日志库用独立迁移身份显式执行journal-up、journal-permissions，运行进程只拿到受限读写身份。monitor使用独立`lyc_monitor` LOGIN，仅CONNECT、public schema USAGE及work_items/deletion_jobs SELECT；不授业务运行角色、DDL或写权限，私有monitor.env与pgpass填写此身份，普通进程不拿到管理员凭据。
6. 按第5节导入正式举报字典与资料，用`lyc-bootstrap`执行business-init（私有app.yaml、显式login/wordlists）；再由迁移身份显式执行opaque-blocks。只在业务读写关闭时，分别以`lyc-account`、`lyc-evidence`执行 `business-worker -config /etc/liyuanci/native/<kind>/runtime.json -environment production -kind <kind> -recover`。任一失败都不开放读取和外发。普通systemd启动不会执行这些操作。

### 启停、升级与回滚

先完成跨进程配置一致性及各自OS身份读取权限检查，再验证角色、供应商、字典、词库和恢复门禁：

```sh
sudo python3 /opt/liyuanci/current/scripts/native.py check-config
sudo python3 /opt/liyuanci/current/scripts/native.py check
# 默认只输出待执行命令；--apply才操作本机专属单元。
sudo python3 /opt/liyuanci/current/scripts/native.py start --scope all --apply
sudo python3 /opt/liyuanci/current/scripts/native.py status --scope all
curl --fail http://127.0.0.1:18081/readyz
sudo journalctl --namespace=liyuanci -u liyuanci-whispers.service --since '10 minutes ago'
# 日常停止/重启仅操作业务和轻量定时器，保留PG/Redis及数据。
sudo python3 /opt/liyuanci/current/scripts/native.py stop --apply
sudo python3 /opt/liyuanci/current/scripts/native.py start --apply
# 维护全停；不删除数据库、Redis数据、任务、回执或历史密钥。
sudo python3 /opt/liyuanci/current/scripts/native.py stop --scope all --apply
```

只有上述检查和业务验收通过后，才显式`systemctl enable liyuanci.target`设置开机启动，并实测一次重启恢复。单元来源必须解析到当前制品，否则脚本拒绝管理同名服务。业务停止反向退出API/业务worker及隐私worker；全停再退出PG/Redis。服务异常5秒后重启，60秒内最多3次；修复原因后显式reset-failed对应单元再启动，不靠无限重启掩盖配置/供应商故障。PG采用SIGINT快速且持久化关闭，Go采用SIGTERM/90秒停止预算。

升级先关闭外部入口、停止业务、备份并核对兼容迁移，再切换current，校验清单和离线/在线配置，daemon-reload后启动。基础设施配置或单元改变时安排全停；不能只改链接便宣称新资源限制生效。回滚切回此前已验兼容制品，保留数据库结构、历史密钥、任务和回执；不自动down迁移、不撤销opaque。恢复数据库后必须重新完成两域recover；恢复日志/KMS不可用或删除未完成时继续保持关闭。

### 资源与轻量监测

初始预算是试运行限制而非容量结论：业务角色池2/1，PG最大48连接/64MiB shared_buffers/1MiB work_mem，Redis maxmemory32MiB且noeviction；API内存上限192MiB，学生卡128MiB，其余worker各96MiB，PG256MiB、Redis64MiB。总slice MemoryHigh896MiB、MemoryMax1100MiB、swap上限128MiB，Go进程GOMAXPROCS=1。限制可能触发拒绝或OOM，须按实测调整，不把6GiB swap当可用内存；已有其他业务未释放时尤其不能直接套用。

日志进入独立journald命名空间，保留7天且持久空间上限128MiB；不覆盖系统全局日志策略。health.timer每分钟检查8个常驻服务、readyz、磁盘80%、宿主可用内存15%、attention及到期待办超过5分钟、隐私待办超时。结果只含聚合数据；`native.py check`只诊断依赖，不消费、不代表验收。`systemctl status liyuanci-health.service`查看最近结果；真实告警渠道与接手人仍由用户交付并验收，定时器失败日志不是“告警已送达”。维护停止业务时定时器一并停止。

详查积压使用只读身份 `psql -X -v ON_ERROR_STOP=1 -f /opt/liyuanci/current/templates/backlog.sql`，连接参数由私有PG环境/pgpass提供。scheduled/waiting单列，waiting需进一步区分审核尚未完成与应解锁未处理。不要只看/healthz判断依赖就绪，也不要把systemd active当消费成功。

### 验收与放行顺序

| 检查 | 必须观察到的结果 |
| --- | --- |
| 制品、配置与权限 | 摘要一致；7份配置同用途密钥一致；空模板/开发轮询/MQ混配/管理员DSN被拒；进程不能读取其他私有目录 |
| 原生中间件 | PG/Redis重启后数据与任务存在；匿名Redis连接失败；数据库运行身份无DDL/跨域权限；只有loopback监听 |
| 全业务与悄悄鲤 | 学生卡扫描、普通审核、树洞/昵称/暖评/评论开关/抱抱/通知/胶囊、治理与匿名屏蔽；全程无MQ进程和连接；API与五类worker均验收 |
| 持久任务 | 停worker后提交任务，数据库可查且重启继续；外部故障后retry_wait和重试上限attention；过期租约回收、双进程争抢幂等、取消/旧版本晚到无副作用；未来胶囊不提前返回，批准且到期后仅解锁一次 |
| 恢复与故障 | 分别中断PG/Redis/供应商；readyz及受保护操作失败关闭；恢复后处理积压；账号/证据删除失败保留屏障；两域recover后才恢复读取/外发 |
| systemd与运维 | 优雅停止、异常重启、连续失败限速、开机启动、业务停与全停、切版本回滚各一次；无其他服务被启停，数据与历史密钥保留 |
| 小规模负载 | 建议先5个专用测试账号、总请求约2次/秒、异步任务提交不高于0.2次/秒持续30分钟；记录P95/错误率/RSS/CPU/磁盘/到期积压趋势，无OOM或持续swap增长，积压可回落；这是试运行起点，正式上线人数和指标需结合实测确定 |
| HTTPS、备份与告警 | 接入现有Nginx且不覆盖其他站点；真实TLS/域名、三端双设备、供应商、备份恢复、告警送达与响应逐项通过 |

不依赖正式外部资料的本地验证入口为`make integration-native`（先为隔离项目提供随机`LIYUANCI_TEST_DB_PASSWORD`和独立`COMPOSE_PROJECT_NAME`，不复用生产凭据）：仅启动隔离PG/Redis与生产业务测试，外部供应商为TLS合成。验后用同一环境文件及项目名执行 `docker compose -f deploy/compose/native-integration.yaml stop`，保留卷；这只是构建机测试依赖，不能冒称目标机systemd/原生安装/性能验收。正式REQ-021容量、全副本/90天恢复和微信真机仍按[后端缺口清单](../docs/development/backend/defects.md)推进。
