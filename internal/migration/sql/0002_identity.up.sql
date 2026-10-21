-- 检索代次由数据库统一维护；进程配置不得自行切换而跳过旧代查重。
CREATE TABLE public.identity_lookup_keys (
    purpose text PRIMARY KEY CHECK (purpose IN ('student_number', 'openid')),
    current_ref text NOT NULL CHECK (length(btrim(current_ref)) > 0),
    previous_ref text CHECK (previous_ref IS NULL OR (length(btrim(previous_ref)) > 0 AND previous_ref <> current_ref)),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0)
);
COMMENT ON TABLE public.identity_lookup_keys IS '身份检索密钥轮换状态，仅保存外部引用，最多同时使用两代';
COMMENT ON COLUMN public.identity_lookup_keys.purpose IS '检索用途：学号或微信身份';
COMMENT ON COLUMN public.identity_lookup_keys.current_ref IS '当前写入和回填的外部检索密钥引用';
COMMENT ON COLUMN public.identity_lookup_keys.previous_ref IS '轮换期间仍参与查重的旧代引用，回填完成前不得清除';
COMMENT ON COLUMN public.identity_lookup_keys.version IS '轮换状态版本，初始化及每次状态切换递增';

CREATE TABLE public.identity_secrets (
    account_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
    purpose text NOT NULL REFERENCES public.identity_lookup_keys(purpose),
    content_version bigint NOT NULL CHECK (content_version > 0),
    envelope jsonb NOT NULL CHECK (jsonb_typeof(envelope) = 'object'),
    PRIMARY KEY (account_id, purpose)
);
COMMENT ON TABLE public.identity_secrets IS '按账号和用途保存身份字段信封密文，不包含主密钥或明文身份';
COMMENT ON COLUMN public.identity_secrets.account_id IS '拥有该身份字段的普通账号，普通账号物理删除时清理';
COMMENT ON COLUMN public.identity_secrets.purpose IS '身份字段用途，同时作为认证加密绑定及检索域';
COMMENT ON COLUMN public.identity_secrets.content_version IS '密文内容版本，与认证加密附加数据一致';
COMMENT ON COLUMN public.identity_secrets.envelope IS '算法、外部密钥引用、包裹数据密钥、随机数和认证密文';

CREATE TABLE public.identity_lookup_claims (
    purpose text NOT NULL,
    key_ref text NOT NULL CHECK (length(btrim(key_ref)) > 0),
    digest bytea NOT NULL CHECK (octet_length(digest) = 32),
    account_id uuid NOT NULL,
    PRIMARY KEY (purpose, key_ref, digest),
    UNIQUE (account_id, purpose, key_ref),
    FOREIGN KEY (account_id, purpose) REFERENCES public.identity_secrets(account_id, purpose) ON DELETE CASCADE
);
COMMENT ON TABLE public.identity_lookup_claims IS '双代身份唯一占用映射，同一用途同一密钥摘要只能属于一个账号';
COMMENT ON COLUMN public.identity_lookup_claims.purpose IS '用途域，学号与微信身份分别保持唯一';
COMMENT ON COLUMN public.identity_lookup_claims.key_ref IS '计算该检索摘要的外部密钥引用';
COMMENT ON COLUMN public.identity_lookup_claims.digest IS '独立检索密钥计算的用途隔离 HMAC-SHA256，禁止写日志';
COMMENT ON COLUMN public.identity_lookup_claims.account_id IS '唯一占用账号，与对应密文同行生命周期清理';
