# Docker 中间件与官方管理工具

T003/T004；REQ-018、020；2026-10-19。用户明确要求：集成及未来生产的 PostgreSQL、Redis、RocketMQ 全部由 Docker 部署。固定架构B和契约0.1.0不变。本次提供配置及隔离验证，不执行生产上线。

## 编排职责

- `compose.integration.yaml`：原有真实隔离探针环境，PG18.6、Redis7.2.12、RocketMQ5.3.2固定digest。仅测试环境允许无口令Redis/MQ，不可用于生产。
- `compose.production.yaml`：独立生产项目、网络和持久卷，默认重启中间件并轮转Docker标准输出日志；PG口令文件、Redis ACL文件、MQ ACL文件全部来自仓库外。无宿主机数据库/MQ端口，不包含API、探针worker、迁移或默认业务账号。
- `compose.management.yaml`：两种环境均可叠加，显式`management` profile才启动；独立管理卷保存工具设置。管理页面同时连接内部中间件网络与管理入口网络，仅loopback发布端口。禁止将带公网入口的管理容器用作网络代理。

| 中间件 | 工具及固定版本 | 宿主访问地址 | 工具内连接地址 |
| --- | --- | --- | --- |
| PostgreSQL18.6 | pgAdmin4 9.18（pgAdmin项目官方） | http://127.0.0.1:15050 | postgres:5432，数据库liyuanci |
| Redis7.2.12 | Redis Insight3.8.0（Redis官方） | http://127.0.0.1:15540 | redis:6379，DB0 |
| RocketMQ5.3.2 | Apache RocketMQ Dashboard2.1.0 | http://127.0.0.1:18081 | namesrv:9876；Broker自动发现broker |

管理工具镜像均固定多架构digest（见Compose），支持linux/amd64和linux/arm64。不使用latest；版本升级需重新验证。远程运维使用SSH本地转发上述端口，禁止将绑定地址改为0.0.0.0。Redis Insight和Dashboard不能依靠默认页面提供访问控制；SSH账号和主机访问权限属于管理权限边界。

## 集成环境启用

沿用原项目名及其原数据库口令，不能以新随机口令重新使用既有PG卷。通过环境注入以下**仓库外**文件路径与pgAdmin邮箱：

```sh
export COMPOSE_PROJECT_NAME=你的隔离项目名
export LIYUANCI_PGADMIN_EMAIL=你的管理邮箱
export LIYUANCI_PGADMIN_PASSWORD_FILE=/绝对路径/pgadmin-password
export LIYUANCI_DASHBOARD_PROPERTIES_FILE=/绝对路径/dashboard.properties
# LIYUANCI_TEST_DB_PASSWORD沿用该项目原值，不打印、不写仓库。
docker compose -f compose.integration.yaml -f compose.management.yaml --profile management up -d postgres redis namesrv broker pgadmin redisinsight rocketmq-dashboard
```

隔离Dashboard properties可以为空文件。pgAdmin登录使用单独管理口令，然后手工注册PostgreSQL连接；Redis Insight手工添加redis:6379。生产Redis须输入专用ACL账号，禁止在仓库预填密码。工具会保存连接设置、查询记录或敏感信息，其卷与备份同样受访问及删除策略约束；不使用真实学生资料做管理工具测试。

完成后使用**同样文件列表和项目名**执行`docker compose ... --profile management down`，默认保留卷；不加`-v`。管理工具平时可单独`stop pgadmin redisinsight rocketmq-dashboard`。

## 未来生产配置

本骨架限同一宿主的内部Docker网络；跨宿主部署须另配TLS与网络策略，不能直接复用明文内部链路。生产使用独立Docker主机/权限边界或受控独立环境，绝不与集成环境复用卷、secret和账号。配置文件中的Docker DNS地址是容器内部地址；宿主默认foundation配置继续只提供健康端点，不改为直连公网中间件。

Compose文件型secret是只读文件挂载，不会自动加密宿主文件或替代密钥管理系统。运行前由运维在仓库外准备并限制宿主访问权限（文件须能被对应容器UID读取）：

