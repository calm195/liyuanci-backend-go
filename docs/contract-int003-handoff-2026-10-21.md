# 契约收口及后台隔离联调交接

日期2026-10-21（Asia/Shanghai）；后端会话。T002/B05/B07、T008/T010/INT-003；REQ-002–008、015、018、019；验收引用002-A–C、015-A–D、018-A–F、019-A/B、C01/C04/C05/C06/C08（只涉及本次子范围，非全项验收）。协调基准docs c927db0fa2a1b12b1ded49bc77e23991cc80325d。

## 快照与直接前置

后端feat/backend-foundation，HEAD 8ba233f7a3c4edab32de8331f775aefd14b2dcad，起始工作区干净。docs main HEAD与协调基准相同，无新增提交；相对该提交的既有变化为build.py/validate.py修改及actions.v0.2.0.json、authorization.py、moderation-extension.draft.json未跟踪。本轮未覆盖这五个文件；复制到/tmp/liyuanci-c927-review/contracts后运行生成/校验，并逐字节比较八个输入/生成文件全部一致。新增共享actions-review.v0.2.0-r1.json只固定静态动作词表revision 1及指纹，不激活Wire扩展，不表示已提交发布。两端可以按该manifest精确锁定文件，不要求重复原0.2.0迁移。原OpenAPI/examples未变。

直接前置：B05依赖T006当前授权/锁及T016领域命令；B07依赖当前投影服务与客户端消费、资料方字典/部署参数；INT-003依赖已有四操作、七版本迁移、隔离PG/Redis、受限身份、仓库外secret及管理端真实适配。T005/T006用户验收不证明真实密钥、学生身份或领域消息链完成。

## B05：持久映射与版本归属

已有DDL：moderation_tasks.(content_id,content_version)以复合外键绑定content_registry.(id,content_version)；registry具有(kind,entity_id,content_version)唯一约束。案件id是moderation_tasks.id，绝不能替代内容id。registry.entity_id是多态业务引用，业务存在性须同事务维护，并无通用外键保证。

| kind | 持久解析 | 提交前还须复核 |
| --- | --- | --- |
| wish | entity_id→wishes.id及对应wish_versions | 当前正文版本、主体权限及屏障 |
| application | entity_id→applications.id | 正文版本、关联心愿版本、资格/取消 |
| message | entity_id→messages.id | 正文版本、原会话write_epoch、取消/参与者权限 |
| story | entity_id→story_versions.wish_id，content_version→story_version | 复合键、撤回、当前公开版本/屏障；该表无独立id |

现有0.2.0语义：expected_version属于当前操作目标；审核判定属于案件版本，不是正文content_version，也不是心愿version、跨聚合wish_expected_version或消息expected_write_epoch。案件持久保存subject_epoch/hold_epoch，执行时重查当前授权、精确正文和屏障。content_decision与business_outcome分离，批准不保证公开/送达，202仅持久受理。

当前缺口：moderation_tasks没有独立version列，content_registry也无处置资源独立version；不能用常数1、正文版本或wish.version填充。正式ModerationCase没有target/allowed_actions字段。草案建议以registry.id为目标id、增加独立目标版本、案件版本及目标动作投影；这属于新Wire/持久结构设计，不能作为0.2.0解释直接上线。后续版本需补追加迁移、中文对象注释、版本推进/锁规则、映射完整性及两端兼容评审；不改已应用0004/0005。

状态候选矩阵（未激活）：human_required且当前review_operator/super_admin、版本/屏障均有效才提示postAdminModerationTasksIdDecision；pending/machine_passed/approved/rejected/obsolete/cancelled均无人工判定动作。内容下架/解限动作另外绑定内容目标和当前处置资格，不能从案件可判定推出可下架。

合成预期样例（未执行HTTP）：案件版本7、正文3、目标版本11的human_required请求expected_version=7可进入判定；请求6→409 VERSION_CONFLICT；案件已obsolete或cancelled→409 STATE_CONFLICT；hold_epoch变化、正文已替换、会话写屏障变化→409 STATE_CONFLICT且不公开、不送达、不解除hold。有效角色不足403、不可见目标404、失效会话401。返回content_decision=approved时business_outcome仍可能是不执行；具体结果枚举以正式schema为准，不造新错误码或结果值。

需用户决定/按影响评审：采用草案registry.id处置资源，还是另设稳定实体级处置资源（影响跨正文版本身份、限制归属和新版本迁移）；接受新响应字段/版本模型的兼容发布范围。独立乐观版本列、复核屏障和失败关闭是实现所需技术工作，无需重问已有政策。未选择前草案继续draft-not-active，B05开发中并标记阻塞。

