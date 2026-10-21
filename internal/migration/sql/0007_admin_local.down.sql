LOCK TABLE public.admin_accounts, public.admin_account_actions IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.admin_accounts WHERE login_name IS NOT NULL) OR EXISTS (SELECT 1 FROM public.admin_account_actions) THEN
        RAISE EXCEPTION '本地后台账号或留迹非空，拒绝破坏性回退';
    END IF;
END $$;
DROP TABLE public.admin_account_actions;
ALTER TABLE public.admin_accounts DROP COLUMN login_name;
