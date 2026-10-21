# 消息运输适配验证快照

日期：2026-10-21（Asia/Shanghai）；后端开发会话。T007 独立 MQ 子范围，REQ-006、007、015、017、018、020。原验收关联 006-A–D、007-A–E/AR-001、015-A–D/AR-004、017-A/B/C、018-A–F、020-A；本次只为运输/通用框架提供局部证据，未执行上述完整业务验收。本地用例编号 MQ-01–05 不是新的正式验收编号。

基准：docs main/a5e6124dd77c0f539516e550fb790d7ad2bd0195，契约0.1.0；起始及结束复核均无后续提交或未提交修改。后端 feat/backend-foundation/550be1e4f340008113c44e9b3c1273d49e7ac84f。本次未提交；用户确认起始 nameservers.go 与 transportmq/ 为可接续前序改动、无其他会话编辑。本轮新增/修改文件以以下指纹及最终 git status 为准，未切换代码或覆盖其他会话文件。

直接前置 T005/T006 已有可用迁移/持久工作/事务与授权库，整体真实密钥、身份和领域接线未验收；本次只继续既有授权独立范围，不把前置改写为通过。

## 源码 SHA256

真实 Broker 最终运行代码及 broker_test、检查脚本对应如下。最终单元测试后追加了 ACK 状态映射反例；真实 Broker 用例本身和运行代码未再变动。文档更新不改变制品。

```text
78327377cd9fbd437bc4b1d52eaeb0bbf77c8c262f99ddc5d561fc7cd761a730  internal/platform/nameservers.go
2fba23fc25112cc800e727fb738daef689598a22b5510cf144cf6a580b0ddfaf  internal/transportmq/transport.go
d705acdd5151a377310ec1b5e888b943a0e43fa855180c5578dc0403b083b111  internal/transportmq/transport_test.go
59b9c668750de657fbd8a6b2b5996a530b9f14978ff1072957101430083dd53e  internal/transportmq/broker_test.go
61718fe75686dbd5ea653288af149e6bfd6ccc90e336a467f95fb6ee5a4ccad8  scripts/check-mq.py
64e49328563b2e4c1f2b9d803625c7e063dbb22df09ffac41e11a8c485965221  .github/workflows/ci.yml
7be287da9904fcc0a16c111743f746736177cfd9504cef31f4bf2e143dac84b9  README.md
```

## 实际命令与结果

| 命令/本地用例 | 实际结果与边界 |
| --- | --- |
| `make fmt` | 通过；gofmt 后检查，非只读格式化 |
| `make check` | 通过，fmt-check、go vet、go test -race -cover；transportmq 64.2%；未配置真实服务环境的集成用例明确跳过，不以此声称真实服务通过 |
| `make build` | 通过，server/worker/probe/migrate；首轮沙箱模块缓存提示 operation not permitted，授权重跑无提示并退出0 |
| `python3 scripts/check-mq.py` / MQ-01–03 | 真实 Broker 两轮通过：初轮项目 liyuanci-mq-check-6c9481de0627（40.02s），加入固定主题校验后最终项目 liyuanci-mq-check-a75850f53441（40.03s）；真实发送/订阅、首次失败NACK再投、重复及合成终态晚到、取消发送、关闭后拒绝发送、实际客户端退出。两轮完整脚本含清理均退出0，卷保留 |
| 单元 MQ-04–05 | ACK映射Applied/Replayed/Cancelled/Missing/Deferred，失败/无结果/panic/非法载荷NACK；关闭取消并等待在途、生产者发送与关闭顺序、错误主题拒绝、日志完成字段/脱敏；由 make check 执行 |
| `python3 scripts/check-migrations.py` | 通过；本次真实独立PostgreSQL，迁移79.7%、identity81.2%、command83.5%、durable77.8%，包含取消/去重/补偿/重驱动/窄权限/中文目录注释；本次未加 --container，不沿用旧镜像CLI结果充作本次重测 |
| `sh scripts/integration.sh` | 退出0；随机项目 liyuanci-t004-20261021104407-af116c，原有PG/Redis/MQ探针/HTTP边界往返及固定Go1.25 Docker构建；并非业务API联调 |
| `git diff --check` | 通过 |
| `PYTHONPYCACHEPREFIX=/private/tmp/liyuanci-pycache python3 -m py_compile scripts/check-mq.py` | 通过；首次默认缓存目录被沙箱拒绝，改用显式临时目录后通过 |

MQ使用 Compose 中锁定 Apache RocketMQ5.3.2 digest、原 broker.conf 和Go SDK2.1.2；随机独立网络/主题/组与保留卷，不操作现有开发项目，不修改Broker行为、指标、启停入口或部署生产。MQ检查将宿主交叉编译的CGO关闭测试二进制放入固定Alpine镜像，在项目内部网络执行；不是锁定Go1.25 builder的MQ库测试。原有集成的正式应用构建使用锁定Go1.25镜像。

## 真实、合成与尚未执行

真实：Docker NameServer/Broker发送、订阅、重试与客户端关闭；另一隔离检查中的PostgreSQL持久工作/事务/去重/取消终态/补偿。

合成：Broker用例内存处理器故意首次失败、重复记账和取消终态；PostgreSQL用例中的身份、领域回调和外发Sender。没有将内存去重计作数据库恰好一次，也没有把“return前处理完成”当成真实领域提交证据。两套检查未组成同一真实PG+MQ领域闭环。

替代传输：现有PG测试仍用合成Sender/JSON重投；新增MQ测试使用真实Broker。未执行真实领域消费者、领域取消写命令、提交后ACK前杀进程、Broker断网/故障恢复/死信重驱动、生产ACL/身份/留存、性能、D02/D03/D05真实适配、三端业务联调或生产验收。新增CI检查已配置，远端CI未执行。

状态：MQ独立运输子范围“本端验证通过”；T007整体“开发中”，附B13。下一步在明确领域授权与依赖后接实际消费者、取消命令及独立业务worker，再做同一PG+MQ快照的提交后ACK前故障、晚到与注销/隐私矩阵验证。当前worker仍是基础探针，没有业务API交付。

B02–B07逐项提案见[协调材料](../contract-coordination-2026-10-21.md)。全部保持阻塞，未修改共享契约或台账；用户/身份资源方决定B02、B07身份标签/会话策略；后端协调B03版本来源、B04授权/读卡/审计、B05目标动作、B06词库版本语义，涉及业务取舍交用户。每项解除条件和样例已逐项列出，预计答复日待定。B08–B12原交付条件不因局部运输通过而关闭；按影响停止相关真实验收，继续不依赖它们的授权工作。

最终文档检查时 make check 的 vet 阶段再次遇到宿主 Go 缓存读取 operation not permitted；随后授权执行 make fmt/check/build 与 git diff --check 全部退出0，无遗留检查失败。

最终未提交文件：`.github/workflows/ci.yml`、`README.md`、`docs/milestones/README.md`、`docs/milestones/phase-04.md`、`internal/platform/nameservers.go`；新增 `docs/contract-coordination-2026-10-21.md`、本快照、`internal/transportmq/transport.go`、`transport_test.go`、`broker_test.go`、`scripts/check-mq.py`。

本次未自动提交、推送、部署或上线。共享进度/阻塞/联调记录留给docs协调会话汇总。
