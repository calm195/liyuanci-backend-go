CREATE TABLE public.matchmaking_profiles (
    account_id uuid PRIMARY KEY REFERENCES public.accounts(id) ON DELETE CASCADE,
    version bigint NOT NULL CHECK (version > 0),
    profile_cipher jsonb NOT NULL
);
COMMENT ON TABLE public.matchmaking_profiles IS '账号维护的包分配个人资料';
COMMENT ON COLUMN public.matchmaking_profiles.account_id IS '资料所属账号';
COMMENT ON COLUMN public.matchmaking_profiles.version IS '资料乐观版本及密文绑定版本';
COMMENT ON COLUMN public.matchmaking_profiles.profile_cipher IS '账号资料密文，报名时另建独立快照';
ALTER TABLE public.matchmaking_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_profiles FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_profiles USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_profiles FROM PUBLIC;
COMMENT ON COLUMN public.matchmaking_activities.campuses IS '历史参与校区配置，当前报名不以此限制';
