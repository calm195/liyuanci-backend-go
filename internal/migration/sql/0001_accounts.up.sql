-- 账号行同时作为后续命令的锁定入口；此迁移不建立任何默认身份。
CREATE TABLE public.accounts (
    id uuid PRIMARY KEY,
    lifecycle text NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active', 'deleting')),
    verification text NOT NULL DEFAULT 'unverified' CHECK (verification IN ('unverified', 'pending', 'verified', 'rejected')),
    auth_epoch bigint NOT NULL DEFAULT 1 CHECK (auth_epoch > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    nickname text NOT NULL CHECK (length(btrim(nickname)) > 0),
    hide_identity_tags boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
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
