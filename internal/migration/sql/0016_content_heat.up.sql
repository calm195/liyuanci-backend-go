ALTER TABLE public.whisper_hugs ADD COLUMN activated_at timestamptz;
COMMENT ON COLUMN public.whisper_hugs.activated_at IS '当前抱抱实际点亮时间，历史未知时间为空，取消后清空';

CREATE TABLE public.story_views (
    wish_id uuid NOT NULL REFERENCES public.wishes(id) ON DELETE CASCADE,
    viewer_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
    viewed_at timestamptz NOT NULL,
    PRIMARY KEY (wish_id, viewer_id)
);
COMMENT ON TABLE public.story_views IS '已登录访客的还愿故事去重浏览，只用于统计故事热度';
COMMENT ON COLUMN public.story_views.wish_id IS '已浏览的还愿故事所属心愿';
COMMENT ON COLUMN public.story_views.viewer_id IS '已验证会话对应的内部账号，注销时级联清理';
COMMENT ON COLUMN public.story_views.viewed_at IS '该访客首次浏览这条故事的时间';
CREATE INDEX story_views_viewer_idx ON public.story_views(viewer_id);

ALTER TABLE public.story_views ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.story_views FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.story_views USING (true) WITH CHECK (true);
REVOKE ALL ON public.story_views FROM PUBLIC;
