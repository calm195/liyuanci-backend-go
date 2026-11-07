CREATE TABLE public.module_guides (
    id bigint PRIMARY KEY CHECK (id = 1),
    version bigint NOT NULL CHECK (version > 0),
    modules jsonb NOT NULL CHECK (jsonb_typeof(modules) = 'array'),
    published_version bigint NOT NULL CHECK (published_version >= 0),
    published_modules jsonb NOT NULL CHECK (jsonb_typeof(published_modules) = 'array')
);
COMMENT ON TABLE public.module_guides IS '模块使用说明草稿与独立发布快照';
COMMENT ON COLUMN public.module_guides.id IS '唯一配置行标识';
COMMENT ON COLUMN public.module_guides.version IS '草稿并发版本';
COMMENT ON COLUMN public.module_guides.modules IS '模块说明草稿';
COMMENT ON COLUMN public.module_guides.published_version IS '已发布说明版本，零表示尚未发布';
COMMENT ON COLUMN public.module_guides.published_modules IS '已发布模块说明快照';
INSERT INTO public.module_guides VALUES (1, 1, '[]', 0, '[]');
ALTER TABLE public.module_guides ENABLE ROW LEVEL SECURITY;
CREATE TABLE public.module_guide_acknowledgements (
    account_id uuid PRIMARY KEY REFERENCES public.accounts(id) ON DELETE CASCADE,
    version bigint NOT NULL CHECK (version > 0)
);
COMMENT ON TABLE public.module_guide_acknowledgements IS '本人已确认的说明发布版本';
COMMENT ON COLUMN public.module_guide_acknowledgements.account_id IS '确认说明的本人账号，删除账号时级联清理';
COMMENT ON COLUMN public.module_guide_acknowledgements.version IS '本人最近确认的发布版本';
ALTER TABLE public.module_guide_acknowledgements ENABLE ROW LEVEL SECURITY;
