CREATE TABLE public.wish_detail_views (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    wish_id uuid NOT NULL REFERENCES public.wishes(id) ON DELETE CASCADE,
    viewed_at timestamptz NOT NULL
);
COMMENT ON TABLE public.wish_detail_views IS '公开心愿详情打开事件，仅用于热度统计，不记录访客身份';
COMMENT ON COLUMN public.wish_detail_views.id IS '详情打开事件编号';
COMMENT ON COLUMN public.wish_detail_views.wish_id IS '被打开的公开心愿';
COMMENT ON COLUMN public.wish_detail_views.viewed_at IS '详情打开时间';
CREATE INDEX wish_detail_views_wish_time_idx ON public.wish_detail_views(wish_id, viewed_at);

ALTER TABLE public.wish_detail_views ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.wish_detail_views FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.wish_detail_views USING (true) WITH CHECK (true);
REVOKE ALL ON public.wish_detail_views FROM PUBLIC;
