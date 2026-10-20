# 统一可观测性实施（T004/REQ-018、020）

## 模块一：技术日志

独立Compose项目部署Alloy→Loki→Grafana，不加入业务网络，不修改中间件数据。Alloy只发现`LIYUANCI_LOG_PROJECT`指定的Compose项目。服务标签由Compose元数据派生，索引只保留environment/service/host；request_id在查询时JSON解析，避免高基数索引。当前保留中间件原文，应用JSON在查询时解析；尚未实现各中间件专用解析器。

范围限Docker可见stdout/stderr，RocketMQ等容器内部独立文件日志未挂载采集，不宣称覆盖全部中间件诊断日志。后续接入文件日志时须单独设置持久卷、读取权限、轮转和解析，不挂载整个宿主文件系统。

受限HAProxy仅允许Docker容器列表、日志、inspect、网络及版本/健康信息的GET/HEAD，拒绝写入、exec、archive与镜像接口。Alloy实际需要inspect判断TTY，因此代理和Alloy均属于宿主高信任组件，可读取包含环境变量的容器元数据；不会把元数据当日志转发。Docker socket只挂载到代理，不能向其他服务/公网开放。项目过滤是采集范围控制，不是Docker API的租户权限控制；不同权限租户应分宿主部署。Alloy读取位置、Loki TSDB/compactor和Grafana配置状态各自持久化。独立一次性容器只初始化日志卷属主，Loki保持非root运行。

源端禁止敏感数据。采集端遇到authorization/password/secret/cookie、PostgreSQL URI或签名参数丢弃整行，并记录丢弃计数；这是兜底启发式，可能误丢合法日志，不能保证识别全部个人信息。不要往日志里放请求体、SQL参数、身份数据或秘密。普通技术日志不等于D05独立删除日志/审计，泄漏需针对性清理。

### 本地启动

先按README启动业务开发环境；在仓库目录另开终端执行：

```sh
monitor_dir="$HOME/.config/liyuanci/observability-dev"
umask 077
mkdir -p "$monitor_dir"
if [ ! -s "$monitor_dir/grafana-password" ]; then
  openssl rand -hex 24 > "$monitor_dir/grafana-password"
fi
chmod 700 "$monitor_dir"
# 父目录限制宿主访问；容器中的Grafana用户需要读取挂载的secret。
chmod 644 "$monitor_dir/grafana-password"
export LIYUANCI_GRAFANA_PASSWORD_FILE="$monitor_dir/grafana-password"
export LIYUANCI_LOG_PROJECT=liyuanci-dev
export LIYUANCI_LOG_ENVIRONMENT=development
export LIYUANCI_LOG_HOST=local-dev
# Linux通常默认/var/run/docker.sock；macOS可明确设置Docker context对应socket。
export LIYUANCI_DOCKER_SOCKET=/var/run/docker.sock
export LIYUANCI_LOG_RETENTION=72h
obs() { docker compose -p liyuanci-observability-dev -f compose.observability.yaml "$@"; }
obs config --quiet
obs up -d
obs logs --tail 100 alloy loki grafana
```

Grafana为`http://127.0.0.1:13000`，账号`operator`，密码为外部文件中保存的随机值。不要把密码粘贴到命令行参数或日志。首次初始化后更换文件不会自动重置已有Grafana账号密码。预置「鲤源池技术日志」看板；Explore选择Loki：

```logql
{environment="development",service="api"} | json | request_id="待定位请求ID"
```

`obs down`停止容器/网络并保留卷；不要加`-v`。采集器停机超过Docker轮转保留窗口会丢日志；发送重试/位置文件不代表端到端恰好一次或无限缓冲。初期文件系统单实例不提供HA/灾备承诺。

### 未来生产

在独立Docker宿主配置不同项目名、凭据文件和安全主机别名；`LIYUANCI_LOG_PROJECT=liyuanci-production`、`LIYUANCI_LOG_ENVIRONMENT=production`、建议`LIYUANCI_LOG_RETENTION=336h`，其他启动顺序同上。启动前检查固定制品、宿主磁盘预算、轮转窗口与外部备份/删除边界。Loki没有内建对外认证，本配置只允许内部telemetry网络访问；Grafana有登录并仅绑定loopback，经SSH `-L 13000:127.0.0.1:13000`访问。按需创建只读查看账号，禁止共享管理员身份。本次不执行生产部署。

Compactor按保留期异步清理，不会因为磁盘接近满自动提前删除；需后续指标告警。配置可验证不等于已经等待72小时并完成删除实测。保留期不改变批准的业务副本生命周期。

