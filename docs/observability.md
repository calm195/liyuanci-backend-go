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

Grafana为`http://127.0.0.1:13000`，账号`operator`，密码为外部文件中保存的随机值。不要把密码粘贴到命令行参数或日志。首次初始化后更换文件不会自动重置已有Grafana账号密码。预置「T004 鲤源池技术日志」看板；Explore选择Loki：

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

T004/REQ-018、020：API成功绑定监听、worker成功启动消费者后，每30秒以注入zap.Logger输出`event=telemetry_heartbeat`、service、result=alive、task_id，不包含身份/正文；停止函数等待日志协程退出，先于日志器清理。心跳只表示进程活着，不承诺数据库/MQ就绪。HTTP完成日志增加稳定事件`http_request_completed`，异常恢复增加`http_panic`，保留请求ID和最终状态。

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
