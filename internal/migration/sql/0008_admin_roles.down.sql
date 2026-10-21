LOCK TABLE public.admin_account_actions IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.admin_account_actions WHERE action_kind='roles_change') THEN
  RAISE EXCEPTION '角色修改留迹非空，拒绝破坏性回退';
 END IF;
END $$;
ALTER TABLE public.admin_account_actions DROP CONSTRAINT admin_account_actions_action_kind_check;
ALTER TABLE public.admin_account_actions ADD CONSTRAINT admin_account_actions_action_kind_check CHECK (action_kind IN ('bootstrap','create','password_change'));
COMMENT ON COLUMN public.admin_account_actions.action_kind IS '后台初始化、创建或本人改密动作';
COMMENT ON TABLE public.admin_account_actions IS '后台账号创建及改密操作留迹，不保存口令、令牌或请求正文';
