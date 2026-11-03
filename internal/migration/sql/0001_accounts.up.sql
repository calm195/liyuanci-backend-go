-- 账号、身份和会话；版本 SQL 是结构定义的唯一来源。

CREATE TABLE public.accounts (
    id uuid NOT NULL,
    lifecycle text DEFAULT 'active'::text NOT NULL,
    verification text DEFAULT 'unverified'::text NOT NULL,
    auth_epoch bigint DEFAULT 1 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    nickname text NOT NULL,
    hide_identity_tags boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT accounts_auth_epoch_check CHECK ((auth_epoch > 0)),
    CONSTRAINT accounts_lifecycle_check CHECK ((lifecycle = ANY (ARRAY['active'::text, 'deleting'::text]))),
    CONSTRAINT accounts_nickname_check CHECK ((length(btrim(nickname)) > 0)),
    CONSTRAINT accounts_verification_check CHECK ((verification = ANY (ARRAY['unverified'::text, 'pending'::text, 'verified'::text, 'rejected'::text]))),
    CONSTRAINT accounts_version_check CHECK ((version > 0)),
    CONSTRAINT accounts_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.accounts IS '普通账号及命令并发控制入口；注销清理完成后物理删除';
COMMENT ON COLUMN public.accounts.id IS '随机账号标识，同时用于统一锁序';
COMMENT ON COLUMN public.accounts.lifecycle IS '账号生命周期：active 正常，deleting 注销处理中';
COMMENT ON COLUMN public.accounts.verification IS '学生资格审核状态，身份绑定本身不代表审核通过';
COMMENT ON COLUMN public.accounts.auth_epoch IS '权限代次，变更后用于撤销旧会话授权';
COMMENT ON COLUMN public.accounts.version IS '聚合状态版本，用于命令期望版本校验';
COMMENT ON COLUMN public.accounts.nickname IS '用户昵称，不得为空白';
COMMENT ON COLUMN public.accounts.hide_identity_tags IS '是否隐藏公开身份标签';
COMMENT ON COLUMN public.accounts.created_at IS '账号创建时间，以带时区时间保存';

CREATE TABLE public.admin_account_actions (
    id uuid NOT NULL,
    actor_id uuid NOT NULL,
    target_id uuid NOT NULL,
    action_kind text NOT NULL,
    reason text NOT NULL,
    at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT admin_account_actions_action_kind_check CHECK ((action_kind = ANY (ARRAY['bootstrap'::text, 'create'::text, 'password_change'::text, 'roles_change'::text]))),
    CONSTRAINT admin_account_actions_reason_check CHECK ((length(btrim(reason)) > 0)),
    CONSTRAINT admin_account_actions_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.admin_account_actions IS '后台账号及角色操作留迹，不保存口令、令牌或请求正文';
COMMENT ON COLUMN public.admin_account_actions.id IS '操作随机标识';
COMMENT ON COLUMN public.admin_account_actions.actor_id IS '执行操作的后台账号，初始超管初始化时为自身';
COMMENT ON COLUMN public.admin_account_actions.target_id IS '本次操作的后台目标账号';
COMMENT ON COLUMN public.admin_account_actions.action_kind IS '后台初始化、创建、本人改密或超管角色修改动作';
COMMENT ON COLUMN public.admin_account_actions.reason IS '创建理由或服务端固定的本人改密用途，不接收口令';
COMMENT ON COLUMN public.admin_account_actions.at IS '事务记录时间';

CREATE TABLE public.admin_accounts (
    id uuid NOT NULL,
    credential_ref text,
    password_hash text,
    status text DEFAULT 'active'::text NOT NULL,
    auth_epoch bigint DEFAULT 1 NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    login_name text,
    CONSTRAINT admin_accounts_auth_epoch_check CHECK ((auth_epoch > 0)),
    CONSTRAINT admin_accounts_check CHECK (((credential_ref IS NULL) <> (password_hash IS NULL))),
    CONSTRAINT admin_accounts_credential_ref_check CHECK (((credential_ref IS NULL) OR (length(btrim(credential_ref)) > 0))),
    CONSTRAINT admin_accounts_login_name_check CHECK (((login_name IS NULL) OR (length(btrim(login_name)) > 0))),
    CONSTRAINT admin_accounts_password_hash_check CHECK (((password_hash IS NULL) OR (length(btrim(password_hash)) > 0))),
    CONSTRAINT admin_accounts_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text]))),
    CONSTRAINT admin_accounts_version_check CHECK ((version > 0)),
    CONSTRAINT admin_accounts_credential_ref_key UNIQUE (credential_ref),
    CONSTRAINT admin_accounts_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.admin_accounts IS '独立后台账号，不初始化管理员或默认口令';
