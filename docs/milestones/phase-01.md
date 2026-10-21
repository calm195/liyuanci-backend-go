# 第一阶段：工程基础与开发运维

T001–T004第一批实施；REQ-001、018、020。固定方案B：Go模块化单体API＋独立worker＋PostgreSQL事务发件箱＋Redis＋RocketMQ。当前为工程基础，领域发件箱与认证未实现，不作为生产服务。

共享契约入口：[docs/contracts/README.md](../../../docs/contracts/README.md)，版本0.1.0；80个业务操作均为定义，不是已实现接口。后端不内置假登录或成功业务Mock；两端可基于合成示例独立准备夹具，必须标明模拟。

文档归档说明：本文件保留原 README 的第一阶段实施、操作与验证记录，关联 T001–T004 / REQ-001、018、020；各次验证结果仅代表对应记录的执行范围，不表示本次重新执行或当前服务在线。后续阶段按文件记录，README 仅维护项目介绍、选项与目录结构、启动方式、开发规范与须知。当前规范见 [AGENTS.md](../../AGENTS.md)。

注释与日志规范（T004；REQ-018、020）：非测试代码不得出现 `T00x` 等任务或里程碑编号；注释使用中文说明功能、业务规则与必要的设计原因，日志消息及面向人的说明使用中文。结构化字段名、协议值和必要的技术标识保持稳定，任务 ID/REQ 仅在测试和文档中追溯。

存量清理已覆盖 API、worker、探针、配置、迁移注释、构建与启停入口，以及 CI 配置；应用日志、配置错误说明和告警标题/摘要改为中文，删除日志和告警标签中的任务 ID/REQ。HTTP 完成、异常恢复和心跳仍使用原有稳定事件字段，告警查询、UID、阈值及业务行为保持不变。测试断言随中文消息同步；启停单元测试隔离宿主默认凭据目录，避免本机配置污染验证结果。集成验收脚本及测试夹具属于测试代码，历史文档和证据保留追溯编号；第三方组件的原生日志和错误正文不做翻译。未增加全仓库静态规范检查器。

本次清理验证（2026-10-20）：`make fmt`、`make check`（格式检查、`go vet`、race/coverage 测试）、`make build`、`python3 -m unittest discover -s scripts -p test_services.py -v`（13 项）及 `python3 scripts/check-api.py` 通过。真实隔离 `python3 scripts/check-observability.py --alerts --metrics`（项目 `liyuanci-logs-ce19888b5a`）通过中文告警求值、合成 Webhook 触发/恢复及采集/指标验证；`python3 scripts/check-management.py --middleware-only --metrics`（项目 `liyuanci-mgmt-a8ac08d53a37`）通过七目标采集、错误凭据识别和恢复；`python3 scripts/check-services.py`（项目 `liyuanci-dev-ops-df574141ac`）通过全栈启动、探针往返、API 日志查询、无隐式迁移重启及停止/移除后卷保留。验证容器和网络已清理，卷保留；未执行生产、Linux 宿主、真实通知或远端 CI 验收。

```sh
make fmt
make check  # 格式检查、vet、race测试与覆盖率；无需中间件
make build  # bin/server、bin/worker、bin/probe
make run    # 默认仅监听127.0.0.1:8080
```

`GET /healthz`为进程存活；`GET /readyz`在本阶段为503（T008认证和密钥就绪尚未实现），不能以Producer初始化推定Broker可写。其他路径404，无模板`/api/v1/users`接口。

```sh
# 默认全新隔离项目：随机测试口令仅存在运行进程，不写入文件。
sh scripts/integration.sh
# 官方Go代理不可达时可显式使用构建下载代理，保留go.sum/GOSUMDB校验：
LIYUANCI_BUILD_GOPROXY=https://goproxy.cn,direct sh scripts/integration.sh
# 仅需独立检查PG/Redis时，单独验证真实PG/Redis；结果明确排除MQ。
sh scripts/check-connections.sh
```

