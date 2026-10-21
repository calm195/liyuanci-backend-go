-- 仅扩展留迹动作，保留既有记录及历史迁移校验和。
ALTER TABLE public.admin_account_actions DROP CONSTRAINT admin_account_actions_action_kind_check;
ALTER TABLE public.admin_account_actions ADD CONSTRAINT admin_account_actions_action_kind_check CHECK (action_kind IN ('bootstrap','create','password_change','roles_change'));
COMMENT ON COLUMN public.admin_account_actions.action_kind IS '后台初始化、创建、本人改密或超管角色修改动作';
COMMENT ON TABLE public.admin_account_actions IS '后台账号及角色操作留迹，不保存口令、令牌或请求正文';
