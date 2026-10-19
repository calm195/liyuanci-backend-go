# 第一批实际验证记录

2026-10-17，T001/T002/T003/T004；REQ-001、003、018、020、021。下面仅记录实际执行，不以文件存在替代验收。

更新：后续T004后端真实MQ往返及容器构建已通过，详细结果与失败修复见[t004-followup.md](t004-followup.md)，结构化证据见[t004-integration-evidence.json](t004-integration-evidence.json)。整体T004仍待两端验收。

| 检查 | 实际结果 | 范围与证据 |
| --- | --- | --- |
| T001审批指纹 | PASS | `python3 scripts/verify-inputs.py`；G1基线及G3九项全匹配，详见input-verification.json；未改批准材料 |
| 模板/仓库状态 | PASS | 模板f7c932fc77dafe22796b1c9809a03928dfeb9a36干净；后端339b202初始工作区干净；原.gitignore未改，历史保留 |
| T002 OpenAPI 3.1 | PASS | `validate.py`：80操作、引用/operationId/任务REQ扩展、757请求/响应样例，5项长度/期限/审核驳回负例；仅静态契约，不代表产品正确 |
| make fmt | PASS | gofmt cmd/internal；没有删除或放宽格式检查 |
| make check | PASS | fmt-check、go vet ./...、go test -race -cover ./...；无中间件单元检查；覆盖率仅报告，无新增门槛 |
| make build | PASS | API/worker/probe三制品，宿主Go1.27.2 darwin/arm64；不是Go1.25容器构建或性能验收 |
| API进程运行 | PASS | 真实bin/server短暂loopback启动：healthz200、readyz503、users/me404，X-Request-ID及结构化完成日志、SIGTERM优雅退出。最终代码即使基础连接通过也保持T008前业务未ready |
| PG/Redis真实Go驱动连接 | PASS | `sh scripts/check-connections.sh`：PG18.6及Redis7.2.12容器健康；Go探针在PG容器内用本地Unix socket+内部Redis地址连接并Ping成功；结果明确`pg_redis_passed_mq_not_tested`。不证明生产TLS/口令/最小权限 |
| 显式探针迁移 | PASS | `docker exec -i … psql … < migrations/integration/0001_probe.up.sql`返回CREATE TABLE；无启动AutoMigrate，不是T005业务DDL验收 |
| PG探针回执SQL | PASS | 回滚事务内同probe_id两次UPSERT，count=1且delivery_count=2为true，随后ROLLBACK。仅验证探针SQL，不是Broker消费测试 |
| 完整make integration | PASS（续验） | 官方RocketMQ5.3.2固定digest，真实双投递→独立worker→PG回执delivery_count=2；4.9.8已放弃，不再追踪获取；版本决定见middleware-decision.md |
| Docker镜像构建/容器API-worker | PASS（续验） | Go1.25.0三制品容器构建通过、非root运行，容器API及worker正常退出；本地镜像ID已记录 |
| 远程CI/两端工程 | 未执行 | 只新增工作流；未提交/push，未操作另两个业务仓库，整体T004未完成 |
| T007/T008/T011及性能恢复 | 未实施/未验收 | 无业务发件箱、认证、学生卡、外部微信/图片联调或P95/90天恢复通过结论 |

真实拉取制品：

| 镜像 | 实际digest | 状态 |
| --- | --- | --- |
| postgres:18.6 | sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336 | 拉取并运行，已固定Compose；实际PG18.6、aarch64、Debian GCC14.2 |
| redis:7.2.12 | sha256:16623900b6ddd58e8bac04ccb6b611b9a5d1aed165453e28bcaed251d549d62c | 拉取并运行，已固定Compose |
| apache/rocketmq:4.9.8 | 历史：未取得；当前已放弃 | 用户决定改用已验证的5.3.2；此行仅保留尝试记录，不是开放待办 |
| golang:1.25.0-alpine / alpine:3.22 | 已取得，见续验报告 | 容器构建/运行通过，仍不是生产制品批准 |

