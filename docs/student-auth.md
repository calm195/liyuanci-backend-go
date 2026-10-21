# 学生微信身份与会话

T008/REQ-002、014、018；2026-10-21。用户确认微信身份遵循小程序wx.login，并授权先实现可注入适配器和隔离测试，外部供应商接入保留缺口。

客户端调用wx.login取得一次性code，POST /api/v1/auth/wechat-sessions提交code/client_request_id；后端WeChat适配器调用固定HTTPS微信接口，AppID/AppSecret只在服务端从仓库外注入。不接受客户端OpenID/UnionID/session_key，微信身份只建立unverified账号，不自动获得学生认证。成功标准封装data含token/me，token是本系统不透明会话，不能用微信session_key替代。

## 装配与资源职责

studentauth.New要求显式注入根GORM连接、zap、Exchanger、identity.Store、共享Limiter及Config；RegisterStudent仅显式注册。当前server尚未装配学生资源，不能通过配置打开合成供应商。正式接入仍缺D01真实小程序资源、D02真实密钥/检索服务及独立执行身份适配。后台池不复用为学生池；数据库组使用identity_maintenance，只有学生sessions RLS权限，无后台sessions跨域访问。启动方需先显式执行九版迁移和permissions、配置openid检索代次。New不迁移、不初始化密钥或账号；外层先等待HTTP退出再关闭池/供应商。

SessionTTL为学生绝对会话期限，ExchangeTTL为同code成功回执期限（不得超过SessionTTL），Command为事务/锁/回执技术限制，SessionKey为独立至少32字节仓库外随机密钥。全部显式注入，测试值不作为生产政策。SessionKey不复用后台口令回执、OpenID检索或KMS包裹密钥；HMAC按code/client_request/来源/session_token隔离用途。现阶段不提供自动续期。

## 原子身份与code重试

先在数据库唯一领取code的HMAC摘要，保存客户端请求标识摘要及随机32字节nonce；再在事务外交换微信身份，不持数据库锁等待微信。一次领取后不重新交换同code。若进程中断、外部失败、回执尚未成功或回执到期，客户端重新wx.login取得新code；同code异client_request_id为409 IDEMPOTENCY_CONFLICT。

微信成功后在根事务内按身份全局锁→账号guard→回执/会话顺序，双代openid检索查重，必要时创建随机账号并保存信封密文/唯一占用，再写会话摘要和完成回执。会话写失败、失钥或超时整笔回滚。新账号昵称为不含身份的“新用户”，后续可本人修改；不从微信读取姓名或手机号。

本系统token由独立SessionKey对随机nonce用途隔离派生256位不可预测值；数据库持有随机nonce、回执引用及token摘要，不持有token、code或OpenID明文。丢失SessionKey不能恢复成功回执，重新获取code登录；切换该密钥的协调窗口/清理策略待真实装配落实，不宣称生产密钥轮换验收。重试只在回执有效且数据库当前会话/账号仍有效时返回同token，退出、到期、删除或auth_epoch变化后不能恢复旧会话。未成功领取记录没有账号身份关联，过期清理调度尚未交付，后续运维维护需保留去重安全窗口。

## 四个HTTP操作

- POST /api/v1/auth/wechat-sessions：无需Bearer/Idempotency-Key，输入code/client_request_id；200 Session。只按同code成功回执去重。无效/已消费code401，外部网络/密钥/数据库故障503，本地共享限流429，不回传供应商错误原文。
- GET /api/v1/me：有效学生Bearer，200 Me；每次查当前账号生命周期/代次和处罚。受罚只返回受限资料与退出动作，不返回姓名/学号/卡片/OpenID。后台token401。
- PUT /api/v1/me/profile：Bearer、Idempotency-Key、nickname/hide_identity_tags/expected_version，全部必填；200 Me。空白昵称/未知字段400，旧版本409 VERSION_CONFLICT，受罚403；同键重放仍当前授权，不重复推进version。
- DELETE /api/v1/auth/session：Bearer与Idempotency-Key，204；受罚可退出，只撤销当前会话。旧token再调用401，不影响其他会话。

均no-store。Me.allowed_actions当前只列实际已实现且当前允许的putMeProfile/deleteAuthSession，认证/学生卡动作由显式装配追加；心愿模块完整装配且当前verified、无交互限制时追加postWishes。GET操作不加入动作数组，未装配时不凭verified开放写入。记录中文完成日志operation/request_id/result/duration；不记录code、OpenID、token、AppSecret、session_key或请求正文。

## 验证边界

python3 scripts/check-adminauth.py分别创建独立后台/学生数据库，真实Docker PG/Redis、实际HTTP处理器；微信和KMS使用明确合成适配器。微信HTTP适配器固定域名/参数、禁止重定向、限制响应大小、拒绝重复字段/尾随JSON；HTTP协议通过注入Transport合成测试。官方参考为[wx.login登录流程](https://developers.weixin.qq.com/miniprogram/dev/framework/open-ability/login.html)及[code2Session](https://developers.weixin.qq.com/miniprogram/dev/server/API/user-login/api_code2session.html)；本轮工具无法打开官方页面，未据此宣称真实供应商验证。无微信实机、生产KMS或跨端联调。
