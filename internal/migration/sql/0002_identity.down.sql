-- 先获取排他锁再检查，禁止回退删除任何仍存在的密文或检索配置。
LOCK TABLE public.identity_lookup_keys, public.identity_secrets, public.identity_lookup_claims IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.identity_lookup_keys)
        OR EXISTS (SELECT 1 FROM public.identity_secrets)
        OR EXISTS (SELECT 1 FROM public.identity_lookup_claims) THEN
        RAISE EXCEPTION '身份表非空，拒绝回退';
    END IF;
END $$;
DROP TABLE public.identity_lookup_claims;
DROP TABLE public.identity_secrets;
DROP TABLE public.identity_lookup_keys;