完整集成顺序：固定镜像→专用MQ卷权限初始化与真实PG/Redis/NameServer/Broker→显式探针迁移及主题创建→API/独立worker→双生产同一随机事件→PostgreSQL单回执行投递计数至少2。worker仅在integration环境消费专用主题`liyuanci_foundation_probe_v1`；数据库提交后才确认，未知格式有限重试。此链不代替T007的领域幂等/取消/发件箱故障测试。完整往返已通过：官方MQ5.3.2＋Go SDK2.1.2，实际Go1.25容器构建与非root运行。用户已决定放弃4.9.8，统一采用当前固定digest的5.3.2，见[版本决定](../middleware-decision.md)；不签T007或性能通过。

配置使用APP_环境变量；DSN、密钥、真实令牌不写仓库/日志。`configs/integration.yaml`无凭据；生产配置与隔离配置分开，缺少真实依赖不能静默变成Mock。API通过独立ingress网络仅绑定loopback端口18080，PG/Redis/MQ保持内部网络。集成脚本默认每次新建隔离项目，完成或失败均清理本次容器/网络并保留卷；显式指定项目时自行管理凭据与生命周期。容器构建保留go.sum，不在Dockerfile tidy；显式迁移不在启动执行，无AutoMigrate。

验证、阻塞和交接见[docs/validation.md](../validation.md)、[环境与D01–D06](../environment.md)、[批次状态及下一批入口](../implementation.md)。CI执行相同质量检查，独立middleware任务执行真实隔离集成；工作流尚未在远程运行，不代表CI通过。本地提交按用户指令分批执行；未推送、部署或上线。

## Docker 中间件与管理工具（T003/T004；REQ-018、020）

开发集成与未来生产的PostgreSQL、Redis、RocketMQ统一使用Docker部署。原`compose.integration.yaml`保持隔离探针用途；新增`compose.production.yaml`独立项目/卷，仅编排中间件，通过仓库外secret文件启用PG口令、Redis ACL和MQ ACL，不包含未实现的业务API/worker。数据库仍为权威，迁移仍显式执行。

`compose.management.yaml`为两种环境提供可选`management` profile，使用固定digest的官方pgAdmin4 9.18、Redis Insight3.8.0、Apache RocketMQ Dashboard2.1.0。页面仅绑定本机15050、15540、18081，中间件不开放宿主端口；远程经SSH隧道。pgAdmin使用独立管理口令，连接凭据不写入仓库；生产Dashboard的MQ凭据通过外部properties文件注入。

配置变量、连接地址、启停与生产限制见[Docker中间件操作说明](../docker-middleware.md)。`python3 scripts/check-management.py`检查两套编排边界，并在唯一临时集成项目启动三款工具、验证HTTP和Broker发现；完成后清理容器/网络、保留卷。该检查不替代原`sh scripts/integration.sh`的生产消费往返，也不验证生产ACL。生产客户端凭据接入、最小权限角色、备份恢复和上线验收仍待后续任务，本批不部署生产。

本次`make fmt/check/build`、两套Compose边界检查及生产形态隔离中间件验证已通过，包含Redis正确口令/匿名拒绝和MQ匿名写入拒绝；真实生产凭据未验。三款管理页面HTTP检查和Dashboard真实Broker发现也已通过；测试结束已停止容器并清理网络、保留卷。详细结果见[验证记录](../validation.md)；不改变契约0.1.0或T004整体待两端验收的状态。

## 本地开发启动（T003/T004；REQ-018、020）

可优先使用新增[启停脚本](../operations.md)：`./scripts/init-dev.sh`初始化缺失开发凭据，首次运行`./scripts/start.sh dev --bootstrap-probe --probe`，日常`./scripts/start.sh dev`，停止`./scripts/stop.sh dev`；默认包含管理工具和日志/告警/指标。以下手工命令仍可用于排查。

前提：Docker引擎及Docker Compose已启动；宿主执行Go检查需Go1.25或更新版本。以下在仓库根目录执行，使用固定项目`liyuanci-dev`保留开发数据。它与`sh scripts/integration.sh`每次创建临时验证项目的用途不同。当前API只提供健康端点，worker只消费集成探针，不包含业务认证能力。

### 1. 准备本地凭据和命令入口

以下初始化可重复执行，不覆盖已有口令；文件存放于仓库外，不提交或打印。新终端需重新执行本节，恢复环境变量及`dc`函数。若此前已使用其他项目名和口令创建数据库卷，应沿用原值，不要将新口令套到旧卷。

