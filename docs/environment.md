# T003 环境与D01–D06清单

2026-10-17，REQ-002、006、008、015、017、018、020、021。集成责任由当前后端承担协调，资料真实性/账号归属/资源提供由项目负责人指定提供方；尚无已交付姓名或日期，不虚构承诺。

| 环境 | 配置/入口 | 真实/模拟边界 |
| --- | --- | --- |
| foundation | configs/config.yaml；默认127.0.0.1:8080 | 无外部适配、无业务Mock、仅健康端点；无中间件readyz=503 |
| integration | configs/integration.yaml + compose.integration.yaml，独立项目liyuanci-foundation/内部网络/专用卷 | 真实PG/Redis/MQ；worker只订阅foundation专用探针主题，无学生/运营账号；只API端口绑定127.0.0.1:18080 |
| production | compose.production.yaml：Docker中间件独立项目/卷及外部secret；可叠加compose.management.yaml | 已提供中间件配置骨架、未部署；业务APP_ENVIRONMENT=production及独立凭据仍待接入；配置校验要求三项中间件，worker拒绝启动探针；业务/密钥/认证未实现，不能以基础健康等同生产就绪 |
| performance | 待D06 manifest | 必须另建performance v0.2固定预算环境；当前本机容器不是性能验收环境 |

模板：`/Users/kurous/code/go-template`，提交`f7c932fc77dafe22796b1c9809a03928dfeb9a36`，核验时干净。选择性导入cmd/internal/configs/构建与CI/锁文件；未导入.git、.env、本地覆盖、.idea、bin或演示迁移；当前.gitignore保持原字节。保留模板AGENTS质量/中文注释/结构化安全日志约束，补充本仓库授权边界。移除示例user领域和MySQL能力，只保留已批准PostgreSQL方案。

| 依赖ID | 需要的交付、承担角色 | 凭据位置/职责 | 状态及受影响任务 |
| --- | --- | --- | --- |
| D01 | 微信主体、AppID、登录/通知能力、分享路由与合法HTTPS域名；负责人指定账号方，后端协调 | 运行平台独立secret引用；不入前端源码/文档/日志；提供方负责主体和域名 | 未交付/未联调；阻塞T008真实微信与T033实机登录/通知/分享；契约/隔离夹具可继续 |
| D02 | 独立密钥源/KMS、后台身份提供方、私有测试桶、图片安全；后端核验，资源方提供 | KMS/secret manager引用及最小权限待交付；测试密钥与生产独立，API/worker/迁移/证据权限分别配置 | 未交付；阻塞T005真实密钥、T008后台凭据定型、T011对象/图片及T033真实验收；加密不可用必须拒绝明文 |
| D03 | 危机/涉校/普通词库、审核/组合信息样例、举报类型、真实热线/预约和值班资料；负责人指定运营/校方，后端收集、测试核对 | 仅非密钥业务资料版本和出处入后续契约；值班方负责8–22时人工接手 | 未交付；T016/T026/T030真实规则及T033阻塞，可用明确合成样例验证协议；不编造热线或24小时人工 |
| D04 | 模板锁文件、Go/Broker/客户端、镜像digest及可执行测试资源；后端协调与验证 | 集成环境随机测试数据库口令只在运行进程注入；无预设账号密码，PG账户仅隔离容器使用 | 模板已锁；PG18.6/Redis7.2.12保持；用户已放弃MQ4.9.8，以固定digest的官方MQ5.3.2＋Go SDK2.1.2作为实施及后续测试版本，T004真实往返通过，见t004-followup.md；T007可靠消费/性能另验 |
| D05 | 独立持久删除日志、WAL/快照/对象历史/回收站/导出全副本清单与90天策略；运维输入、后端实现 | 独立加密日志/备份权限与主体数据隔离；恢复负责人待指定 | 未交付；阻塞T031真实清理闭环、T035恢复放行；不得开放缺失删除日志的恢复系统 |
| D06 | 固定性能资源、版本/配置/数据指纹、独立压测机；集成提供，测试锁manifest | 不含DSN、token、私有图或密钥；仅配置哈希和secret引用名 | 未交付；阻塞T036/T037真实性能验收。固定300在线/87每秒、四列表及稀疏/空分组P95不改写 |

当前宿主：macOS arm64、Go1.27.2；容器Dockerfile指定Go1.25.0，续验实际RocketMQ5.3.2原生arm64、JRE Temurin1.8.0_442-b06；容器Go1.25.0构建和往返已通过。4.9.8已依用户决定放弃，不再列为获取或验证待办。宿主Go与设计起点不同，基础构建结果单列，不能冒用Go1.25性能结果。Go锁文件固定Gin1.11.0、GORM1.31.1、PG驱动1.6.0、Redis Go9.17.0、RocketMQ Go2.1.2；go.mod/go.sum是实际依赖依据。镜像仅版本标签尚不证明不可变制品或安全通过，实际拉取digest与失败见validation.md；正式环境须固定digest并安全核验。

运行：`sh scripts/integration.sh`默认创建唯一全新项目/卷，生成进程内随机口令，依次初始化MQ卷权限、启动真实中间件→显式migrate及探针主题→API/worker→probe与容器API检查；结束清理本次容器和网络，保留卷。网络受限可显式设置LIYUANCI_BUILD_GOPROXY=https://goproxy.cn,direct，go.sum和GOSUMDB仍校验。重复运行现有PG卷须保持原口令环境；脚本不会自动删卷/重置数据库。清理只用同Compose项目`down`停止本批容器，默认保留卷，不操作其他项目容器。CI有独立middleware job；其成功须看实际日志，不能由工作流文件存在推定。

环境参数：APP_ENVIRONMENT、APP_DATABASE_DRIVER/DSN/MAXOPEN/MAXIDLE、APP_REDIS_ENABLED/ADDRESS/PASSWORD/DB、APP_ROCKETMQ_ENABLED/NAMESERVERS/GROUP、APP_SERVER_ADDRESS及超时、APP_LOG_LEVEL。DSN只经secret注入；禁止打印`docker compose config`完整展开值。当前integration测试账户不是生产授权分离方案，生产迁移/普通API/worker权限须T005落实。

T004续验制品与兼容性以[t004-followup.md](t004-followup.md)及foundation-manifest.json为准；NameServer DNS在启动时解析为SDK支持的IP，地址变化须重启客户端。当前仅验证最小正常/重复投递，真实业务重试/取消/故障属于T007，远程CI及两端仍待验收。

版本决定见[middleware-decision.md](middleware-decision.md)；已批准设计快照保持只读，后续性能manifest记录5.3.2与原快照的软件版本差异，其他负载/数据/资源/门槛保持原方案，不需要再次为同一版本选择请求确认。

## 2026-10-19 Docker部署约定补充（T003/T004；REQ-018、020）

依据用户新指令，中间件在集成及未来生产统一通过Docker部署；不使用宿主机安装或托管服务替代本批配置。生产与集成编排独立，管理工具作为可选profile提供。新增文件、外部secret变量、连接地址和运维职责见[docker-middleware.md](docker-middleware.md)。所有管理端口只绑定loopback；无新增业务接口，契约仍为0.1.0。D02/D05/D06仍待真实资源/密钥/备份恢复与性能资源验收，Docker配置存在不代表已交付或上线。