COMMENT ON COLUMN public.admin_accounts.id IS '随机对象标识';
COMMENT ON COLUMN public.admin_accounts.credential_ref IS '外部后台身份引用，供应商交付前不填入模拟账号';
COMMENT ON COLUMN public.admin_accounts.password_hash IS '可选本地凭据摘要，不能保存明文口令';
COMMENT ON COLUMN public.admin_accounts.status IS '后台账号状态';
COMMENT ON COLUMN public.admin_accounts.auth_epoch IS '后台权限代次，撤销旧会话使用';
COMMENT ON COLUMN public.admin_accounts.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.admin_accounts.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.admin_accounts.login_name IS '本地后台登录名，精确匹配且唯一，历史账号不自动补名';

CREATE TABLE public.admin_role_bindings (
    admin_id uuid NOT NULL,
    role text NOT NULL,
    CONSTRAINT admin_role_bindings_role_check CHECK ((role = ANY (ARRAY['review_operator'::text, 'crisis_operator'::text, 'super_admin'::text]))),
    CONSTRAINT admin_role_bindings_pkey PRIMARY KEY (admin_id, role)
);
COMMENT ON TABLE public.admin_role_bindings IS '后台角色绑定，仅由受控超管命令变更';
COMMENT ON COLUMN public.admin_role_bindings.admin_id IS '后台账号';
COMMENT ON COLUMN public.admin_role_bindings.role IS '应用角色，不等同于数据库权限组';

CREATE TABLE public.assets (
    id uuid NOT NULL,
    owner_id uuid NOT NULL,
    purpose text NOT NULL,
    encrypted_object_key jsonb NOT NULL,
    object_version text NOT NULL,
    wrapped_key_ref text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    scan_status text DEFAULT 'pending'::text NOT NULL,
    CONSTRAINT assets_encrypted_object_key_check CHECK (((encrypted_object_key IS NULL) OR (jsonb_typeof(encrypted_object_key) = 'object'::text))),
    CONSTRAINT assets_object_version_check CHECK ((length(btrim(object_version)) > 0)),
    CONSTRAINT assets_purpose_check CHECK ((purpose = 'student_card'::text)),
    CONSTRAINT assets_scan_status_check CHECK ((scan_status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text]))),
    CONSTRAINT assets_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'deleting'::text, 'deleted'::text, 'failed'::text]))),
    CONSTRAINT assets_version_check CHECK ((version > 0)),
    CONSTRAINT assets_wrapped_key_ref_check CHECK ((length(btrim(wrapped_key_ref)) > 0)),
    CONSTRAINT assets_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.assets IS '普通学生卡私有对象元数据，删除前须完成外部对象清理';
