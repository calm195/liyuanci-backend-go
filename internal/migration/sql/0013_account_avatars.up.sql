CREATE TABLE public.account_avatars (
    id uuid PRIMARY KEY,
    owner_id uuid REFERENCES public.accounts(id) ON DELETE SET NULL,
    object_key text NOT NULL UNIQUE CHECK (object_key = 'avatars/' || id::text || '.png'),
    object_version text NOT NULL CHECK (object_version ~ '^sha256:[a-f0-9]{64}$'),
    byte_size bigint NOT NULL CHECK (byte_size BETWEEN 1 AND 2097152),
    removable_after timestamptz NOT NULL,
    upload_until timestamptz,
    cleanup_started_at timestamptz,
    installed_at timestamptz,
    removed_at timestamptz
);
COMMENT ON TABLE public.account_avatars IS '本人头像独立对象、上传预约与清理回执';
COMMENT ON COLUMN public.account_avatars.id IS '头像随机标识';
COMMENT ON COLUMN public.account_avatars.owner_id IS '头像所属账号，注销后置空并保留清理对象引用';
COMMENT ON COLUMN public.account_avatars.object_key IS '独立头像对象键';
COMMENT ON COLUMN public.account_avatars.object_version IS '规范化头像的SHA-256完整性版本';
COMMENT ON COLUMN public.account_avatars.byte_size IS '规范化PNG的字节数';
COMMENT ON COLUMN public.account_avatars.removable_after IS '允许尝试清理的最早时间，保护在途上传';
COMMENT ON COLUMN public.account_avatars.upload_until IS '外部上传排他预约期限';
COMMENT ON COLUMN public.account_avatars.cleanup_started_at IS '已进入清理，禁止重启上传';
COMMENT ON COLUMN public.account_avatars.installed_at IS '曾安装时间，保留幂等重放判断';
COMMENT ON COLUMN public.account_avatars.removed_at IS '对象清理确认时间';
CREATE INDEX account_avatars_cleanup ON public.account_avatars(removable_after, id) WHERE removed_at IS NULL OR owner_id IS NULL;
ALTER TABLE public.account_avatars ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.accounts ADD COLUMN avatar_id uuid REFERENCES public.account_avatars(id) ON DELETE SET NULL;
COMMENT ON COLUMN public.accounts.avatar_id IS '当前私有头像标识，空时可展示存量头像地址';
