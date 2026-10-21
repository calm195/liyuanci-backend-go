-- 回退仅允许空表，避免运维误操作静默删除账号；有数据时须另行审查迁移方案。
-- 先排他锁表，避免空表检查与删除之间插入新账号而丢失数据。
LOCK TABLE public.accounts IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.accounts) THEN
        RAISE EXCEPTION '账号表非空，拒绝回退';
    END IF;
END $$;
DROP TABLE public.accounts;