COMMENT ON COLUMN public.assets.id IS '随机对象标识';
COMMENT ON COLUMN public.assets.owner_id IS '普通账号所有者，不级联跳过外部对象清理';
COMMENT ON COLUMN public.assets.purpose IS '普通对象仅学生卡，证据对象另存隔离域';
COMMENT ON COLUMN public.assets.encrypted_object_key IS '私有对象键，保存信封密文而非明文';
COMMENT ON COLUMN public.assets.object_version IS '外部对象版本，不使用公开下载地址';
COMMENT ON COLUMN public.assets.wrapped_key_ref IS '本机包裹密钥引用，不保存主密钥';
COMMENT ON COLUMN public.assets.status IS '对象处理状态';
COMMENT ON COLUMN public.assets.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.assets.version IS '学生卡处理版本，用于锁后状态复核';
COMMENT ON COLUMN public.assets.scan_status IS '图片安全状态，pending待处理、approved通过或rejected拒绝，不代表学生认证';

CREATE TABLE public.blocks (
    owner_id uuid NOT NULL,
    target_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    block_id uuid DEFAULT gen_random_uuid() NOT NULL,
    CONSTRAINT blocks_check CHECK ((owner_id <> target_id)),
    CONSTRAINT blocks_version_check CHECK ((version > 0)),
    CONSTRAINT blocks_opaque_id_unique UNIQUE (block_id),
    CONSTRAINT blocks_pkey PRIMARY KEY (owner_id, target_id)
);
COMMENT ON TABLE public.blocks IS '有向拉黑关系，交互授权必须复核任一方向';
COMMENT ON COLUMN public.blocks.owner_id IS '设置拉黑的账号';
COMMENT ON COLUMN public.blocks.target_id IS '被拉黑的账号';
COMMENT ON COLUMN public.blocks.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.blocks.version IS '有向拉黑关系版本，解除时校验；重复拉黑不递增';
COMMENT ON COLUMN public.blocks.block_id IS '不透明随机黑名单条目标识，历史关系原位回填且不返回目标账号';

CREATE TABLE public.identity_lookup_claims (
    purpose text NOT NULL,
    key_ref text NOT NULL,
    digest bytea NOT NULL,
    account_id uuid NOT NULL,
    CONSTRAINT identity_lookup_claims_digest_check CHECK ((octet_length(digest) = 32)),
    CONSTRAINT identity_lookup_claims_key_ref_check CHECK ((length(btrim(key_ref)) > 0)),
    CONSTRAINT identity_lookup_claims_account_id_purpose_key_ref_key UNIQUE (account_id, purpose, key_ref),
    CONSTRAINT identity_lookup_claims_pkey PRIMARY KEY (purpose, key_ref, digest)
);
COMMENT ON TABLE public.identity_lookup_claims IS '双代身份唯一占用映射，同一用途同一密钥摘要只能属于一个账号';
COMMENT ON COLUMN public.identity_lookup_claims.purpose IS '用途域，学号与微信身份分别保持唯一';
COMMENT ON COLUMN public.identity_lookup_claims.key_ref IS '计算该检索摘要的本机密钥引用';
COMMENT ON COLUMN public.identity_lookup_claims.digest IS '独立检索密钥计算的用途隔离 HMAC-SHA256，禁止写日志';
COMMENT ON COLUMN public.identity_lookup_claims.account_id IS '唯一占用账号，与对应密文同行生命周期清理';

CREATE TABLE public.identity_lookup_keys (
    purpose text NOT NULL,
    current_ref text NOT NULL,
    previous_ref text,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT identity_lookup_keys_check CHECK (((previous_ref IS NULL) OR ((length(btrim(previous_ref)) > 0) AND (previous_ref <> current_ref)))),
    CONSTRAINT identity_lookup_keys_current_ref_check CHECK ((length(btrim(current_ref)) > 0)),
    CONSTRAINT identity_lookup_keys_purpose_check CHECK ((purpose = ANY (ARRAY['student_number'::text, 'openid'::text]))),
    CONSTRAINT identity_lookup_keys_version_check CHECK ((version > 0)),
    CONSTRAINT identity_lookup_keys_pkey PRIMARY KEY (purpose)
);
COMMENT ON TABLE public.identity_lookup_keys IS '身份检索密钥轮换状态，仅保存外部引用，最多同时使用两代';
COMMENT ON COLUMN public.identity_lookup_keys.purpose IS '检索用途：学号或微信身份';
COMMENT ON COLUMN public.identity_lookup_keys.current_ref IS '当前写入和回填的本机检索密钥引用';
COMMENT ON COLUMN public.identity_lookup_keys.previous_ref IS '轮换期间仍参与查重的旧代引用，回填完成前不得清除';
COMMENT ON COLUMN public.identity_lookup_keys.version IS '轮换状态版本，初始化及每次状态切换递增';

