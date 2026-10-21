-- 排他锁覆盖整个版本，防止空表检查之后并发插入；任何数据存在都拒绝回退。
LOCK TABLE public.identity_names, public.admin_accounts, public.admin_role_bindings, public.sessions, public.assets, public.identity_reviews, public.blocks, public.penalties, public.appeals IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.identity_names) OR
        EXISTS (SELECT 1 FROM public.admin_accounts) OR
        EXISTS (SELECT 1 FROM public.admin_role_bindings) OR
        EXISTS (SELECT 1 FROM public.sessions) OR
        EXISTS (SELECT 1 FROM public.assets) OR
        EXISTS (SELECT 1 FROM public.identity_reviews) OR
        EXISTS (SELECT 1 FROM public.blocks) OR
        EXISTS (SELECT 1 FROM public.penalties) OR
        EXISTS (SELECT 1 FROM public.appeals) THEN
        RAISE EXCEPTION '业务表非空，拒绝回退';
    END IF;
END $$;
DROP TABLE public.appeals;
DROP TABLE public.penalties;
DROP TABLE public.blocks;
DROP TABLE public.identity_reviews;
DROP TABLE public.assets;
DROP TABLE public.sessions;
DROP TABLE public.admin_role_bindings;
DROP TABLE public.admin_accounts;
DROP TABLE public.identity_names;
