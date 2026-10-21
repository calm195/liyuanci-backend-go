# 心愿草稿创建模块

T014；REQ-003、004、005、008、014、018。正式传输形状沿用共享 OpenAPI 0.2.0 的 WishInput/WishResult，不改变版本、操作ID、状态枚举或共享审批。完整实施与实测边界见 [第九阶段](milestones/phase-09.md)。

## 显式依赖与启用

装配方以运行角色 PostgreSQL 连接池、注入的 zap.Logger、securedata.Codec、可选 Grades 与 wish.ServiceConfig 调用 wish.NewService。命令超时、锁超时、重试次数、回执保留期和至少32字节独立回执摘要密钥必须显式提供；密钥从仓库外注入，禁止复用样例或测试密钥。填年级但没有字典时失败关闭。API普通启动不迁移；迁移由既有专用身份入口执行。本模块不新增表或权限。

通过 httpapi.New 建立包含请求日志/恢复的路由，再显式调用 httpapi.RegisterWishCreation(router, service)。仅新增 POST /api/v1/wishes；cmd/server及现有studentapi装配尚未接入此模块，普通启动仍不开放心愿写入。真实供应商、运行配置、本人详情/列表、编辑/删除、送审、上架和公开查询后续分别接线；不能仅因注册创建接口而在学生/me中授予后续动作。正式索引实施状态不以本注入模块代替进程装配交付。

## 请求与响应

请求使用学生 Bearer 会话、Idempotency-Key 和 application/json：

```json
{"category":"help","body":"合成心愿示例","campus":"any"}
```

必填 category/body/campus；可选 identity_tags、reward_method、duration_days。省略报答方式及期限在服务层补 sincere_thanks 与7天；显式空值或0不使用默认。正文1–500 Unicode码点、非全空白；期限1–30整数天；枚举与已批准年级字典由Policy验证。请求安全上限1MiB。顶层和标签拒绝未知/大小写错误字段、重复键、null及尾随JSON；类型不符、非法UTF-8或超大请求返回400。

成功201，示例数据部分：

```json
{"id":"6c500000-0000-4000-8000-000000000001","version":1,"content_version":1,"current_state":"draft","allowed_actions":[]}
```

外层为既有code/message/data/request_id信封；响应Cache-Control: no-store、Pragma: no-cache及X-Request-ID。allowed_actions只描述此装配已实现动作；当前没有详情、编辑、提交审核等后续入口，因此为空数组。错误按既有稳定映射：输入400、会话401、资格/处罚403、回执目标不可访问404、同键异输入409、依赖503、恢复后的内部异常500；不泄漏正文、标签、token或供应商原始错误。

## 事务与验证

持账号guard复核当前会话、认证和处罚，校验内容后以用途/随机对象ID/内容版本绑定加密。心愿、私有内容版本、精确版本注册表和回执同事务提交；草稿不占活跃额度、不计时、不送审。用户填写标签与正文一起加密，公开正文NULL、公开标签空对象，不能以输入充当审核结果。重放重新复核当前权限和归属，返回当前版本/状态，不重建旧对象。

`make fmt`、`make check`、`make build`执行标准质量检查；`python3 scripts/check-wish.py`启动锁定镜像的隔离PostgreSQL容器，分别在独立业务测试库与HTTP测试库执行真实事务及最小权限检查。密钥、身份和材料均为明确合成夹具，不代表D02真实密钥或跨端验收；未设置显式测试DSN时普通Go检查会跳过对应数据库测试，不能据此声称数据库测试通过。