CREATE TABLE public.identity_names (
    account_id uuid NOT NULL,
    content_version bigint NOT NULL,
    name_cipher jsonb NOT NULL,
    CONSTRAINT identity_names_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT identity_names_name_cipher_check CHECK (((name_cipher IS NULL) OR (jsonb_typeof(name_cipher) = 'object'::text))),
    CONSTRAINT identity_names_pkey PRIMARY KEY (account_id)
);
COMMENT ON TABLE public.identity_names IS '普通账号姓名密文，独立于检索占用，无姓名查重索引';
COMMENT ON COLUMN public.identity_names.account_id IS '所属账号，普通账号清理时删除';
COMMENT ON COLUMN public.identity_names.content_version IS '姓名内容版本，绑定认证加密附加数据';
COMMENT ON COLUMN public.identity_names.name_cipher IS '姓名，保存信封密文而非明文';

CREATE TABLE public.identity_reviews (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    asset_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    reason text,
    reviewer_id uuid,
    reviewed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    data_cipher jsonb,
    revision_suggestion text,
    CONSTRAINT identity_reviews_check CHECK (((status <> 'rejected'::text) OR ((reason IS NOT NULL) AND (length(btrim(reason)) > 0)))),
    CONSTRAINT identity_reviews_data_cipher_check CHECK (((data_cipher IS NULL) OR (jsonb_typeof(data_cipher) = 'object'::text))),
    CONSTRAINT identity_reviews_material_rejection CHECK (((data_cipher IS NULL) OR (status <> 'rejected'::text) OR ((revision_suggestion IS NOT NULL) AND (length(btrim(revision_suggestion)) > 0)))),
    CONSTRAINT identity_reviews_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'obsolete'::text, 'cancelled'::text]))),
    CONSTRAINT identity_reviews_version_check CHECK ((version > 0)),
    CONSTRAINT identity_reviews_account_id_version_key UNIQUE (account_id, version),
    CONSTRAINT identity_reviews_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.identity_reviews IS '学生资格审核版本，图片扫描通过不等同于认证';
COMMENT ON COLUMN public.identity_reviews.id IS '随机对象标识';
COMMENT ON COLUMN public.identity_reviews.account_id IS '申请账号';
COMMENT ON COLUMN public.identity_reviews.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.identity_reviews.asset_id IS '本次审核的学生卡对象';
COMMENT ON COLUMN public.identity_reviews.status IS '审核状态';
COMMENT ON COLUMN public.identity_reviews.reason IS '驳回原因';
COMMENT ON COLUMN public.identity_reviews.reviewer_id IS '处理审核的后台账号';
COMMENT ON COLUMN public.identity_reviews.reviewed_at IS '完成审核时间';
COMMENT ON COLUMN public.identity_reviews.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.identity_reviews.data_cipher IS '按申请版本加密的待审姓名与学号，历史未提交材料为空';
COMMENT ON COLUMN public.identity_reviews.revision_suggestion IS '驳回后的修改建议';