失败也保留：首次Docker daemon未启动，启动OrbStack后恢复；Go缓存受沙箱限制，提升权限后tidy/check/build通过。PG/Redis首次宿主loopback探针失败，定位为internal网络不发布宿主中间件端口；改为容器内Go驱动检查后通过，并移除端口映射，没有放宽网络。OpenAPI首轮示例null类型不一致已修正，再校验通过。

本批只操作liyuanci-foundation专用容器/网络/卷；未操作已有其他项目容器。测试结束停止本批PG/Redis容器，保留专用数据卷和网络；测试库仅探针表，无真实用户资料。随机测试密码未写文件或日志。重新执行完整集成应在新隔离资源中使用调用方持有的测试secret；现有PG卷不会因修改环境变量自动改口令，禁止为通过测试删除无关卷或默认使用生产凭据。

当前主要风险：D01–D03、D05–D06未交付；4.9.8已放弃，当前5.3.2只签基础往返，生产供应链/可靠业务消费/性能仍未验收；后台凭据契约与部分部署参数仍依赖D02。保留现有方案B，不用删除MQ、放宽检查或模拟结果解决阻塞。

## 2026-10-19 Docker部署及管理配置（T003/T004；REQ-018、020）

用户要求集成与未来生产统一Docker部署；新增独立生产中间件编排、可选官方管理工具编排及操作文档，契约仍为0.1.0，无业务接口变化。

| 检查 | 实际结果 | 范围 |
| --- | --- | --- |
| make fmt/check/build | PASS | 本地格式、vet、race/coverage及三制品构建；首次沙箱Go缓存写入告警后提升权限重跑无告警通过 |
| 两套Compose合并边界 | PASS | `python3 scripts/check-management.py --config-only`；internal网络、无中间件宿主端口、管理loopback端口和固定digest、生产不包含探针/API/worker |
| 生产形态隔离中间件 | PASS | `python3 scripts/check-management.py --middleware-only`；最终项目`liyuanci-mgmt-9cde6cc08241`，PG/Redis/NameServer/Broker健康；Redis匿名PING返回NOAUTH，正确合成口令返回PONG；MQ匿名updateTopic明确拒绝。最终配置包含SCRAM初始化、日志轮转及外部只读ACL文件 |
| 镜像版本与架构 | PASS（清单） | pgAdmin9.18、Redis Insight3.8.0、Dashboard2.1.0官方镜像manifest均取得digest和amd64/arm64条目；不等于镜像运行/安全审计通过 |

生产形态测试使用临时随机凭据、合成ACL和独立项目，退出后容器/网络已清理，卷保留。原始无密钥验证日志位于本次宿主`/tmp/liyuanci-production-shape-final.log`；不依赖该临时路径交付生产。首次Redis验证使用redis-cli管道模式，因测试ACL没有管道工具所需ECHO权限导致超时；改用stdin普通AUTH/PING后复验通过，没有放宽被测ACL或业务检查。

未执行实际生产部署、真实生产账号/密钥与Dashboard ACL联调、远程CI或两端工程验收；原T004生产消费往返证据仍见既有续验报告，本次没有修改Go客户端和集成探针链路。生产最小权限角色/客户端凭据、外部备份与恢复、性能仍待后续任务。

管理工具最终复验：`python3 scripts/check-management.py` **PASS**，项目`liyuanci-mgmt-6b4b8bb805d3`。pgAdmin `/misc/ping`、Redis Insight `/api/health/`、Dashboard首页均HTTP200；Dashboard `/cluster/list.query`返回成功并包含`broker-a`。这验证真实页面与Broker发现，不等于生产账号登录/ACL或业务联调。日志：本次宿主`/tmp/liyuanci-management-final.log`。全部本次容器/网络已清理，数据卷保留。

管理工具真实启动期间发现Dashboard secret默认挂载名与Spring配置路径不一致，修正为显式目标`dashboard.properties`，并增加静态断言；HTTP探测改为本机直连，避免继承宿主失效代理。官方镜像下载较慢，尝试镜像代理取得同一官方digest，未修改全局Docker镜像源；最终运行仍使用Compose固定的官方镜像引用。失败的首次拉取/旧Dashboard配置验证已停止并清理，未将失败记录算作通过。