### 验证

`python3 scripts/check-observability.py`创建唯一隔离项目、随机凭据与合成日志，验证Docker采集→Grafana认证代理查询、固定标签、敏感行阻断、外项目隔离、Alloy/Loki重启查询与预置看板。退出清理本次容器/网络、保留专用卷，临时凭据删除；不触碰其他容器。重启查询测试不证明所有故障场景无丢失/无重复；长期保留清理需单独演练。

2026-10-19模块一实际PASS，项目`liyuanci-logs-6f2e6e6233`；本机原始记录`/tmp/liyuanci-observability-check-v4.log`，交付不依赖临时文件。已验证代理拒绝archive/镜像读取/POST、重启后继续采集新日志。`make fmt`、`make check`、`make build`通过；Alloy validate、Loki verify-config、HAProxy配置校验通过。

制品为Alloy1.14.0、Loki3.6.0、Grafana12.3.0、HAProxy3.2.14，均固定官方多架构digest；本机实际验证arm64。HAProxy3.2.26的镜像层多地址持续停滞，选择已下载的同系列3.2.14，不代表安全扫描或生产制品准入通过。镜像加速仅用于下载相同digest，配置仍引用官方源。首次真实启动暴露Loki卷属主、Alloy所需inspect权限及Loki自动派生标签，均已修复后重跑；未放宽断言或开放Docker写API。

