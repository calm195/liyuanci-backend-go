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
