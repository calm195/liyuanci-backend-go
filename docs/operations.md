# 启动与停止脚本（T004/REQ-018、020）

依赖Python3.9+、Docker引擎和Docker Compose v2。三个shell入口转交同一Python控制器，按脚本所在位置定位仓库，可从任意目录执行。Compose制品/配置和业务架构不变，不自动提交、推送或部署生产。所有执行阶段输出安全JSON完成记录；失败只显示阶段和退出码，不转储Docker输出、环境、DSN或secret内容。

## 本地开发

```sh
# 首次初始化：只创建缺失的仓库外开发凭据，不写仓库、不覆盖旧值。
./scripts/init-dev.sh
# 首次需要探针表/主题时显式执行；探针是真实MQ往返，不是认证验收。
./scripts/start.sh dev --bootstrap-probe --probe
# 日常启动默认重新构建API/worker；复用镜像可加--no-build。
./scripts/start.sh dev --no-build
# 停止全部服务，保留容器、网络、数据卷，不需要再次提供凭据。
./scripts/stop.sh dev
# 可重复执行：只移除所选项目容器/网络，仍保留数据卷。
./scripts/stop.sh dev --remove
```

默认主项目`liyuanci-dev`，监控项目`liyuanci-observability-dev`。初始化沿用README原路径：开发数据库/pgAdmin/dashboard文件位于`~/.config/liyuanci/dev`，Grafana口令位于`~/.config/liyuanci/observability-dev`；生成`~/.config/liyuanci/dev/operations.env`，启动自动读取。已有数据卷却缺原口令文件时拒绝生成新口令，必须恢复原文件；pgAdmin/Grafana初始化文件也不能重置已有账号。已有非空文件及环境配置不覆盖。开发目录要求0700，新secret文件0644供容器非root用户读取，operations.env为0600。

默认启动顺序：先校验主项目及监控全部配置→中间件healthy→仅显式参数触发探针迁移/主题→API/worker→管理工具→Alloy/Loki/Grafana/Prometheus及日志规则。`--probe`额外验证独立worker数据库回执；普通启动不执行迁移/创建主题、不跑探针、不创建监控身份。API目前healthz200、readyz503；没有业务认证能力。没有healthcheck的服务，Compose wait仅证明运行，不能代替HTTP、指标或告警验收。

默认本机地址：API18080、pgAdmin15050、Redis Insight15540、MQ Dashboard18081、Grafana13000，全部loopback。账号及操作同README与observability文档；脚本不输出密码。中间件和监控内部端口不公开。

## 参数与配置

| 参数 | 作用 |
| --- | --- |
| `--env-file /绝对路径/operations.env` | 显式环境文件覆盖同名进程环境值 |
| `--without-management` | 启动时不启用三个管理工具，不要求它们的配置 |
| `--without-monitoring` | 启动时不启用监控，不要求监控配置 |
| `--bootstrap-probe` | 仅dev，显式运行探针SQL迁移及创建探针主题 |
| `--probe` | 仅dev，启动API/worker后运行真实往返探针 |
| `--no-build` | 仅dev，API/worker复用镜像 |
| `--middleware-metrics` | 显式启用PG/Redis统计exporter及MQ指标，需要已有统计身份和外部secret |
| `--linux-host` | 仅Linux，启用宿主指标；macOS明确拒绝 |
| `--notifications` | 显式挂载真实通知配置；当前用户尚未提供真实渠道 |
| `--project NAME`、`--monitor-project NAME` | 所有入口可指定独立资源归属；启动和停止必须使用相同名称 |
| `--dry-run` | 只输出计划，不访问Docker/校验secret或修改文件，结果为planned |
| `--config-dir /路径` | 仅init-dev；将开发配置及observability子目录放到仓库外指定目录 |
| `--remove` | 仅stop；额外移除容器/网络，永远不删除卷 |

主/监控项目禁止同名，禁止交叉使用dev/production的默认项目名。指定自定义主项目后，未显式指定监控名时使用`主项目-observability`。所有Docker命令均显式传项目名，不依赖COMPOSE_PROJECT_NAME；停止按Docker项目标签查找容器，不依赖原始环境文件或当前终端导出状态。不要把同一项目名用于其他工程。

环境文件只支持一行一个`KEY=value`，仅接受LIYUANCI_/APP_前缀；空行/整行注释忽略，同名变量重复定义拒绝。值按字面读取，可用成对单/双引号包裹，不执行source、export、$HOME、`${VAR}`或`$()`，不支持行内注释或多行值。`*_FILE`的相对路径相对于环境文件目录；`~/`按当前用户展开，建议使用绝对路径。开发启动始终读取`LIYUANCI_TEST_DB_PASSWORD_FILE`再向Compose注入口令，旧终端的`LIYUANCI_TEST_DB_PASSWORD`不会覆盖文件；不支持只给明文变量而缺少口令文件的脚本启动。APP_最终是否传给容器由现有Compose定义决定。凭据必须引用仓库外文件，不应把密码放入命令行。

