CREATE TABLE public.identity_names (
    account_id uuid PRIMARY KEY REFERENCES public.accounts(id) ON DELETE CASCADE,
    content_version bigint NOT NULL CHECK (content_version > 0),
    name_cipher jsonb NOT NULL CHECK (name_cipher IS NULL OR jsonb_typeof(name_cipher) = 'object')
);
COMMENT ON TABLE public.identity_names IS '普通账号姓名密文，独立于检索占用，无姓名查重索引';
COMMENT ON COLUMN public.identity_names.account_id IS '所属账号，普通账号清理时删除';
COMMENT ON COLUMN public.identity_names.content_version IS '姓名内容版本，绑定认证加密附加数据';
COMMENT ON COLUMN public.identity_names.name_cipher IS '姓名，保存信封密文而非明文';

CREATE TABLE public.admin_accounts (
    id uuid PRIMARY KEY,
    credential_ref text CHECK (credential_ref IS NULL OR length(btrim(credential_ref)) > 0),
    password_hash text CHECK (password_hash IS NULL OR length(btrim(password_hash)) > 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    auth_epoch bigint NOT NULL DEFAULT 1 CHECK (auth_epoch > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((credential_ref IS NULL) <> (password_hash IS NULL)),
    UNIQUE (credential_ref)
);
COMMENT ON TABLE public.admin_accounts IS '独立后台账号，不初始化管理员或默认口令';
COMMENT ON COLUMN public.admin_accounts.id IS '随机对象标识';
COMMENT ON COLUMN public.admin_accounts.credential_ref IS '外部后台身份引用，供应商交付前不填入模拟账号';
COMMENT ON COLUMN public.admin_accounts.password_hash IS '可选本地凭据摘要，不能保存明文口令';
COMMENT ON COLUMN public.admin_accounts.status IS '后台账号状态';
COMMENT ON COLUMN public.admin_accounts.auth_epoch IS '后台权限代次，撤销旧会话使用';
COMMENT ON COLUMN public.admin_accounts.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.admin_accounts.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE public.admin_role_bindings (
    admin_id uuid NOT NULL REFERENCES public.admin_accounts(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('review_operator', 'crisis_operator', 'super_admin')),
    PRIMARY KEY (admin_id, role)
);
COMMENT ON TABLE public.admin_role_bindings IS '后台角色绑定，仅由受控超管命令变更';
COMMENT ON COLUMN public.admin_role_bindings.admin_id IS '后台账号';
COMMENT ON COLUMN public.admin_role_bindings.role IS '应用角色，不等同于数据库权限组';

CREATE TABLE public.sessions (
    id uuid PRIMARY KEY,
    account_id uuid REFERENCES public.accounts(id) ON DELETE CASCADE,
    admin_id uuid REFERENCES public.admin_accounts(id) ON DELETE CASCADE,
    subject_kind text GENERATED ALWAYS AS (CASE WHEN account_id IS NOT NULL THEN 'student' ELSE 'admin' END) STORED,
    subject_id uuid GENERATED ALWAYS AS (coalesce(account_id, admin_id)) STORED,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    auth_epoch bigint NOT NULL CHECK (auth_epoch > 0),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((account_id IS NULL) <> (admin_id IS NULL)),
    CHECK (expires_at > created_at)
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

CREATE INDEX sessions_subject_idx ON public.sessions(subject_kind, subject_id);
-- 普通与后台连接共享表但不能跨命名空间读写；显式权限配置前对运行组默认拒绝。
ALTER TABLE public.sessions ENABLE ROW LEVEL SECURITY;
CREATE TABLE public.assets (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES public.accounts(id),
    purpose text NOT NULL CHECK (purpose = 'student_card'),
    encrypted_object_key jsonb NOT NULL CHECK (encrypted_object_key IS NULL OR jsonb_typeof(encrypted_object_key) = 'object'),
    object_version text NOT NULL CHECK (length(btrim(object_version)) > 0),
    wrapped_key_ref text NOT NULL CHECK (length(btrim(wrapped_key_ref)) > 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'deleting', 'deleted', 'failed')),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE public.assets IS '普通学生卡私有对象元数据，删除前须完成外部对象清理';
COMMENT ON COLUMN public.assets.id IS '随机对象标识';
COMMENT ON COLUMN public.assets.owner_id IS '普通账号所有者，不级联跳过外部对象清理';
COMMENT ON COLUMN public.assets.purpose IS '普通对象仅学生卡，证据对象另存隔离域';
COMMENT ON COLUMN public.assets.encrypted_object_key IS '私有对象键，保存信封密文而非明文';
COMMENT ON COLUMN public.assets.object_version IS '外部对象版本，不使用公开下载地址';
COMMENT ON COLUMN public.assets.wrapped_key_ref IS '外部包裹密钥引用，不保存主密钥';
COMMENT ON COLUMN public.assets.status IS '对象处理状态';
COMMENT ON COLUMN public.assets.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE public.identity_reviews (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES public.accounts(id),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    asset_id uuid NOT NULL REFERENCES public.assets(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'obsolete', 'cancelled')),
    reason text,
    reviewer_id uuid REFERENCES public.admin_accounts(id),
    reviewed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status <> 'rejected' OR (reason IS NOT NULL AND length(btrim(reason)) > 0)),
    UNIQUE (account_id, version)
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

CREATE UNIQUE INDEX identity_reviews_pending_unique ON public.identity_reviews(account_id) WHERE status = 'pending';
CREATE TABLE public.blocks (
    owner_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
    target_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (owner_id, target_id),
    CHECK (owner_id <> target_id)
);
COMMENT ON TABLE public.blocks IS '有向拉黑关系，交互授权必须复核任一方向';
COMMENT ON COLUMN public.blocks.owner_id IS '设置拉黑的账号';
COMMENT ON COLUMN public.blocks.target_id IS '被拉黑的账号';
COMMENT ON COLUMN public.blocks.created_at IS '创建时间，以带时区时间保存';

CREATE INDEX blocks_target_idx ON public.blocks(target_id, owner_id);
CREATE TABLE public.penalties (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES public.accounts(id),
    level text NOT NULL CHECK (level IN ('minor', 'general', 'severe')),
    action_kind text NOT NULL CHECK (action_kind IN ('warning', 'mute', 'ban')),
    violation_ref uuid NOT NULL,
    content_version bigint NOT NULL CHECK (content_version > 0),
    starts_at timestamptz NOT NULL,
    ends_at timestamptz,
    reason_ref uuid NOT NULL,
    first_displayed_at timestamptz,
    confirmable_at timestamptz,
    acknowledged_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    revoked_at timestamptz,
    UNIQUE (account_id, violation_ref, action_kind),
    CHECK ((level = 'minor' AND action_kind = 'warning' AND ends_at IS NULL) OR (level = 'general' AND action_kind = 'mute' AND ends_at IS NOT NULL AND ends_at = starts_at + interval '168 hours') OR (level = 'severe' AND action_kind = 'ban' AND ends_at IS NULL)),
    CHECK ((first_displayed_at IS NULL AND confirmable_at IS NULL AND acknowledged_at IS NULL) OR (action_kind = 'warning' AND first_displayed_at IS NOT NULL AND confirmable_at IS NOT NULL AND confirmable_at = first_displayed_at + interval '30 seconds' AND (acknowledged_at IS NULL OR acknowledged_at >= confirmable_at)))
);
COMMENT ON TABLE public.penalties IS '警告、七天禁言与永久封禁，是否限制交互由动作及当前时间判定';
COMMENT ON COLUMN public.penalties.id IS '随机对象标识';
COMMENT ON COLUMN public.penalties.account_id IS '受处置账号';
COMMENT ON COLUMN public.penalties.level IS '违规等级';
COMMENT ON COLUMN public.penalties.action_kind IS '处置动作，warning 不限制交互';
COMMENT ON COLUMN public.penalties.violation_ref IS '违规事件引用，不直接依赖证据主体存续';
COMMENT ON COLUMN public.penalties.content_version IS '触发处置的精确内容版本';
COMMENT ON COLUMN public.penalties.starts_at IS '处置开始时间';
COMMENT ON COLUMN public.penalties.ends_at IS '七天禁言的结束时间，警告和永久封禁为空';
COMMENT ON COLUMN public.penalties.reason_ref IS '处置原因技术引用，不复制正文';
COMMENT ON COLUMN public.penalties.first_displayed_at IS '首次警告展示时间，后续重进不得重置';
COMMENT ON COLUMN public.penalties.confirmable_at IS '首次展示三十秒后的可确认时间';
COMMENT ON COLUMN public.penalties.acknowledged_at IS '本人确认警告时间，不得提前';
COMMENT ON COLUMN public.penalties.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.penalties.revoked_at IS '经授权撤销时间';

CREATE INDEX penalties_account_idx ON public.penalties(account_id, action_kind, starts_at);
CREATE TABLE public.appeals (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES public.accounts(id),
    penalty_id uuid NOT NULL REFERENCES public.penalties(id),
    body_cipher jsonb NOT NULL CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected')),
    handler_id uuid REFERENCES public.admin_accounts(id),
    decision_reason text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status <> 'accepted' OR (decision_reason IS NOT NULL AND length(btrim(decision_reason)) > 0))
);
COMMENT ON TABLE public.appeals IS '受限账号可提交的处罚申诉，成功解罚必须有理由';
COMMENT ON COLUMN public.appeals.id IS '随机对象标识';
COMMENT ON COLUMN public.appeals.account_id IS '申诉账号';
COMMENT ON COLUMN public.appeals.penalty_id IS '申诉目标处置，主体一致性由命令复核';
COMMENT ON COLUMN public.appeals.body_cipher IS '申诉正文，保存信封密文而非明文';
COMMENT ON COLUMN public.appeals.status IS '申诉处理状态';
COMMENT ON COLUMN public.appeals.handler_id IS '处理人';
COMMENT ON COLUMN public.appeals.decision_reason IS '处置结论理由';
COMMENT ON COLUMN public.appeals.created_at IS '创建时间，以带时区时间保存';