| 环境变量 | 文件内容/要求 |
| --- | --- |
| LIYUANCI_POSTGRES_PASSWORD_FILE | PG初始化管理账号的强随机口令；仅新卷初始化生效，既有卷轮换须执行数据库改密 |
| LIYUANCI_REDIS_ACL_FILE | Redis原生ACL；关闭无密码default账号，为运维和未来应用分别分配口令哈希、键前缀与命令权限。不能仅复制空ACL占位 |
| LIYUANCI_MQ_ACL_FILE | 5.3.2 ACL1.0的plain_acl.yml；不设全局白名单，应用默认主题权限DENY，按主题/消费组授权；管理账号独立 |
| LIYUANCI_PGADMIN_PASSWORD_FILE | 独立pgAdmin管理口令，不复用数据库口令 |
| LIYUANCI_DASHBOARD_PROPERTIES_FILE | Spring properties；生产填写`rocketmq.config.accessKey`、`rocketmq.config.secretKey`以访问开启ACL的Broker，内容视为secret |

通过`compose.production.yaml`启用中间件，按需叠加管理文件。这里只提供未来操作入口，本批不运行该生产项目：

```sh
# 先通过受控环境注入上表路径；命令只校验，不展开输出secret。
docker compose -f compose.production.yaml config --quiet
# 未来获准部署并完成上线验收后，由运维执行：
# docker compose -f compose.production.yaml up -d --wait
# docker compose -f compose.production.yaml -f compose.management.yaml --profile management up -d pgadmin redisinsight rocketmq-dashboard
```

生产PG初始化账号是管理角色，不能直接作为API/worker账号；最小权限角色及迁移在T005落实。Redis ACL账号和RocketMQ凭据尚未接入当前业务客户端，不能用集成探针验证生产ACL成功。MQ5.3.2对应ACL1.0；未来升级ACL2.0属于明确的版本兼容变更，不能套用新版配置悄悄替换。生产Redis探活确认无凭据请求返回NOAUTH；PG/MQ探活不代表应用账号授权通过。真实鉴权正反例、审计和密钥轮换须另验。

本文件为单PG/单Redis/单Broker的部署骨架，不宣称高可用、容灾或生产容量通过；PG/WAL、Redis持久化、MQ store及工具卷的外部备份、删除日志和恢复屏障必须在D05/T035落实。容器重启和持久卷不能替代备份。生产没有自动迁移，也没有启动未完成的领域worker。

## 可重复验证与边界

```sh
python3 scripts/check-management.py --config-only
python3 scripts/check-management.py --middleware-only
python3 scripts/check-management.py
# 镜像分批就绪时可独立验证某工具：
# python3 scripts/check-management.py --tool rocketmq-dashboard
make fmt
make check
make build
# 原有后端中间件端到端回归：
sh scripts/integration.sh
```

管理工具脚本使用唯一临时项目、进程内随机口令与仓库外临时secret文件，检查两套合并编排的网络/端口/固定镜像边界，再真实启动集成中间件及管理工具，检查三个HTTP入口和Dashboard的Broker发现。结束时删除本次容器/网络，保留卷；不打印展开配置或秘密。`--config-only`仅校验编排；`--middleware-only`使用同样的生产形态配置在唯一临时项目中启动PG/Redis/MQ，验证Redis无凭据拒绝/正确凭据PING成功及MQ匿名写入拒绝，不启动实际生产项目或管理工具。测试ACL和口令仅为合成临时凭据，不代表真实生产账号权限验收；完整管理检查仍不验证生产Dashboard凭据，也不替代业务/认证/性能验收。运行需Docker、Compose、Python3，且本机15050/15540/18081端口空闲。

官方依据：[pgAdmin容器部署](https://www.pgadmin.org/docs/pgadmin4/latest/container_deployment.html)、[Redis Insight Docker部署](https://redis.io/docs/latest/operate/redisinsight/install/install-on-docker/)、[RocketMQ Dashboard](https://rocketmq.apache.org/docs/deploymentOperations/04Dashboard/)、[RocketMQ 5.3.2 ACL1.0适用范围](https://rocketmq.apache.org/zh/docs/bestPractice/10access-1.0/)。本次两套配置静态检查、生产形态中间件隔离验证、三款管理页面HTTP和Dashboard Broker发现均已通过；测试后清理容器/网络、保留卷，工具当前未常驻运行。实际验证结果另记`docs/validation.md`。