## B07：已有正式operationId、索引快照与未冻结语义

静态索引固定为actions.v0.2.0.json revision 1，精确指纹见末尾及共享actions-review.v0.2.0-r1.json。正式动作标识为已有OpenAPI operationId，区分大小写；无content.scan等事件别名。83条含GET/登录等协议操作，不代表每条都可出现在allowed_actions，也不代表服务可用（仍仅4项implemented-opt-in）。生成/校验改动只独立调用authorization.build/validate，未改冻结Wire。projection非运行授权规则，不能单凭路径猜权限；null只表示此索引未提供Me/WishResult归属。

Me归属包括postWishes、putMeProfile、postMeVerificationRequests等账号级动作；WishResult归属包括postWishesIdPublish、putWishesIdContent、postWishesIdApplications及故事相关心愿操作。版本字段逐操作见索引。申请、消息、案件、内容处置、后台账号等不能借用Me/WishResult授权，按各自精确目标和服务端当前授权处理。

索引仍是未提交工作区快照，manifest不等于正式提交或两端确认。正式性仅指operationId已存在于冻结OpenAPI；不能向要求“已正式提交输入”的消费者宣称新索引已发布。以下可供两端评审/实现的映射不启用真实写入口：

| 场景 | 动作名 | 提示投影/目标及版本 |
| --- | --- | --- |
| 选图上传 | postMeStudentCardAssets | Me当前学生；本地选图本身无HTTP动作；上传无旧版本 |
| 首次认证 | postMeVerificationRequests | Me当前学生；资产须本人approved；无审核记录省略expected_version |
| 重提认证 | postMeVerificationRequests | Me当前学生；expected_version取Verification.version，不取Me.version |
| 新建草稿 | postWishes | Me当前学生；无旧版本 |
| 改版正文 | putWishesIdContent | WishResult当前心愿id；expected_version为心愿版本 |
| 删除草稿 | deleteWishesIdDraft | WishResult当前心愿id；expected_version为心愿版本 |
| 送审 | postWishesIdSubmitReview | WishResult当前心愿id；expected_version及当前content_version |
| 上架 | postWishesIdPublish | WishResult当前心愿id；expected_version为心愿版本 |

首次认证已有记录却缺版本预期400，重提旧版409 VERSION_CONFLICT，资产未就绪409 ASSET_NOT_READY；当前资格/角色不足403，不可见目标404。动作提示不绕过这些校验。学生服务尚未实现，以上是既有协议与本次静态归属核对，非HTTP实测。

消费判定：仅接受当前服务端投影中的已知动作，且动作适用该投影、目标id与当前页面目标相同；版本取该命令目标，不能跨账号/跨心愿复用。Me返回[postWishes]可提示创建；WishResult(id=A)返回[postWishesIdPublish]只可提示A上架。缺失/null/空数组/未知content.scan、Me携带心愿上架、A动作套B均拒绝。未知项不影响已知合法项；每次写后端仍重查角色/资格/状态/版本/屏障。当前校验仅覆盖缺失/未知/错误投影，不覆盖真实目标绑定或并发授权，后两项是后续领域/客户端测试要求，不能称已通过。

后台角色刷新：登录postAdminSessions.data.account.roles为本次快照；服务端逐请求读当前角色。401清除token/角色，显式重新登录；403停止当前操作，不自动重试、不推断新角色或会话失效；需要新菜单时显式重新登录。不新增GET后台会话/refresh接口，不借学生/me。改密204清除当前会话重新登录；迟到旧响应不得覆盖新会话。token仅内存，绝对到期重登，无前端TTL猜测。角色修改HTTP仍未实现。

学生策略：独立学生Bearer、数据库当前生命周期/auth_epoch/撤销/到期及资格复核已定；后台绝对到期策略不能套给学生。学生TTL、续期/恢复体验尚未冻结，真实微信/学生服务未交付；不新增刷新端点或假定长期有效。

资产查询客户端技术建议（尚未冻结为强制协议、尚未接页面）：进入/重入页面且持有本人asset id时立即单次GET；pending可串行延迟重查，间隔/退避/总时限由联调配置显式锁定，暂不编造秒数。离开/后台/换asset/会话失效取消定时器并忽略旧代响应；恢复重新GET当前资产，不复用旧approved，不并发叠加轮询。approved/rejected停止；404停止并清理该引用，401清会话，503保留失败提示且不得提交。approved只证明图片安全通过，不代表认证。此建议不要求修改已完成独立API迁移，B14缺的是实际页面编排和动作消费。

