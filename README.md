# 鲤源池 Go 后端

T001–T004第一批实施；REQ-001、018、020。固定方案B：Go模块化单体API＋独立worker＋PostgreSQL事务发件箱＋Redis＋RocketMQ。当前为工程基础，领域发件箱与认证未实现，不作为生产服务。

共享契约入口：[docs/contracts/README.md](../docs/contracts/README.md)，版本0.1.0；80个业务操作均为定义，不是已实现接口。后端不内置假登录或成功业务Mock；两端可基于合成示例独立准备夹具，必须标明模拟。

开发规范：每个功能的完整修改完成后，必须在本 README 同步说明实现功能、实际行为与设计，关联任务 ID/REQ，并记录验证结果、未完成部分及真实/模拟状态；详细设计可链接展开。该要求已写入 [AGENTS.md](AGENTS.md)，作为功能完成及提交前的必要条件。

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

完整集成顺序：固定镜像→专用MQ卷权限初始化与真实PG/Redis/NameServer/Broker→显式探针迁移及主题创建→API/独立worker→双生产同一随机事件→PostgreSQL单回执行投递计数至少2。worker仅在integration环境消费专用主题`liyuanci_foundation_probe_v1`；数据库提交后才确认，未知格式有限重试。此链不代替T007的领域幂等/取消/发件箱故障测试。完整往返已通过：官方MQ5.3.2＋Go SDK2.1.2，实际Go1.25容器构建与非root运行。用户已决定放弃4.9.8，统一采用当前固定digest的5.3.2，见[版本决定](docs/middleware-decision.md)；不签T007或性能通过。

配置使用APP_环境变量；DSN、密钥、真实令牌不写仓库/日志。`configs/integration.yaml`无凭据；生产配置与隔离配置分开，缺少真实依赖不能静默变成Mock。API通过独立ingress网络仅绑定loopback端口18080，PG/Redis/MQ保持内部网络。集成脚本默认每次新建隔离项目，完成或失败均清理本次容器/网络并保留卷；显式指定项目时自行管理凭据与生命周期。容器构建保留go.sum，不在Dockerfile tidy；显式迁移不在启动执行，无AutoMigrate。

验证、阻塞和交接见[docs/validation.md](docs/validation.md)、[环境与D01–D06](docs/environment.md)、[批次状态及下一批入口](docs/implementation.md)。CI执行相同质量检查，独立middleware任务执行真实隔离集成；工作流尚未在远程运行，不代表CI通过。本地提交按用户指令分批执行；未推送、部署或上线。

## Docker 中间件与管理工具（T003/T004；REQ-018、020）

开发集成与未来生产的PostgreSQL、Redis、RocketMQ统一使用Docker部署。原`compose.integration.yaml`保持隔离探针用途；新增`compose.production.yaml`独立项目/卷，仅编排中间件，通过仓库外secret文件启用PG口令、Redis ACL和MQ ACL，不包含未实现的业务API/worker。数据库仍为权威，迁移仍显式执行。

`compose.management.yaml`为两种环境提供可选`management` profile，使用固定digest的官方pgAdmin4 9.18、Redis Insight3.8.0、Apache RocketMQ Dashboard2.1.0。页面仅绑定本机15050、15540、18081，中间件不开放宿主端口；远程经SSH隧道。pgAdmin使用独立管理口令，连接凭据不写入仓库；生产Dashboard的MQ凭据通过外部properties文件注入。

配置变量、连接地址、启停与生产限制见[Docker中间件操作说明](docs/docker-middleware.md)。`python3 scripts/check-management.py`检查两套编排边界，并在唯一临时集成项目启动三款工具、验证HTTP和Broker发现；完成后清理容器/网络、保留卷。该检查不替代原`sh scripts/integration.sh`的生产消费往返，也不验证生产ACL。生产客户端凭据接入、最小权限角色、备份恢复和上线验收仍待后续任务，本批不部署生产。

本次`make fmt/check/build`、两套Compose边界检查及生产形态隔离中间件验证已通过，包含Redis正确口令/匿名拒绝和MQ匿名写入拒绝；真实生产凭据未验。三款管理页面HTTP检查和Dashboard真实Broker发现也已通过；测试结束已停止容器并清理网络、保留卷。详细结果见[验证记录](docs/validation.md)；不改变契约0.1.0或T004整体待两端验收的状态。

## 本地开发启动（T003/T004；REQ-018、020）

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

| 入口 | 本机地址 | 登录及连接方式 |
| --- | --- | --- |
| API存活 | http://127.0.0.1:18080/healthz | 预期200；`/readyz`当前预期503，不表示认证完成 |
| pgAdmin | http://127.0.0.1:15050 | 使用上述邮箱及pgadmin-password文件内口令登录；添加主机postgres、端口5432、数据库/用户名liyuanci，数据库口令来自postgres-password文件 |
| Redis Insight | http://127.0.0.1:15540 | 添加redis:6379、DB0；仅本地集成环境无密码 |
| RocketMQ Dashboard | http://127.0.0.1:18081 | 已配置namesrv:9876，集成环境Broker无ACL |

`postgres`、`redis`、`namesrv`是容器网络内地址，供管理工具连接；宿主没有开放数据库/MQ端口。`make run`是另一种不连接中间件的宿主健康端点模式，监听127.0.0.1:8080，不等同于上述完整Docker开发环境。

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

本文新增的是操作说明，命令与当前Compose/secret/探针入口已核对；没有执行上述生产命令。现有隔离实测结果见[验证记录](docs/validation.md)，不冒用为真实生产部署通过。

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

本次修复已通过实际无缓存builder构建：依赖下载13.7秒、API/worker/probe编译9.8秒；`make fmt/check/build`及Compose静态检查通过。见[验证记录](docs/validation.md)，不是仅使用旧依赖层缓存判断通过。