自定义开发目录示例：

```sh
./scripts/init-dev.sh --config-dir /安全绝对路径/dev-config
./scripts/start.sh dev --env-file /安全绝对路径/dev-config/operations.env --bootstrap-probe
```

增加中间件指标前，按[统计身份接入](observability.md)创建PG/Redis监控身份，将外部监控口令文件路径加入operations.env，再使用`--middleware-metrics`；脚本不生成生产身份，不将缺失真实资源降级为模拟。Linux宿主与真实通知同样显式启用，不能与`--without-monitoring`组合。

## 未来生产

在已获准的独立生产Docker主机上，将`configs/operations.production.env.example`复制到仓库外并填写实际文件路径/邮箱/安全主机别名，交付真实secret及最小权限账号。生产默认叠加独立监控状态密钥/保留期配置；未提供实际文件会在任何启动操作前失败。

```sh
# 本机可预览，planned不表示配置或生产验收通过，不执行Docker操作。
./scripts/start.sh production --env-file configs/operations.production.env.example --dry-run
# 仅在生产验收、维护窗口和实际授权具备后，由运维执行：
./scripts/start.sh production --env-file /etc/liyuanci/operations.env --middleware-metrics --linux-host
./scripts/stop.sh production
# 维护需要移除容器/网络时才使用；数据卷和仓库外secret仍保留。
./scripts/stop.sh production --remove
```

默认主项目`liyuanci-production`，监控项目`liyuanci-observability-production`。生产骨架不包含业务API/worker，禁止传dev的bootstrap/probe/no-build参数，不运行迁移、不生成凭据，不意味着完整产品上线。只启动中间件可加`--without-management --without-monitoring`。真实通知配置尚未交付，不在本次启停测试里启用notifications。备份恢复、真实账号/ACL、宿主/容量/保留期仍按既有准入验证。

## 停止、失败与排查

停止顺序：API/worker及管理工具退出时采集链路仍在线→Alloy及exporter→Grafana/Prometheus→Loki/日志代理→Broker→NameServer→Redis→PostgreSQL。Docker给予每组30秒停止时间，超时由Docker处理；不保证第三方镜像全部优雅退出。维护前按运维策略设置有时限的告警静默。`--remove`仅使用项目标签删除已停止容器/空闲网络，不force、不prune、不触碰卷；其他容器仍占用网络时Docker拒绝删除。

启动中途失败/中断不自动回滚已启动资源，保留现场并输出failed/interrupted；可用相同项目名执行stop。默认停止后可直接start复用原容器/卷；remove后也复用原命名卷和凭据。删除凭据或改密不等于重置数据库/管理账号，禁止为通过启动删卷。

脚本不回显Docker原始输出。排查可先查看所选项目容器，再按名称读取安全技术日志：

```sh
docker ps -a --filter label=com.docker.compose.project=liyuanci-dev
docker logs --tail 100 liyuanci-dev-api-1
docker logs --tail 100 liyuanci-observability-dev-grafana-1
```

镜像构建网络问题见README的GOPROXY配置；可用`--dry-run`核对实际Compose文件组合，再按原手工步骤诊断。不要打印环境文件、secret或不带--quiet的展开配置。

## 验证

单元边界：`python3 -m unittest discover -s scripts -p test_services.py -v`；真实全栈：`python3 scripts/check-services.py`。后者新建唯一项目和仓库外临时开发凭据，包含默认管理/监控、真实API日志查询、探针往返、停止保留、无隐式迁移重启及remove保留数据卷，退出只清理本次容器/网络。端口已占用时拒绝测试，不停止占用者；结果仅作开发环境验收，不作为生产上线验收。

2026-10-20实际PASS，项目`liyuanci-dev-ops-e8a5ec59c3`及其独立监控项目，记录在本机`/tmp/liyuanci-services-check-final.log`：上述全部流程通过，重启后再次完成真实MQ/独立worker数据库回执，移除后仅留下专用卷。13项单元回归、`make fmt/check/build`、`sh -n`、Python编译检查通过。首轮验收辅助脚本误用Docker volume的ID格式，改为Name后完整重跑；停止/保留断言未放宽。生产参数/文件预检路径通过单元测试，生产只执行dry-run计划预览，未启动生产或验证真实通知/Linux扩展，远端CI仍待执行。

存量规范清理（T004；REQ-018、020）：启停入口及共用实现的注释只说明功能与业务约束；结构化完成日志增加中文说明，不再输出任务 ID/REQ，保留操作、阶段、环境、结果和耗时字段。

本次清理验证（2026-10-20）：`python3 -m unittest discover -s scripts -p test_services.py -v`（13 项）与 `python3 scripts/check-services.py` 均通过；真实隔离项目 `liyuanci-dev-ops-df574141ac` 完成启动、探针往返、日志查询、停止、无隐式迁移重启及移除后卷保留检查。`make fmt/check/build` 通过；未执行生产启动或远端 CI。