CREATE TABLE public.identity_secrets (
    account_id uuid NOT NULL,
    purpose text NOT NULL,
    content_version bigint NOT NULL,
    envelope jsonb NOT NULL,
    CONSTRAINT identity_secrets_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT identity_secrets_envelope_check CHECK ((jsonb_typeof(envelope) = 'object'::text)),
    CONSTRAINT identity_secrets_pkey PRIMARY KEY (account_id, purpose)
);
COMMENT ON TABLE public.identity_secrets IS '按账号和用途保存身份字段信封密文，不包含主密钥或明文身份';
COMMENT ON COLUMN public.identity_secrets.account_id IS '拥有该身份字段的普通账号，普通账号物理删除时清理';
COMMENT ON COLUMN public.identity_secrets.purpose IS '身份字段用途，同时作为认证加密绑定及检索域';
COMMENT ON COLUMN public.identity_secrets.content_version IS '密文内容版本，与认证加密附加数据一致';
COMMENT ON COLUMN public.identity_secrets.envelope IS '算法、本机密钥引用、包裹数据密钥、随机数和认证密文';

CREATE TABLE public.interaction_block_policy (
    singleton boolean DEFAULT true NOT NULL,
    opaque_enabled boolean DEFAULT false NOT NULL,
    switched_at timestamp with time zone,
    CONSTRAINT interaction_block_policy_check CHECK ((opaque_enabled = (switched_at IS NOT NULL))),
    CONSTRAINT interaction_block_policy_singleton_check CHECK (singleton),
    CONSTRAINT interaction_block_policy_pkey PRIMARY KEY (singleton)
);
COMMENT ON TABLE public.interaction_block_policy IS '独立于树洞开关的全局匿名黑名单单向切换策略';
COMMENT ON COLUMN public.interaction_block_policy.singleton IS '唯一全局策略行';
COMMENT ON COLUMN public.interaction_block_policy.opaque_enabled IS '启用后禁止回退目标账号标识接口';
COMMENT ON COLUMN public.interaction_block_policy.switched_at IS '已验证历史随机标识后显式切换时间';

CREATE TABLE public.sessions (
    id uuid NOT NULL,
    account_id uuid,
    admin_id uuid,
    subject_kind text GENERATED ALWAYS AS (
CASE
    WHEN (account_id IS NOT NULL) THEN 'student'::text
    ELSE 'admin'::text
END) STORED,
    subject_id uuid GENERATED ALWAYS AS (COALESCE(account_id, admin_id)) STORED,
    token_hash bytea NOT NULL,
    auth_epoch bigint NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT sessions_auth_epoch_check CHECK ((auth_epoch > 0)),
    CONSTRAINT sessions_check CHECK (((account_id IS NULL) <> (admin_id IS NULL))),
    CONSTRAINT sessions_check1 CHECK ((expires_at > created_at)),
    CONSTRAINT sessions_token_hash_check CHECK ((octet_length(token_hash) = 32)),
    CONSTRAINT sessions_pkey PRIMARY KEY (id),
    CONSTRAINT sessions_token_hash_key UNIQUE (token_hash)
);
COMMENT ON TABLE public.sessions IS '学生与后台独立会话，仅保存高熵令牌摘要';
COMMENT ON COLUMN public.sessions.id IS '随机对象标识';
COMMENT ON COLUMN public.sessions.account_id IS '学生会话主体，与后台主体二选一';
COMMENT ON COLUMN public.sessions.admin_id IS '后台会话主体，与学生主体二选一';
COMMENT ON COLUMN public.sessions.subject_kind IS '根据主体外键生成的会话命名空间';
COMMENT ON COLUMN public.sessions.subject_id IS '根据主体外键生成的主体标识';
COMMENT ON COLUMN public.sessions.token_hash IS '会话令牌摘要，禁止存令牌原文';
COMMENT ON COLUMN public.sessions.auth_epoch IS '签发时主体权限代次，每次请求需与当前值复核';
COMMENT ON COLUMN public.sessions.expires_at IS '会话失效时间';
COMMENT ON COLUMN public.sessions.revoked_at IS '主动撤销时间';
COMMENT ON COLUMN public.sessions.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE public.student_card_payloads (
    asset_id uuid NOT NULL,
    envelope jsonb NOT NULL,
    mime text NOT NULL,
    CONSTRAINT student_card_payloads_envelope_check CHECK ((jsonb_typeof(envelope) = 'object'::text)),
    CONSTRAINT student_card_payloads_mime_check CHECK ((mime = ANY (ARRAY['image/png'::text, 'image/jpeg'::text]))),
    CONSTRAINT student_card_payloads_pkey PRIMARY KEY (asset_id)
);
COMMENT ON TABLE public.student_card_payloads IS '学生卡待外部持久化的短期信封密文，扫描终结后移除，不存图片明文';
COMMENT ON COLUMN public.student_card_payloads.asset_id IS '对应学生卡对象，物理清理时级联移除暂存密文';
COMMENT ON COLUMN public.student_card_payloads.envelope IS '学生卡图片的认证信封密文，不包含明文数据密钥';
COMMENT ON COLUMN public.student_card_payloads.mime IS '重新编码后的图片媒体类型';

