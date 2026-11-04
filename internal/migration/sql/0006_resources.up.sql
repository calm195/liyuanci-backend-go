-- 资料分类、资料、反馈和审计；版本 SQL 是结构定义的唯一来源。

CREATE TABLE public.resource_audits (
    id uuid NOT NULL,
    actor_id uuid NOT NULL,
    target_id uuid NOT NULL,
    operation text NOT NULL,
    version bigint NOT NULL,
    reason_cipher jsonb,
    created_at timestamp with time zone NOT NULL,
    CONSTRAINT resource_audits_pkey PRIMARY KEY (id)
);

ALTER TABLE ONLY public.resource_audits FORCE ROW LEVEL SECURITY;
COMMENT ON TABLE public.resource_audits IS '资料后台变更审计';
COMMENT ON COLUMN public.resource_audits.id IS '审计标识';
COMMENT ON COLUMN public.resource_audits.actor_id IS '后台操作者';
COMMENT ON COLUMN public.resource_audits.target_id IS '资料或分类技术引用';
COMMENT ON COLUMN public.resource_audits.operation IS '变更动作';
COMMENT ON COLUMN public.resource_audits.version IS '变更后版本';
COMMENT ON COLUMN public.resource_audits.reason_cipher IS '下架理由密文';
COMMENT ON COLUMN public.resource_audits.created_at IS '审计时间';

CREATE TABLE public.resource_categories (
    id uuid NOT NULL,
    name text NOT NULL,
    name_key text NOT NULL,
    sort_order integer NOT NULL,
    enabled boolean NOT NULL,
    version bigint NOT NULL,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT resource_categories_sort_order_check CHECK (((sort_order >= 0) AND (sort_order <= 9999))),
    CONSTRAINT resource_categories_version_check CHECK (((version >= 1) AND (version <= '9007199254740991'::bigint))),
    CONSTRAINT resource_categories_pkey PRIMARY KEY (id),
    CONSTRAINT uni_resource_categories_name_key UNIQUE (name_key)
);

ALTER TABLE ONLY public.resource_categories FORCE ROW LEVEL SECURITY;
COMMENT ON TABLE public.resource_categories IS '资料单级分类';
COMMENT ON COLUMN public.resource_categories.id IS '稳定分类标识';
COMMENT ON COLUMN public.resource_categories.name IS '规范分类名称';
COMMENT ON COLUMN public.resource_categories.name_key IS 'ASCII折叠名称唯一键';
COMMENT ON COLUMN public.resource_categories.sort_order IS '显示排序';
COMMENT ON COLUMN public.resource_categories.enabled IS '是否启用';
COMMENT ON COLUMN public.resource_categories.version IS '分类并发版本';
COMMENT ON COLUMN public.resource_categories.created_at IS '创建时间';
COMMENT ON COLUMN public.resource_categories.updated_at IS '修改时间';

CREATE TABLE public.resource_feedback (
    id uuid NOT NULL,
    resource_id uuid NOT NULL,
    account_id uuid NOT NULL,
    content_version bigint NOT NULL,
    kind text NOT NULL,
    version bigint NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT resource_feedback_content_version_check CHECK (((content_version >= 1) AND (content_version <= '9007199254740991'::bigint))),
    CONSTRAINT resource_feedback_kind_check CHECK ((kind = ANY (ARRAY['valid'::text, 'link_invalid'::text, 'outdated'::text, 'incomplete'::text]))),
    CONSTRAINT resource_feedback_version_check CHECK (((version >= 1) AND (version <= '9007199254740991'::bigint))),
    CONSTRAINT resource_feedback_pkey PRIMARY KEY (id),
    CONSTRAINT resource_feedback_resource_id_account_id_content_version_key UNIQUE (resource_id, account_id, content_version)
);

ALTER TABLE ONLY public.resource_feedback FORCE ROW LEVEL SECURITY;
COMMENT ON TABLE public.resource_feedback IS '资料当前内容版本学生反馈';
COMMENT ON COLUMN public.resource_feedback.id IS '反馈技术标识及幂等引用';
COMMENT ON COLUMN public.resource_feedback.resource_id IS '资料标识';
COMMENT ON COLUMN public.resource_feedback.account_id IS '反馈主体，注销清理';
COMMENT ON COLUMN public.resource_feedback.content_version IS '反馈内容版本';
COMMENT ON COLUMN public.resource_feedback.kind IS '四类质量反馈';
COMMENT ON COLUMN public.resource_feedback.version IS '本人反馈并发版本';
COMMENT ON COLUMN public.resource_feedback.updated_at IS '反馈更新时间';

CREATE TABLE public.resource_materials (
    id uuid NOT NULL,
    category_id uuid NOT NULL,
    title text NOT NULL,
    description text NOT NULL,
    download_cipher jsonb NOT NULL,
    status text NOT NULL,
    version bigint NOT NULL,
    content_version bigint NOT NULL,
    content_blocked boolean DEFAULT false NOT NULL,
    privacy_blocked boolean DEFAULT false NOT NULL,
    published_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL,
    CONSTRAINT resource_materials_check CHECK (((content_version >= 1) AND (content_version <= version))),
    CONSTRAINT resource_materials_check1 CHECK ((((status <> 'published'::text) OR ((published_at IS NOT NULL) AND (NOT content_blocked) AND (NOT privacy_blocked))) AND ((status <> 'draft'::text) OR (published_at IS NULL)))),
    CONSTRAINT resource_materials_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'published'::text, 'offline'::text]))),
    CONSTRAINT resource_materials_version_check CHECK (((version >= 1) AND (version <= '9007199254740991'::bigint))),
    CONSTRAINT resource_materials_pkey PRIMARY KEY (id)
);

ALTER TABLE ONLY public.resource_materials FORCE ROW LEVEL SECURITY;
COMMENT ON TABLE public.resource_materials IS '外部下载资料元信息';
COMMENT ON COLUMN public.resource_materials.id IS '稳定资料标识';
COMMENT ON COLUMN public.resource_materials.category_id IS '启用分类引用';
COMMENT ON COLUMN public.resource_materials.title IS '规范标题';
COMMENT ON COLUMN public.resource_materials.description IS '规范简介';
COMMENT ON COLUMN public.resource_materials.download_cipher IS '用途及内容版本绑定的链接和提取码密文';
COMMENT ON COLUMN public.resource_materials.status IS '资料发布状态';
COMMENT ON COLUMN public.resource_materials.version IS '资料并发版本';
COMMENT ON COLUMN public.resource_materials.content_version IS '正文版本';
COMMENT ON COLUMN public.resource_materials.content_blocked IS '受控内容屏障';
COMMENT ON COLUMN public.resource_materials.privacy_blocked IS '受控隐私屏障';
COMMENT ON COLUMN public.resource_materials.published_at IS '最近上架时间';
COMMENT ON COLUMN public.resource_materials.created_at IS '不可变创建时间';
COMMENT ON COLUMN public.resource_materials.updated_at IS '修改时间';

CREATE INDEX resource_feedback_actor ON public.resource_feedback USING btree (account_id);

CREATE INDEX resource_materials_category ON public.resource_materials USING btree (category_id);

CREATE INDEX resource_materials_page ON public.resource_materials USING btree (created_at DESC, id DESC);

ALTER TABLE public.resource_audits ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.resource_categories ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.resource_feedback ENABLE ROW LEVEL SECURITY;

ALTER TABLE public.resource_materials ENABLE ROW LEVEL SECURITY;