```sh
cd ~/code/liyuanci/liyuanci-backend-go
export COMPOSE_PROJECT_NAME=liyuanci-dev
dev_config_dir="$HOME/.config/liyuanci/dev"

umask 077
mkdir -p "$dev_config_dir"
if [ ! -s "$dev_config_dir/postgres-password" ]; then
  openssl rand -hex 24 > "$dev_config_dir/postgres-password"
fi
if [ ! -s "$dev_config_dir/pgadmin-password" ]; then
  openssl rand -hex 24 > "$dev_config_dir/pgadmin-password"
fi
touch "$dev_config_dir/dashboard.properties"
chmod 700 "$dev_config_dir"
# 宿主目录限制访问；文件须允许挂载后的容器非root用户读取。
chmod 644 "$dev_config_dir/postgres-password" \
  "$dev_config_dir/pgadmin-password" "$dev_config_dir/dashboard.properties"

export LIYUANCI_TEST_DB_PASSWORD="$(cat "$dev_config_dir/postgres-password")"
export LIYUANCI_PGADMIN_EMAIL=developer@example.com
export LIYUANCI_PGADMIN_PASSWORD_FILE="$dev_config_dir/pgadmin-password"
export LIYUANCI_DASHBOARD_PROPERTIES_FILE="$dev_config_dir/dashboard.properties"

dc() {
  docker compose -f compose.integration.yaml \
    -f compose.management.yaml --profile management "$@"
}
```

`developer@example.com`仅为本机pgAdmin管理账号邮箱示例，可在首次初始化前替换；不是学生或业务后台账号。pgAdmin已存在数据卷时，修改初始化邮箱/口令文件不会自动修改已有账号，需通过pgAdmin账号管理流程变更。

### 2. 启动中间件、管理工具及后端

```sh
dc up -d --wait postgres redis namesrv broker \
  pgadmin redisinsight rocketmq-dashboard

# 显式初始化集成探针表，并在消费者启动前创建主题。
dc run --rm migrate
dc exec -T broker sh mqadmin updateTopic \
  -n namesrv:9876 -c DefaultCluster -t liyuanci_foundation_probe_v1

dc up -d --build --wait api worker
# 验证真实Producer→MQ→独立worker→PostgreSQL回执。
dc run --rm probe
```

官方Go下载代理不可达时，可在构建前执行`export LIYUANCI_BUILD_GOPROXY=https://goproxy.cn,direct`；仍保留go.sum/GOSUMDB校验。源码修改后重新执行`dc up -d --build --wait api worker`。

### 本地访问地址速查（T003/T004；REQ-018、020）

以下为仓库 Compose 和启停脚本的默认地址。`./scripts/start.sh dev`默认启动中间件、API/worker、管理工具，以及独立监控项目中的日志、告警规则和监控组件指标；上面的手工`dc`命令只启动主项目，不会启动Grafana等监控组件。脚本默认主项目为`liyuanci-dev`、监控项目为`liyuanci-observability-dev`。