标签：grade在读年级字典待资料方，male/female/undisclosed为男/女/保密；未填和主动保密不同。隐藏时不暴露值。创建时identity_tags或字段省略表示未填；null不被schema接受。现有PUT内容没有定义省略是保留还是清除，不能借HTTP方法自行认定；空对象虽通过schema也不能证明数据库清除行为。更新省略/清除须补版本化语义与客户端回归；建议完整替换标签（省略字段清除），目前仅草案，需按兼容影响确认后启用。不要新增未经批准的null清除语义。

B07开发中（阻塞）：词表/后台重登技术子项可消费；学生策略、查询配置与页面、标签更新语义、年级字典、部署TTL及真实正反验证未齐。

## INT-003可重复交接清单与预期

状态开发中（阻塞），仅准备完成，不是待联调或联调通过。四操作沿用8ba233f；启用细节及全部APP_ADMINAUTH_*名称见[操作说明](admin-auth.md)。本轮未启动、初始化或部署任何环境。

1. 后端交付方指定独立Docker项目/网络/PG/Redis与loopback或受控隧道API地址；记录环境标识、镜像digest、配置非敏感指纹、后端完整HEAD及管理端完整HEAD。当前地址/环境实例未交付，小程序此局部链不适用。
2. 管理身份显式运行bin/migrate up至0007_admin_local，bin/migrate permissions；提交迁移账本/七版校验及权限脱敏证据。普通启动不迁移。后台实际LOGIN只继承liyuanci_admin_maintenance，非超管/属主/BYPASSRLS/建库/建角色或其他业务组；专用连接池连同一业务权威库。权限组脚本不创建实际登录身份。
3. 仓库外受控secret引用：APP_ADMINAUTH_DSN、INITIALPASSWORDFILE、RECEIPTKEYFILE、Redis凭据；只交引用和访问主体，不写真实值/令牌或对低熵口令做公开指纹。回执密钥至少32字节、实例一致、环境隔离。空库才以bin/admin-init显式初始化首超管；已有账号拒绝，不自动重置。
4. 显式锁定测试SESSIONTTL、TIMEOUT、LOCKTIMEOUT、RECEIPTTTL、MAXATTEMPTS、LOGINWINDOW、LOGINLIMIT和Redis连接参数。TTL正数，锁时限至少1ms且不超过操作时限，重试1–10；实际参数及配置指纹待环境交付方填写，不能把单测参数当生产批准值。独立保留测试账号/限流窗口，保证重复运行不污染其他会话。
5. 管理端交付方显式接真实传输/地址、仅内存token、no-store、401/403/204及弱网/重复/晚到响应清理；不得用替代fetch结果充当真实接线。环境交付后先核对健康与局部接口；readyz仍503不表示本局部登录一定不可测，也不能绕过真实依赖检查。
6. 集成执行方逐条记录请求ID、时间、操作、预期/实际HTTP、制品/迁移/配置快照及脱敏服务日志。凭据仅执行器内存，账号为合成资料；测试报告不存请求体或token。重复运行使用独立命名账号/幂等键并注明清理所有权，不删除其他项目/卷。

| 用例 | 前置/操作 | 预期HTTP与状态（全部待执行） |
| --- | --- | --- |
| I3-01 | 不存在账号/错误口令登录 | 401 UNAUTHENTICATED，统一失败不枚举；无泄漏 |
| I3-02 | 有效身份登录两次保存会话A/B | 各200，安全account投影，token仅内存 |
| I3-03 | A旧口令错误改密 | 403 FORBIDDEN，口令未变，A/B仍可用 |
| I3-04 | A正确改密，独立幂等键 | 204空体，撤销所有旧会话；A/B及并发旧会话后续操作401 |
| I3-05 | 旧口令/新口令重新登录 | 旧401、新200；新会话同键重放改密按当前授权回204且不重复修改 |
| I3-06 | 新会话退出 | 204空体；再次用该token401，前端清理 |
| I3-07 | 到期/学生token/撤权/限流或Redis故障 | 到期/学生token401；无创建角色403；超限429、Redis故障503，失败关闭 |
| I3-08（可选创建） | 超管创建/普通角色创建 | 201/403；同键同请求不重复、异摘要409 IDEMPOTENCY_CONFLICT、异键重名409 STATE_CONFLICT；查留迹 |

以上为INT-003局部验收编号，不代替原REQ全项。全部旧会话测试至少包含同账号两个旧token，不能只验证当前token；其他账号/学生域不应被误撤销。历史phase-05已有真实PG/Redis/httptest证据，此处无新跨端HTTP结果。准入缺项仍是可访问环境、迁移实例、实际受限登录身份、secret访问引用、测试配置和管理端真实接线/统一快照。