官方依据：[Alloy Docker采集](https://grafana.com/docs/alloy/latest/reference/components/loki/loki.source.docker/)、[Loki保留期](https://grafana.com/docs/loki/latest/operations/storage/retention/)。本扩展不改变业务契约版本0.1.0，也不把三端整体T004标记完成。

## 模块二：日志告警与存活心跳

T004/REQ-018、020：API成功绑定监听、worker成功启动消费者后，每30秒以注入zap.Logger输出`event=telemetry_heartbeat`、service、result=alive，不包含身份/正文；停止函数等待日志协程退出，先于日志器清理。心跳只表示进程活着，不承诺数据库/MQ就绪。HTTP完成日志增加稳定事件`http_request_completed`，异常恢复增加`http_panic`，保留请求ID和最终状态。

显式叠加`compose.alerting.yaml`，Grafana每10秒求值：

| 规则 | 条件 | 说明 |
| --- | --- | --- |
| API panic | 近1分钟至少一次 | 立即进入告警，不记录panic正文 |
| API 5xx | 5分钟至少20次请求且失败比例>5%，持续2分钟 | 排除healthz/readyz；按status判断，不依赖info/error |
| worker错误 | 5分钟至少5次error，持续1分钟 | 当前仅探针/生命周期，不代表T007可靠消费 |
| API/worker心跳缺失 | 近3分钟无心跳，再持续1分钟 | 无业务流量也可检测；进程退出、采集失败需进一步排查 |

规则无错误样本时正常，心跳用零值判断缺失；求值/数据源失败进入Alerting。阈值是技术初值，不改批准的性能或业务时限。T007发件箱积压、死信和重试耗尽尚未实现，不能添加空壳告警冒充完成。当前readyz固定503，不能作为故障依据。

保留模块一环境变量，将启动函数改为：

```sh
obs() { docker compose -p liyuanci-observability-dev -f compose.observability.yaml -f compose.alerting.yaml "$@"; }
obs config --quiet
obs up -d
```

更新Go心跳代码后需重新构建并启动开发API/worker，才会有对应心跳。未启动这些服务时心跳规则会正常报缺失，开发仅查日志可不叠加告警。Grafana→Alerting查看规则状态。规则由文件管理，修改文件后重启Grafana；不要只在UI改临时状态。处理步骤：先检查请求ID或目标服务，再检查Alloy/Loki/Docker连接与磁盘，定位后修复；维护窗口使用有时限的silence，禁止永久屏蔽。

用户选择先本地验证。默认没有真实SMTP/Webhook配置，Grafana内置默认联系点不构成通知闭环；未配置SMTP时通知尝试会失败，不得当作通知成功。测试脚本`python3 scripts/check-observability.py --alerts`只创建隔离Python HTTP接收器、临时联系点/策略，验证合成panic真实触发和恢复JSON，不联系外部人员。测试接收器不公开宿主端口，退出删除临时通知数据。

未来真实Webhook：将`configs/observability/notifications.example.yaml`复制到仓库外，填入核验后的通知地址与必要鉴权，设置`LIYUANCI_ALERT_NOTIFICATIONS_FILE`，启动命令再叠加`-f compose.notifications.yaml`；凭据不出现在Compose环境、仓库或示例中。该文件同时管理联系点和分组/重复通知策略。与告警规则一同重建Grafana后，必须真实验证触发、恢复、分组、静默及失败处置，之后才能签通知通过。邮件需另行配置SMTP及secret；当前没有实际邮件配置。生产仍需值班负责人和独立平台失联检测，不能只依赖同一Grafana自我报警。

心跳要求应用Info或Debug日志级别，否则会被logger过滤；不强行覆盖运营配置。初始阈值尚未做全部流量/时间边界演练。2026-10-19 `python3 scripts/check-observability.py --alerts`实际PASS，项目`liyuanci-logs-0d45838f71`，本机记录`/tmp/liyuanci-alerting-check-v2.log`；合成panic真实触发、隔离Webhook收到firing及resolved。首次Alpine缺少httpd的测试接收器已替换为固定官方Python镜像；接收器仅测试使用，不部署生产。心跳生命周期/重复停止、HTTP事件字段测试以及`make fmt/check/build`通过。

依据：[Grafana文件化告警](https://grafana.com/docs/grafana/latest/alerting/set-up/provision-alerting-resources/file-provisioning/)、[No Data/Error处理](https://grafana.com/docs/grafana/latest/alerting/fundamentals/alert-rule-evaluation/nodata-and-error-states/)。

## 模块三：监控链路及Linux宿主指标

T004/REQ-018、020：独立Prometheus每15秒抓取监控组件，无API/业务/中间件空壳指标；通过固定UID数据源在Grafana查询`up`、`loki_write_dropped_entries_total`和`loki_process_dropped_lines_total`。TSDB设置7天和1GB双限制，WAL/head等仍占额外空间，不能当作宿主磁盘硬配额。数据卷独立，停止保留。基础告警每20秒求值，符合Grafana10秒调度粒度；首次15秒间隔被真实运行拒绝，已修正并复验。

本地使用已有密码/环境变量，把函数改为：

```sh
obs() { docker compose -p liyuanci-observability-dev -f compose.observability.yaml -f compose.alerting.yaml -f compose.metrics.yaml "$@"; }
obs config --quiet
obs up -d
```

Grafana指标看板和Explore选择Prometheus；`up`为抓取端点可达，不等于业务健康。日志发送丢弃/敏感行阻断增加即告警；任何监控抓取目标持续1分钟up<1告警。未启用Linux采集时磁盘/内存规则无数据视为正常，不能据此签宿主正常。

仅Linux生产宿主，启动命令再叠加`-f compose.metrics.linux.yaml`。Node Exporter只启用CPU/meminfo/filesystem/uname，pid使用宿主命名空间，/proc、/sys、根文件系统只读挂载，无宿主监听端口、不直接接触Docker socket；它能读取宿主元数据，是高信任组件。CPU可查`1-avg by(instance)(rate(node_cpu_seconds_total{mode="idle"}[5m]))`；内存使用>90%持续5分钟、磁盘可用<15%持续5分钟告警。处置时先确认挂载点/设备、日志增长和Docker轮转，不自动删除业务卷或扩大保留期。首次生产启用需确认所有日志卷所在设备被覆盖，不能只看根盘。

`python3 scripts/check-observability.py --metrics`实际PASS，项目`liyuanci-logs-d2523d7a8e`：四目标全部up=1、阻断计数和看板可查、Prometheus重启后恢复；组合项目`liyuanci-logs-ecbab0b171`日志/指标/本地通知全链路PASS。基础配置经固定镜像promtool校验、Linux目标JSON静态检查通过，全部Compose组合静态检查通过。`--linux-host`明确拒绝macOS宿主；GitHub Linux CI已配置运行`--alerts --metrics --linux-host`，远端执行结果未验证。生产Linux宿主、全部磁盘覆盖、阈值边界、保留期/容量和平台失联检测仍待实际环境验收。

官方依据：[Prometheus Docker部署](https://prometheus.io/docs/prometheus/latest/installation/)、[TSDB保留](https://prometheus.io/docs/prometheus/latest/storage/)、[Node Exporter](https://github.com/prometheus/node_exporter)。

## 模块四：中间件内部指标

T004/REQ-018、020：显式叠加`compose.metrics.middleware.yaml`，PostgreSQL由Prometheus社区postgres-exporter0.18.1采集，Redis由上游redis_exporter1.77.0采集，两者固定digest、独立监控账号和外部secret文件。RocketMQ5.3.2直接启用Broker原生`metricsExporterType=PROM`，不是额外Java exporter，也不要求Go SDK提供指标。只采Broker端指标，未接入客户端指标或T007领域发件箱指标。

Prometheus通过独立file_sd文件组合可选目标，可同时叠加Linux宿主采集，不覆盖主配置。监控项目只连接显式指定的单个业务foundation网络；不发布5557/9187/9121。Broker配置变化需在开发重建/生产批准的维护窗口重启Broker生效，不自动操作生产。原生指标端点无额外认证，只能保留在受控内部网络，不能改为公网端口。

### 账号与配置准备

PostgreSQL管理员在目标库显式创建`liyuanci_monitor`登录角色（NOSUPERUSER、NOCREATEDB、NOCREATEROLE、NOREPLICATION、NOBYPASSRLS），授予`pg_monitor`和目标库连接权限。可在已授权的交互psql里执行角色/授权SQL后，用`\password liyuanci_monitor`安全提示设置密码，避免口令出现在shell参数/历史中。监控口令保存到仓库外文件，由`LIYUANCI_PG_MONITOR_PASSWORD_FILE`引用；不使用bootstrap超级用户或业务账号采集。`pg_monitor`仍可读取运行统计/SQL元数据，账号和exporter属于高信任组件；当前关闭设置指标和stat_statements，不采SQL正文。

```sql
CREATE ROLE liyuanci_monitor LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT pg_monitor TO liyuanci_monitor;
GRANT CONNECT ON DATABASE liyuanci TO liyuanci_monitor;
\password liyuanci_monitor
```

如果角色已存在，先核对现有用途/权限，不覆盖或复用未知身份；上述SQL不是启动迁移，也不嵌入API/worker。

Redis准备`liyuanci_monitor`：只允许`PING`和`INFO`，不授予键模式、GET/SCAN、CONFIG、Lua或客户端列表权限。生产将以下规则中的密码哈希替换为外部监控密码的SHA-256，追加到既有外部ACL文件，保留原有业务/管理身份及`default off`：

```text
user liyuanci_monitor on #<外部监控密码的SHA-256> -@all +ping +info
```

Redis exporter禁用CONFIG查询、客户端列表、延迟直方图与业务键值采集；不因为导出器需要统计就扩大到+@all。另准备仓库外JSON密码文件，环境变量为`LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE`。URL键必须包含用户名：

```json
{"redis://liyuanci_monitor@redis:6379": "<由密钥管理工具写入的真实监控密码>"}
```

上面仅结构示例，不能直接当口令使用；文件内容不入仓库/日志。所有secret所在宿主目录0700、挂载文件需对容器的非root进程可读（当前文件型secret示例0644，目录限制宿主访问）。更新口令需同步目标账号与secret并重启exporter，检查pg_up/redis_up和last_scrape_error，不能只检查HTTP up。

### 本地开发接入

先按README启动真实开发中间件。使用管理员连接创建上述PostgreSQL统计身份；Redis开发集成没有ACL文件，需在隔离开发实例显式设置监控身份。可在仓库终端用以下命令从外部JSON文件生成仅含哈希的ACL命令，经stdin送入开发容器，不将密码放入参数：

```sh
export LIYUANCI_PG_MONITOR_PASSWORD_FILE="$HOME/.config/liyuanci/dev/pg-monitor-password"
export LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE="$HOME/.config/liyuanci/dev/redis-monitor-passwords.json"
python3 - <<'PY'
import hashlib,json,os,subprocess
from pathlib import Path
password=json.loads(Path(os.environ['LIYUANCI_REDIS_MONITOR_PASSWORDS_FILE']).read_text())['redis://liyuanci_monitor@redis:6379']
rule='ACL SETUSER liyuanci_monitor reset on #'+hashlib.sha256(password.encode()).hexdigest()+' -@all +ping +info\r\n'
subprocess.run(['docker','compose','-p','liyuanci-dev','-f','compose.integration.yaml','exec','-T','redis','redis-cli','--raw'],input=rule,text=True,check=True)
PY
```

此命令明确仅操作`liyuanci-dev`开发项目，不用于生产；开发Redis重建/重启后需重新确认动态ACL，生产必须使用持久化的外部ACL文件。管理员角色/secret前置条件未满足时不启动exporter冒充接入。然后沿用前述Grafana密码等环境变量：

```sh
export LIYUANCI_MONITORED_NETWORK=liyuanci-dev_foundation
obs() { docker compose -p liyuanci-observability-dev -f compose.observability.yaml -f compose.alerting.yaml -f compose.metrics.yaml -f compose.metrics.middleware.yaml "$@"; }
obs config --quiet
obs up -d
```

在Grafana查看「中间件指标」：HTTP抓取up=1和`pg_up=1`/`redis_up=1`、两项last_scrape_error=0均须满足；MQ必须出现原生rocketmq_*指标。无消费组时消息积压/滞后面板可能无数据，不能解释为已消费或系统健康。

### 未来生产和验收

先在独立生产环境完成角色最小权限、外部secret、Redis ACL、内部网络与Broker配置准入；`LIYUANCI_MONITORED_NETWORK=liyuanci-production_foundation`，与开发完全不同的项目/secret/卷。生产启动函数使用上述叠加文件并按Linux实际需要增加`compose.metrics.linux.yaml`，按真实通知进度增加notifications叠加。停止监控时先down监控项目，再维护中间件网络，不加-v。未批准不执行生产启动。

生产监控还必须叠加`compose.observability.production.yaml`：独立持久化的仓库外`LIYUANCI_GRAFANA_ENCRYPTION_KEY_FILE`加密Grafana状态中的敏感配置，设置明确的`LIYUANCI_LOG_RETENTION`及`LIYUANCI_GRAFANA_ROOT_URL`（SSH方式可为`http://127.0.0.1:13000/`）。不要沿用内建默认状态密钥，也不要在已有Grafana卷上随意换密钥；备份/轮换须连同状态恢复验证。完整顺序示例为：

```sh
obsprod() { docker compose -p liyuanci-observability-production -f compose.observability.yaml -f compose.observability.production.yaml -f compose.alerting.yaml -f compose.metrics.yaml -f compose.metrics.linux.yaml -f compose.metrics.middleware.yaml "$@"; }
obsprod config --quiet
# 仅经生产准入及用户批准后由运维执行，当前批次未执行：
obsprod pull
obsprod up -d
```

需要真实通知时在函数中继续叠加notifications文件及对应外部配置；仅启用规则不代表通知可达。这里仍是单机监控骨架，不签高可用或容量通过。

新增告警监控PG/Redis连接和统计采集错误，现有up告警同时覆盖Broker/两个exporter；不预设未实现T007领域的积压/重试/死信阈值。阈值、全时间边界、生产凭据与故障恢复仍需生产前验收。`python3 scripts/check-management.py --middleware-only --metrics`创建独立生产形态中间件及监控项目，检查七目标、统计指标、原生MQ指标、PG/Redis错误监控密码及恢复、监控用户GET被拒绝；随后按依赖顺序清理容器/网络、保留卷。

2026-10-19最终实际PASS，项目`liyuanci-mgmt-bee10a5fb347`：上述正反例全部通过，中间件看板、四条规则实际求值健康及生产监控状态密钥注入也通过。本机记录`/tmp/liyuanci-middleware-metrics-final.log`；初次密码映射未包含用户名导致Redis未认证，按固定版本源码修正后成功，不扩大ACL权限。保留`PING/INFO`最小集合，监控账号GET返回NOPERM。`sh scripts/integration.sh`在`liyuanci-t004-20261019232016-2f878e`实际PASS，probe_id=`95d992af199f64c60eb726e9dd116b80`，delivery_count=2，真实API/worker也输出心跳。所有配置静态组合、脚本语法与`make fmt/check/build`通过。这里只验证隔离生产形态与合成数据，不等于真实生产账号/宿主/通知、容量、长期清理或业务验收，整体T004状态不变。

官方依据：[RocketMQ原生指标](https://rocketmq.apache.org/docs/observability/01metrics/)、[PostgreSQL exporter](https://github.com/prometheus-community/postgres_exporter)、[Redis exporter1.77.0密码映射](https://github.com/oliver006/redis_exporter/blob/v1.77.0/exporter/redis.go)。

存量规范清理（T004；REQ-018、020）：应用日志消息和告警标题/摘要使用中文，移除运行日志与告警标签中的任务 ID/REQ；事件字段、告警 UID、查询表达式与阈值保持稳定。历史验证记录保留原始编号和消息，不代表新版本运行结果。

本次清理验证（2026-10-20）：`python3 scripts/check-observability.py --alerts --metrics` 与 `python3 scripts/check-management.py --middleware-only --metrics` 均通过，隔离项目分别为 `liyuanci-logs-ce19888b5a`、`liyuanci-mgmt-a8ac08d53a37`；包含中文告警实际求值、隔离合成 Webhook 触发/恢复及中间件凭据正反例。`make fmt/check/build` 通过；未执行真实通知、Linux 宿主或生产验收。
