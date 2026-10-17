# T004 后端续验（2026-10-17）

关联T004、REQ-001、018、020；依据用户“尝试完成T004，可尝试多个镜像”继续执行。**后端工程基础及真实中间件最小往返通过；整体T004仍未完成，等待两端工程验收。**不扩展到T005–T011业务实现，不宣称认证或T007可靠消费已完成。

当前版本决定：用户已明确放弃4.9.8，以已验证的5.3.2固定digest作为实施及后续测试基准，见[middleware-decision.md](middleware-decision.md)。下列拉取失败仍保留为历史证据，不再作为待办。

## 实际结果

执行：`LIYUANCI_BUILD_GOPROXY=https://goproxy.cn,direct sh scripts/integration.sh`，退出码0。成功项目为`liyuanci-t004-20261017092509-a84a9f`，运行时间约2026-10-17 09:25–09:26（北京时间）。结构化证据见[t004-integration-evidence.json](t004-integration-evidence.json)。

| 项目 | 实际验证 |
| --- | --- |
| 真实依赖 | PostgreSQL18.6、Redis7.2.12、RocketMQ5.3.2 NameServer/Broker健康，API/worker/probe以Go驱动连接PG/Redis及MQ |
| 显式初始化 | 探针表SQL返回CREATE TABLE；mqadmin显式建立liyuanci_foundation_probe_v1主题成功；Broker关闭自动建主题 |
| 容器构建 | API、worker、probe均从锁文件构建；容器二进制元数据确认为Go1.25.0，linux/arm64，运行用户app/UID10001 |
| 真实生产/消费 | Probe向MQ发送同一随机事件两次；独立worker两次完成消费并提交数据库；probe_id=`bd4da2ed29c29978eb431bb16dead855`，单主键回执delivery_count=2，完成日志result=passed，探针耗时约1.024秒（非性能验收） |
| 容器HTTP边界 | loopback18080：healthz200、readyz503、POST模板users404、GET未实现me404；各有X-Request-ID。readyz保持T008认证/密钥未完成的边界 |
| 生命周期 | API server shutdown/resource cleanup、worker consumer shutdown/resources cleanup均有成功日志；随后本次容器/网络已移除，专用测试卷保留 |
| 质量 | make fmt、make check（格式/vet/race/cover）、make build通过；新增DNS解析、失败关闭及初始化失败不panic的回归测试；无检查削弱 |
| 只读边界 | T001基线/G3指纹复核通过；源模板、批准资料、另外两个业务仓库未修改，契约保持0.1.0；未提交/push/部署 |

## 镜像尝试与实际制品

| 镜像/来源 | 结果与选择 |
| --- | --- |
| apache/rocketmq:4.9.8 | 当前默认加速节点仍返回繁忙，未取得；原性能候选组合没有补签通过 |
| registry.cn-hangzhou.aliyuncs.com/rocketmqinc/rocketmq:4.9.4 | 仓库不存在或无权限；未取得、未运行、未采用 |
| apache/rocketmq:4.9.6 | 成功取得digest `sha256:8f71d5bd704b6c23dd7b8e885c0d1ff6b120deb9b0f3b24184c241a09f1a73cc`；仅备选下载，未用于验收 |
| apache/rocketmq:5.3.2 | 成功取得并用于真实往返；原生arm64，Temurin JRE1.8.0_442-b06；digest `sha256:4a80ff5cd4b0a3d3ef6dc338ce2028a85bd995648cb3aa5c3e753746d2dbc587`，现为隔离Compose默认固定版本 |
| golang:1.25.0-alpine | 已实际构建；digest `sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440` |
| alpine:3.22 | 已实际运行，Alpine3.22.6；digest `sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8` |

RocketMQ Go客户端仍锁定2.1.2，没有升级SDK或使用Proxy绕开原生消费。5.3.2也是源模板的版本，且有[官方Docker示例](https://rocketmq.apache.org/docs/quickStart/02quickstartWithDocker/)。本次运行只建立T004基础兼容证据。后续用户已放弃4.9.8，5.3.2版本选择已确定，无需再次确认；批准的performance.md原文保持只读，正式性能manifest按版本决定记录差异，以同一负载/数据/资源/门槛实测，不据此签性能/安全/生产验收。PG/Redis固定digest沿用首批，见foundation-manifest.json。

## 已修复的问题与依据

1. **全新MQ卷属主错误**：官方非root Broker无法写root属主的`store/lock`。增加仅挂载专用卷的mq-init，禁网、只读根文件系统、仅CHOWN/FOWNER能力；初始化入口挂载到根目录`/mq-store`，避免受限root遍历用户私有目录。Broker继续以rocketmq/UID3000运行。
2. **NameServer域名不兼容**：SDK2.1.2的NamesrvAddr.Check只接受IP形式。启动时使用带context的系统DNS解析，统一供Producer与Consumer使用，校验host/port、地址去重及失败关闭；不把DNS域名冒充HTTP发现URL。解析结果在进程生命周期固定，地址变化须重启客户端，后续生产拓扑运维需覆盖此限制。
3. **模板失败清理panic**：SDK构造失败返回的Producer interface可含nil指针；直接存入Resources会在Close时panic。改为仅在构造及Start成功后接管，保留失败清理顺序，增加原崩溃路径的回归测试。
4. **订阅早于自动建主题**：消费者Start要求目标主题已存在。将探针主题创建加入显式初始化，关闭autoCreateTopicEnable，不能靠Producer首次发送碰巧建立通道。
5. **内部网络不发布宿主端口**：API增加项目内独立ingress网络，仍只绑定127.0.0.1:18080；PG/Redis/MQ无宿主端口，保留foundation internal隔离。真实容器入口检查已通过。
6. **旧卷口令与本轮随机口令不匹配**：脚本默认创建唯一新项目，退出清理仅本次容器/网络、保留卷；显式指定项目时由调用方维护凭据和生命周期，不改旧测试库口令，不打印/保存密钥。
7. **官方Go代理超时**：实际探测官方代理超时，goproxy.cn的模块/zip/校验数据库代理可达。增加显式构建代理参数；go.sum、GOSUMDB默认验证保持启用，没有换依赖版本或禁校验。

首次启动SDK对尚未生成的`%RETRY%liyuanci-foundation-probe-v1`主题有路由警告，已保留在原始日志。本次仅验收正常/重复基础投递；重试、死信、取消、晚到/崩溃和事务发件箱仍由T007单独验证，不能用本次成功代替。

本轮新增/修改文件：Dockerfile、Makefile、compose.integration.yaml、configs/broker.conf、scripts/integration.sh、scripts/check-container-api.py、internal/platform/resources.go、internal/platform/nameservers.go及对应测试、internal/worker/worker.go、cmd/probe/main.go，以及本仓库状态/环境/验证/制品文档。完整文件清单见change-files.md。

下一步：后端T004基础证据可交接两端；整体T004仍待两端工程验收。下一批仍按T005→T006→T007/T008→T011的已批准依赖推进，D01–D03/D05–D06外部交付不因本次MQ通过而自动就绪。远程CI尚未运行，不将本地同等检查结果冒称远程绿灯。