## 阻塞责任与下一步

B02：政策、四操作和管理端协议迁移完成；后端/环境方交配置实例、管理端开隔离接线、集成执行INT-003。B03：独立GET/条件版本已完成；后端学生卡服务/资源及小程序页面生命周期仍缺。B04：后端真实脱敏分页、读卡授权与审计未交付。B05：本次持久映射/版本差异已核对；后端提交新版设计/迁移与领域命令，两端消费，用户确认上述兼容取舍。B06：管理端heads/双版本迁移完成；后端发布/扫描/审计及资料方D03待补。B07见上节。B08真实密钥/执行身份由资源方补；B09微信资源由微信交付方补；B10身份规格/私有桶/安全扫描由资料与资源方补；B11正式词库/运营资料由运营方补；B12删除恢复/固定性能环境由运维测试方补；B13运输已交付，后端仍须同一真实PG+MQ领域闭环/取消及提交后ACK前故障。均不整体关闭。

B14仅共同输入冻结、管理端适用迁移、小程序独立API完成；本轮静态词表校验不关闭B14。小程序页面/正式动作消费及三端适用快照核对仍缺，不重复既有迁移。共享plans由docs协调会话汇总，本会话不修改。下一步按影响评审B05新版本与标签语义，交付INT-003环境并接线后再申请准入；无部署上线动作。

## 本轮指纹及验证

以下为实际观察工作区SHA256，不将未提交文件冒充c927db0提交内容：

```text
5f7389cb5e3ba968345db1c9b1d3565daee0ef9731810df657b94d6365c661a4  openapi.v0.2.0.json
0d3560fcb0e634d4e0e65a48e448bcb9e605fe3253f65aa976d31bd30bf2df1e  examples.v0.2.0.json
188043aa722f3a40d74644bb77c2f6f462cb6b04fe46e8ff7399a03ccf294a07  decisions.v0.2.0.md
5f436b6b6ce264f4a69eba719139dd043cd6a4313c7199b9f895884a8867bb55  actions.v0.2.0.json
4db5299e9d90d658a1571a51666332a94382dfb53d39e45452c3ed2fbc2da5cd  authorization.py
22d762592d7733262d55c5a09945d3e9188fb43afd1e25fa9b7b1c68a3d4b155  build.py
876a8fcd84ca2fe0bb0dc49b11929a732fec03c8c2e8fc16c1817171af3361f9  validate.py
1bdda358942abf6392efeac2975d636334a60bf448e4ca5d7a31846656d4eb12  moderation-extension.draft.json
```

在隔离副本运行 `/tmp/liyuanci-contract-validator/bin/python /tmp/liyuanci-c927-review/contracts/build.py` 与相邻 `validate.py` 均退出0：83操作/130 schema，783示例、5原负边界/8新增负例，动作及草案正反例通过；逐字节再生成一致。验证器0.7.2；LibreSSL/RefResolver弃用警告存在但未影响本次退出结果。静态校验不验证运行时授权或目标关联，不代表审核/学生服务或前端联调。规定Go质量检查结果见本阶段增量；本轮仅文档/快照清单变化，无运行时代码改变。

本轮文档完成验证：`make fmt`、`make check`（只读格式检查、`go vet ./...`、`go test -race -cover ./...`）退出0；单元结果含缓存，未配置集成DSN的真实集成不计本轮验证。两仓`git diff --check`通过。无Go/依赖修改，未运行`make build`；未重跑Docker后台/迁移检查或真实跨端联调。最终新增/未提交文件列表以本轮回报为准，无本轮提交。

## 后续B07正式交付覆盖说明

2026-10-21：用户授权独立正式提交已执行，docs `f8c35f93fcc10cd2b4adc3f3b67ae4e4736b0b87`。契约0.2.0/index revision 1，actions SHA256 `1c6b67db585a9f80970bdde16b996c145fb5627ee56c86de2ce82af3bfb2ebd2`，manifest SHA256 `0d377b65bf7a6f353ecf620f1db403d3117debbb239084e4fd5791c61a0680ba`。前文未提交状态/旧生成源与索引指纹仅属历史，接入必须使用本节新SHA/manifest。生成器已拆除审核草案依赖，草案原文件未改/未提交/未激活；GET列表不再标为WishResult写动作。正式提交包含消费说明和合成跨目标负例；详情及实际检查见phase-05后续增量。B05用户取舍、B07其余待定项、B14页面/消费和INT-003环境缺项不因此关闭。
