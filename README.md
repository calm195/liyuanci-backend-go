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
