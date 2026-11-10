CREATE TABLE public.wish_supports (
    wish_id uuid NOT NULL REFERENCES public.wishes(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (wish_id, account_id)
);
COMMENT ON TABLE public.wish_supports IS '当前有效的心愿支持记录，每个账号对同一心愿最多支持一次';
COMMENT ON COLUMN public.wish_supports.wish_id IS '获得支持的心愿';
COMMENT ON COLUMN public.wish_supports.account_id IS '发出支持的学生账号';
COMMENT ON COLUMN public.wish_supports.created_at IS '本次支持生效的时间';
CREATE INDEX wish_supports_wish_time_idx ON public.wish_supports(wish_id, created_at);
CREATE INDEX wish_supports_account_idx ON public.wish_supports(account_id);

ALTER TABLE public.wish_supports ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.wish_supports FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.wish_supports USING (true) WITH CHECK (true);
REVOKE ALL ON public.wish_supports FROM PUBLIC;
