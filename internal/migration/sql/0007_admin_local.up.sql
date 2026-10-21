-- 登录名只用于新建本地账号，既有外部引用或历史夹具不推断登录名。
ALTER TABLE public.admin_accounts ADD COLUMN login_name text CHECK (login_name IS NULL OR length(btrim(login_name)) > 0);
COMMENT ON COLUMN public.admin_accounts.login_name IS '本地后台登录名，精确匹配且唯一，历史账号不自动补名';
CREATE UNIQUE INDEX admin_accounts_login_name_idx ON public.admin_accounts(login_name) WHERE login_name IS NOT NULL;
COMMENT ON INDEX public.admin_accounts_login_name_idx IS '非空本地后台登录名唯一约束';
CREATE TABLE public.admin_account_actions (
    id uuid PRIMARY KEY,
    actor_id uuid NOT NULL REFERENCES public.admin_accounts(id),
    target_id uuid NOT NULL REFERENCES public.admin_accounts(id),
    action_kind text NOT NULL CHECK (action_kind IN ('bootstrap','create','password_change')),
    reason text NOT NULL CHECK (length(btrim(reason)) > 0),
    at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE public.admin_account_actions IS '后台账号创建及改密操作留迹，不保存口令、令牌或请求正文';
COMMENT ON COLUMN public.admin_account_actions.id IS '操作随机标识';
COMMENT ON COLUMN public.admin_account_actions.actor_id IS '执行操作的后台账号，初始超管初始化时为自身';
COMMENT ON COLUMN public.admin_account_actions.target_id IS '本次操作的后台目标账号';
COMMENT ON COLUMN public.admin_account_actions.action_kind IS '后台初始化、创建或本人改密动作';
COMMENT ON COLUMN public.admin_account_actions.reason IS '创建理由或服务端固定的本人改密用途，不接收口令';
COMMENT ON COLUMN public.admin_account_actions.at IS '事务记录时间';