CREATE TABLE public.student_login_exchanges (
    id uuid NOT NULL,
    code_hash bytea NOT NULL,
    request_hash bytea NOT NULL,
    nonce bytea NOT NULL,
    status text DEFAULT 'processing'::text NOT NULL,
    session_id uuid,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT student_login_exchanges_check CHECK (((status = 'completed'::text) = (session_id IS NOT NULL))),
    CONSTRAINT student_login_exchanges_code_hash_check CHECK ((octet_length(code_hash) = 32)),
    CONSTRAINT student_login_exchanges_nonce_check CHECK ((octet_length(nonce) = 32)),
    CONSTRAINT student_login_exchanges_request_hash_check CHECK ((octet_length(request_hash) = 32)),
    CONSTRAINT student_login_exchanges_status_check CHECK ((status = ANY (ARRAY['processing'::text, 'completed'::text]))),
    CONSTRAINT student_login_exchanges_code_hash_key UNIQUE (code_hash),
    CONSTRAINT student_login_exchanges_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.student_login_exchanges IS '学生微信一次性交换去重及短期会话引用回执，不保存微信code、身份或令牌';
COMMENT ON COLUMN public.student_login_exchanges.id IS '随机交换回执标识';
COMMENT ON COLUMN public.student_login_exchanges.code_hash IS '用途隔离HMAC的微信code摘要，唯一限制一次交换';
COMMENT ON COLUMN public.student_login_exchanges.request_hash IS '用途隔离HMAC的客户端请求标识摘要，防止跨请求重放';
COMMENT ON COLUMN public.student_login_exchanges.nonce IS '独立随机会话派生材料，单独持有不能恢复令牌';
COMMENT ON COLUMN public.student_login_exchanges.status IS '交换处理中或已原子提交身份及会话的完成状态';
COMMENT ON COLUMN public.student_login_exchanges.session_id IS '完成后绑定的学生会话，随会话物理清理级联删除';
COMMENT ON COLUMN public.student_login_exchanges.expires_at IS '同code成功回执绝对有效期，到期必须重新获取code';
COMMENT ON COLUMN public.student_login_exchanges.created_at IS '数据库记录的领取时间';

CREATE UNIQUE INDEX admin_accounts_login_name_idx ON public.admin_accounts USING btree (login_name) WHERE (login_name IS NOT NULL);
COMMENT ON INDEX public.admin_accounts_login_name_idx IS '非空本地后台登录名唯一约束';

CREATE INDEX blocks_target_idx ON public.blocks USING btree (target_id, owner_id);

CREATE UNIQUE INDEX identity_reviews_pending_unique ON public.identity_reviews USING btree (account_id) WHERE (status = 'pending'::text);

CREATE INDEX sessions_subject_idx ON public.sessions USING btree (subject_kind, subject_id);

ALTER TABLE public.sessions ENABLE ROW LEVEL SECURITY;

INSERT INTO public.interaction_block_policy(singleton) VALUES (true);