| 入口 | 宿主浏览器访问地址 | 登录及用途 |
| --- | --- | --- |
| API存活 | [healthz](http://127.0.0.1:18080/healthz) | 预期200，仅表示进程存活 |
| API就绪 | [readyz](http://127.0.0.1:18080/readyz) | 当前预期503，认证及密钥能力尚未实现 |
| pgAdmin | [PostgreSQL管理](http://127.0.0.1:15050) | 默认邮箱`developer@example.com`；口令见下表；登录后需自行添加数据库连接 |
| Redis Insight | [Redis管理](http://127.0.0.1:15540) | 添加`redis:6379`、DB 0；当前开发集成Redis无密码 |
| RocketMQ Dashboard | [MQ管理](http://127.0.0.1:18081) | 已配置`namesrv:9876`；当前开发集成Broker无ACL |
| Grafana | [监控首页](http://127.0.0.1:13000) | 默认账号`operator`；口令见下表，统一查看日志、指标及告警 |
| 技术日志 | [日志看板](http://127.0.0.1:13000/d/liyuanci-logs) | 需登录Grafana；API/worker等容器技术日志 |
| 运行指标 | [运行指标看板](http://127.0.0.1:13000/d/liyuanci-metrics) | 默认含监控组件指标；Linux宿主面板需显式启用扩展 |
| 中间件指标 | [中间件看板](http://127.0.0.1:13000/d/liyuanci-middleware) | 需准备监控身份并加`--middleware-metrics`，默认启动不采集PG/Redis内部指标 |
| 自定义查询 | [Grafana Explore](http://127.0.0.1:13000/explore) | 选择`Loki`查询日志，选择`Prometheus`查询指标 |
| 告警规则 | [Grafana Alerting](http://127.0.0.1:13000/alerting/list) | 默认加载告警规则；真实通知渠道尚未配置 |

Grafana端口可由`LIYUANCI_GRAFANA_PORT`覆盖，覆盖后同步替换上表的`13000`。`--without-management`或`--without-monitoring`启动时不启用对应入口。所有宿主入口仅绑定`127.0.0.1`；远程Docker主机上的地址应通过SSH隧道访问，不能直接使用客户端本机地址。

| 凭据用途 | 默认仓库外文件/配置 | 说明 |
| --- | --- | --- |
| PostgreSQL | `~/.config/liyuanci/dev/postgres-password` | 数据库和用户名均为`liyuanci`；脚本变量`LIYUANCI_TEST_DB_PASSWORD_FILE` |
| pgAdmin登录 | `~/.config/liyuanci/dev/pgadmin-password` | 管理页面口令，与数据库口令分开；邮箱由`LIYUANCI_PGADMIN_EMAIL`指定 |
| Grafana登录 | `~/.config/liyuanci/observability-dev/grafana-password` | 对应`LIYUANCI_GRAFANA_PASSWORD_FILE`，账号`operator` |
| 启停配置 | `~/.config/liyuanci/dev/operations.env` | 初始化脚本生成，启动自动读取；自定义目录或`--env-file`时以实际文件为准，不执行shell展开 |

通过本机文件查看器读取所需口令，不将其粘贴到日志或提交仓库。已有数据卷时，修改初始化口令文件不会重置数据库、pgAdmin或Grafana中的既有账号；应恢复原凭据或走各组件的账号管理流程。

#### 容器内部连接地址

下表供同一Docker网络内的管理工具、应用及采集组件使用，**没有映射到宿主端口**，不能在宿主直接连接`127.0.0.1:5432`或浏览`127.0.0.1:9090`。网络按职责隔离：主项目使用`foundation`，监控采集使用`telemetry`，可选中间件采集显式连接主项目网络。

| 服务 | 容器内部地址 | 使用方式与范围 |
| --- | --- | --- |
| PostgreSQL | `postgres:5432` | pgAdmin注册服务器时填写此主机/端口，数据库及用户名`liyuanci`，使用数据库口令 |
| Redis | `redis:6379` | Redis Insight添加连接；DB 0，仅当前开发集成环境无密码 |
| RocketMQ NameServer | `namesrv:9876` | Dashboard及应用的服务发现地址 |
| RocketMQ Broker | `broker:10911` | 客户端通过NameServer发现；worker无独立HTTP入口 |
| Loki | `http://loki:3100` | Grafana的`Loki`数据源，日志由Alloy采集写入 |
| Prometheus | `http://prometheus:9090` | Grafana的`Prometheus`数据源；宿主通过Explore查询 |
| Alloy | `http://alloy:12345` | 内部采集状态及`/metrics`端点 |
| Grafana | `http://grafana:3000` | 容器内部界面及`/metrics`端点，宿主使用13000 |
| PostgreSQL Exporter | `http://postgres-exporter:9187/metrics` | `--middleware-metrics`显式启用，依赖独立监控身份/口令 |
| Redis Exporter | `http://redis-exporter:9121/metrics` | 同上，不采集业务键值 |
| RocketMQ原生指标 | `http://broker:5557/metrics` | Broker已启用PROM接口；需`--middleware-metrics`让Prometheus抓取 |
| Node Exporter | `http://node-exporter:9100/metrics` | 仅Linux使用`--linux-host`启用；macOS不支持以Docker VM指标代替宿主指标 |
| Docker日志只读代理 | `docker-log-proxy:2375` | Alloy内部读取Docker元数据与日志，无浏览器管理入口 |

扩展身份准备和查询示例见[监控操作说明](../observability.md)，组件开关见[启停说明](../operations.md)。上述服务由真实Docker组件提供；当前范围为基础设施及集成探针，不包含业务认证、真实通知闭环或生产验收。

`make run`是另一种不连接中间件的宿主健康端点模式，地址为[http://127.0.0.1:8080/healthz](http://127.0.0.1:8080/healthz)，不使用Docker开发API的18080端口。

本节文档验证（2026-10-20）：已逐项核对Compose端口映射、Grafana看板UID、数据源配置和启停脚本默认值；`make fmt`、`make check`（格式检查、vet、race/coverage测试）及`git diff --check`通过。本次仅更新访问说明，未启动服务或执行HTTP连通性验证，不将配置地址视为当前服务在线证明。

### 3. 查看日志和停止

```sh
dc ps
dc logs -f api worker
# 仅关闭管理工具。
dc stop pgadmin redisinsight rocketmq-dashboard
# 停止并移除本项目容器/网络，保留数据卷。
dc down
```

再次启动沿用同一项目名、凭据和数据卷。不要为解决口令不匹配直接执行`down -v`或删除旧卷。

## 未来生产部署流程（T003/T004；REQ-018、020）

当前仅提供**中间件生产部署骨架**；以下为未来获准部署时的操作流程，本次没有执行生产部署。当前生产Compose不含业务API/worker；认证、领域发件箱、客户端凭据接入、最小权限角色、真实外部资源及备份恢复尚未完成，不能按此启动完整产品或宣布上线。

### 1. 固定制品与准备环境

运维准备独立Docker生产主机、足够磁盘及受控管理入口，检出经过验证的发布提交，保留Compose和配置文件的相对目录。镜像使用仓库固定digest；开发与生产不得共用项目、卷或凭据。本骨架为同宿主单PG/单Redis/单Broker；跨宿主链路需另配TLS和网络策略，高可用/容量须独立验收。

部署前完成对应版本的质量检查、真实账号鉴权正反例，以及D05/T035外部备份/删除日志/恢复验收。容器重启策略和持久卷不能替代备份；管理工具卷也可能含连接信息及查询记录。

### 2. 交付仓库外secret文件

由凭据管理方在生产主机安全下发以下文件，不使用本地开发口令、不将密钥值写入README、Git或命令历史。Compose文件secret只是只读挂载，不自动加密宿主文件；由运维按目录访问权限及容器UID落实可读性。

| 文件 | 必需内容 |
| --- | --- |
| postgres-password | PostgreSQL初始化管理口令；应用不得使用该管理角色 |
| redis.acl | 关闭无密码default账号；为应用与管理工具分别配置口令哈希、键范围和命令权限 |
| plain_acl.yml | MQ5.3.2 ACL1.0配置；不设全局白名单，应用默认权限DENY，再按主题/消费组授权；管理账号独立 |
| pgadmin-password | 独立pgAdmin管理口令 |
| dashboard.properties | Spring属性`rocketmq.config.accessKey`、`rocketmq.config.secretKey`，对应MQ管理凭据 |

以下路径为交付位置示例，须替换为实际路径和管理邮箱；命令不会创建或生成生产凭据：

```sh
export COMPOSE_PROJECT_NAME=liyuanci-production
export LIYUANCI_POSTGRES_PASSWORD_FILE=/etc/liyuanci/secrets/postgres-password
export LIYUANCI_REDIS_ACL_FILE=/etc/liyuanci/secrets/redis.acl
export LIYUANCI_MQ_ACL_FILE=/etc/liyuanci/secrets/plain_acl.yml
export LIYUANCI_PGADMIN_EMAIL=operator@example.com
export LIYUANCI_PGADMIN_PASSWORD_FILE=/etc/liyuanci/secrets/pgadmin-password
export LIYUANCI_DASHBOARD_PROPERTIES_FILE=/etc/liyuanci/secrets/dashboard.properties

prod() {
  docker compose -f compose.production.yaml "$@"
}
prod_admin() {
  docker compose -f compose.production.yaml \
    -f compose.management.yaml --profile management "$@"
}
```

### 3. 校验、拉取并启动中间件

以下只在获准的生产部署窗口执行；不要输出不带`--quiet`的展开配置，它可能包含敏感信息。

```sh
prod config --quiet
prod_admin config --quiet
prod pull
prod_admin pull pgadmin redisinsight rocketmq-dashboard
prod up -d --wait postgres redis namesrv broker
prod ps
# 按需启用管理工具。
prod_admin up -d --wait pgadmin redisinsight rocketmq-dashboard
prod_admin ps
```

验证Redis无凭据请求被拒绝，并用实际专用账号验证允许/禁止的操作；MQ同样验证未授权写入拒绝和授权主题/消费组可用；PG使用实际最小权限角色验证。容器healthy只说明基础探活，不证明业务授权或备份恢复正确。生产PG口令文件只在新卷初始化时生效，已有数据库改密须执行数据库改密流程；pgAdmin初始化口令同理。

### 4. 经SSH访问管理页面

在运维电脑执行，将`operator@production-host`替换为实际SSH目标：

```sh
ssh -N \
  -L 15050:127.0.0.1:15050 \
  -L 15540:127.0.0.1:15540 \
  -L 18081:127.0.0.1:18081 \
  operator@production-host
```

随后访问上方管理地址；pgAdmin和Redis Insight使用各自生产账号连接容器内postgres:5432、redis:6379。Dashboard通过外部properties访问开启ACL的Broker，该真实生产凭据组合仍待联调。SSH账号及生产主机访问权限属于管理权限边界，管理端口保持loopback绑定，不直接对公网发布。

### 5. 业务发布、升级及停止

业务发布仍需T005显式迁移/最小权限角色、T007可靠领域worker、T008/T011认证和T033–T038真实集成/恢复/性能/交付验收。后续须提供独立生产API/worker编排、密钥及客户端凭据适配、显式迁移命令和业务就绪检查；当前不存在可执行的完整业务上线命令。不得在生产运行集成`migrate`、`probe`或集成探针worker来替代这些步骤。

升级前记录发布提交、镜像digest、配置及secret引用，验证外部备份和恢复路径；数据库版本、DDL和数据格式变更须使用该版本的迁移/回滚方案，不能仅切旧镜像或删除数据卷。获准后按依赖顺序更新并复验，失败时按已验证的恢复方案处理。

```sh
# 日常状态及技术日志（不要输出环境变量或secret内容）。
prod ps
prod logs --tail 100 postgres redis namesrv broker
# 关闭管理服务不影响中间件。
prod_admin stop pgadmin redisinsight rocketmq-dashboard
# 仅在获准维护窗口停止整个生产项目；保留卷。
prod_admin down
```

本文新增的是操作说明，命令与当前Compose/secret/探针入口已核对；没有执行上述生产命令。现有隔离实测结果见[验证记录](../validation.md)，不冒用为真实生产部署通过。

### Docker依赖下载超时处理（T004；REQ-020）

若构建在`go mod download`报`proxy.golang.org ... i/o timeout`，原因是构建容器无法连接官方Go代理。已在本机复现官方代理超时并验证goproxy.cn可达；Dockerfile与集成Compose默认统一为`https://goproxy.cn,direct`，保留go.sum及GOSUMDB校验，不修改依赖版本或关闭校验。

先前执行过本地启动章节、已定义`dc`的终端，可执行：

```sh
# 覆盖终端内可能遗留的旧代理设置，重新构建并启动。
export LIYUANCI_BUILD_GOPROXY=https://goproxy.cn,direct
dc --progress plain build api worker
dc up -d --wait api worker
```

直接使用Docker构建时可执行`docker build --progress=plain --build-arg GOPROXY=https://goproxy.cn,direct -t liyuanci-backend:local .`。能访问官方代理的环境可通过`LIYUANCI_BUILD_GOPROXY=https://proxy.golang.org,direct`（Compose）或`--build-arg GOPROXY=https://proxy.golang.org,direct`（docker build）覆盖；构建参数只作用于镜像构建，不由运行期`APP_`配置控制。检查失败详情时保留`go mod download`上方错误，而非仅查看最终exit code。

本次修复已通过实际无缓存builder构建：依赖下载13.7秒、API/worker/probe编译9.8秒；`make fmt/check/build`及Compose静态检查通过。见[验证记录](../validation.md)，不是仅使用旧依赖层缓存判断通过。
## 统一日志采集与查询（T004/REQ-018、020）

新增独立`compose.observability.yaml`：Alloy通过限制GET读取接口的Docker代理采集指定Compose项目日志，发送到单实例Loki，Grafana提供统一查询和预置技术日志看板。应用继续输出zap JSON，中间件保留原文；索引只保留环境、服务、安全主机别名，请求ID在查询时解析。采集端丢弃命中明显凭据模式的整行日志，源端仍须禁止敏感信息。采集位置、日志与界面状态分别持久化，不依赖业务数据库。

启动前设置目标项目、环境、主机别名和仓库外Grafana密码文件，再运行`docker compose -p liyuanci-observability-dev -f compose.observability.yaml up -d`。Grafana仅开放`127.0.0.1:13000`，账号`operator`；完整本地启动、生产配置/SSH访问、查询及停止流程见[运维说明](../observability.md)。默认技术日志保留72小时，生产建议显式336小时；Compactor异步清理，不能替代磁盘告警。单实例不承诺HA/零丢失，不承担D05删除日志职责。

验证入口：`python3 scripts/check-observability.py`；真实隔离验证PASS（项目`liyuanci-logs-6f2e6e6233`）：请求ID查询、固定标签、敏感行阻断、项目隔离、代理越权拒绝、Alloy/Loki重启查询及新日志采集、预置看板。`make fmt/check/build`通过；长期保留清理、生产容量/安全验收未完成。

## 日志告警与存活心跳（T004/REQ-018、020）

API监听、worker消费者启动成功后，每30秒通过注入的zap.Logger输出无业务数据心跳；停止函数等待协程退出，保证先停止心跳再清理日志器。HTTP完成和panic增加稳定event字段，保留原请求ID及状态。心跳需Info或Debug日志级别，不代表业务ready或T007可靠消费完成。

启动日志设施时叠加`-f compose.alerting.yaml`，启用panic、排除健康检查的5xx比例、worker错误及API/worker心跳缺失五条规则；文件管理规则、Grafana负责求值，数据源失败也进入告警。默认没有真实通知渠道；`compose.notifications.yaml`提供仓库外通知配置入口。用户选择先本地验证，真实通知、值班负责人及平台失联外部检测仍待交付。具体阈值、启停、分组/静默、处置与生产接入见[运维说明](../observability.md)。

`python3 scripts/check-observability.py --alerts`真实隔离PASS（`liyuanci-logs-0d45838f71`）：合成panic触发Grafana告警，隔离Webhook收到firing，故障消失后收到resolved。未向外部地址发送消息；不等于五条规则全部边界/真实通知验收。心跳停止/重复停止、HTTP正常/失败/panic事件回归及`make fmt/check/build`通过。

## 运行指标（T004/REQ-018、020）

叠加`-f compose.metrics.yaml`启用固定版本Prometheus3.8.0、独立数据卷、Grafana指标数据源/看板及指标告警。每15秒抓取Alloy/Loki/Grafana/Prometheus自身，数据保留7天或1GB（先触及的TSDB限制生效，并非整个卷硬上限）；9090不映射宿主。覆盖抓取失败、日志投递丢弃和敏感日志阻断，规则每20秒求值。日志与指标组合共享Grafana，仍需外部渠道及平台失联检测。

Linux宿主额外叠加`-f compose.metrics.linux.yaml`，固定Node Exporter1.10.2以只读挂载读取宿主CPU/内存/磁盘，配置内存>90%与磁盘可用<15%的持续告警；无宿主网络/管理端口。它是高信任宿主读取组件，仅Linux使用；macOS Docker虚拟机指标不代表macOS宿主。未启用metrics时指标看板明确提示所需叠加文件，日志查询仍可单独使用。生产启动/验收边界见[运维说明](../observability.md)。

真实隔离`--metrics`验证PASS（`liyuanci-logs-d2523d7a8e`）：四目标up=1、敏感行丢弃计数可查询、Prometheus重启后恢复查询；`--alerts --metrics`组合验证PASS（`liyuanci-logs-ecbab0b171`）。promtool检查基础配置、Linux目标JSON静态检查、Compose静态组合和`make fmt/check/build`通过。Linux宿主运行验收、长期清理和生产容量未执行；CI增加Linux验证入口，远端CI结果未运行。业务指标仍待对应领域实现，不能从up=1推导业务健康。

## 中间件内部指标与生产监控配置（T004/REQ-018、020）

叠加`-f compose.metrics.middleware.yaml`连接明确指定的Docker foundation网络，PostgreSQL/Redis使用固定社区exporter、独立`liyuanci_monitor`统计身份及仓库外secret；PostgreSQL关闭设置/SQL语句指标，Redis只允许PING/INFO，不读取业务键值。Broker5.3.2原生启用PROM指标，5557/9187/9121均不发布宿主。Grafana预置中间件看板，新增连接/采集错误告警，现有up规则覆盖各端点；Linux与中间件目标通过file_sd叠加组合。

接入前需显式创建监控身份并提供`LIYUANCI_MONITORED_NETWORK`、`LIYUANCI_PG_MONITOR_PASSWORD_FILE`、`LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE`；Redis密码JSON的键包含用户名。生产监控额外叠加`compose.observability.production.yaml`，必须提供持久状态加密密钥、界面URL和明确日志保留期，禁止依赖内建默认密钥；身份准备、本地开发、生产启动/停止和恢复边界详见[操作步骤](../observability.md)。Broker配置更新需重启生效，当前未操作生产。

`python3 scripts/check-management.py --middleware-only --metrics`真实隔离PASS，最终项目`liyuanci-mgmt-bee10a5fb347`：七目标up=1、PG/Redis连接成功且采集错误为0、MQ原生指标、错误监控密码被识别并恢复、Redis监控身份GET被拒绝；生产形态监控密钥注入、中间件看板和四条规则求值也通过。`sh scripts/integration.sh`PASS（`liyuanci-t004-20261019232016-2f878e`），启用Broker指标后消息重复发送仍由独立worker完成两次数据库回执，API健康边界保持正确。全部Compose叠加静态检查与`make fmt/check/build`通过。真实生产凭据、Linux宿主运行、通知、容量、长期清理和T007领域阈值仍未验收；未推进整体T004或认证完成状态。

## 启动与停止脚本（T004/REQ-018、020）

`scripts/init-dev.sh`、`start.sh`、`stop.sh`共用Python3.9+控制器，任意工作目录可调用。初始化只补仓库外缺失凭据，旧数据卷缺原口令时拒绝生成；环境文件按字面读取而非source。启动先预检全部选中配置，再启动中间件、开发API/独立worker、管理工具和独立监控。普通启动不迁移，`--bootstrap-probe`与`--probe`仅用于显式开发探针；生产只启动既有中间件/监控骨架。

停止不需要凭据文件，按项目标签、依赖顺序停止，API/worker退出时采集仍在线；默认保留容器/网络/卷，`--remove`也只移除容器/网络，不删卷、不prune。启动失败保留已启动资源，结构化完成记录不回显Docker输出或秘密。支持组件开关、外部环境文件、自定义项目、计划预览及Linux/中间件指标等显式扩展。[完整操作说明](../operations.md)包含本地/生产命令、参数、失败排查；生产配置模板为`configs/operations.production.env.example`，真实通知与生产准入保持待验收。

验证入口：`python3 -m unittest discover -s scripts -p test_services.py -v`（13项边界PASS）、`python3 scripts/check-services.py`（2026-10-20真实全栈PASS，项目`liyuanci-dev-ops-e8a5ec59c3`）。已验证初始化幂等、默认管理/监控页面、真实API日志查询、探针往返、停止保留、无隐式迁移重启和remove保留卷；`make fmt/check/build`、shell/Python语法检查及生产计划预览通过。真实生产启动、Linux/真实通知扩展未在本功能执行，远端CI未运行。本功能不修改API契约0.1.0，不推进整体T004或认证状态。

## 2026-10-21 其他模块 ORM 统一增量

T004、REQ-018/020：探针回执改用实体及 GORM OnConflict 原子累加，探针实体读取消费计数；消息仍在 PostgreSQL 写入成功后确认，中文结构化完成日志保留。完整范围、设计取舍、兼容性修复、真实/合成状态与本轮实际验证见[ORM 专题后续增量](orm-refactor-2026-10-21.md#后续模块-orm-统一增量)。本次不推进生产或完整领域业务验收。
