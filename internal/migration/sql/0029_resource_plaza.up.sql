CREATE TABLE public.resource_categories (
id uuid PRIMARY KEY,
name text NOT NULL,
name_key text NOT NULL CONSTRAINT uni_resource_categories_name_key UNIQUE,
sort_order integer NOT NULL CHECK (sort_order BETWEEN 0 AND 9999),
enabled boolean NOT NULL,
version bigint NOT NULL CHECK (version BETWEEN 1 AND 9007199254740991),
created_at timestamptz NOT NULL,
updated_at timestamptz NOT NULL);
COMMENT ON TABLE public.resource_categories IS '资料单级分类';
COMMENT ON COLUMN public.resource_categories.id IS '稳定分类标识';
COMMENT ON COLUMN public.resource_categories.name IS '规范分类名称';
COMMENT ON COLUMN public.resource_categories.name_key IS 'ASCII折叠名称唯一键';
COMMENT ON COLUMN public.resource_categories.sort_order IS '显示排序';
COMMENT ON COLUMN public.resource_categories.enabled IS '是否启用';
COMMENT ON COLUMN public.resource_categories.version IS '分类并发版本';
COMMENT ON COLUMN public.resource_categories.created_at IS '创建时间';
COMMENT ON COLUMN public.resource_categories.updated_at IS '修改时间';
ALTER TABLE public.resource_categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.resource_categories FORCE ROW LEVEL SECURITY;
CREATE TABLE public.resource_materials (
id uuid PRIMARY KEY,
category_id uuid NOT NULL REFERENCES public.resource_categories(id),
title text NOT NULL,
description text NOT NULL,
download_cipher jsonb NOT NULL,
status text NOT NULL CHECK (status IN ('draft','published','offline')),
version bigint NOT NULL CHECK (version BETWEEN 1 AND 9007199254740991),
content_version bigint NOT NULL CHECK (content_version BETWEEN 1 AND version),
content_blocked boolean NOT NULL DEFAULT false,
privacy_blocked boolean NOT NULL DEFAULT false,
published_at timestamptz,
created_at timestamptz NOT NULL,
updated_at timestamptz NOT NULL);
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
ALTER TABLE public.resource_materials ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.resource_materials FORCE ROW LEVEL SECURITY;
CREATE TABLE public.resource_feedback (
id uuid PRIMARY KEY,
resource_id uuid NOT NULL REFERENCES public.resource_materials(id),
account_id uuid NOT NULL REFERENCES public.accounts(id),
content_version bigint NOT NULL CHECK (content_version BETWEEN 1 AND 9007199254740991),
kind text NOT NULL CHECK (kind IN ('valid','link_invalid','outdated','incomplete')),
version bigint NOT NULL CHECK (version BETWEEN 1 AND 9007199254740991),
updated_at timestamptz NOT NULL ,UNIQUE(resource_id,account_id,content_version));
COMMENT ON TABLE public.resource_feedback IS '资料当前内容版本学生反馈';
COMMENT ON COLUMN public.resource_feedback.id IS '反馈技术标识及幂等引用';
COMMENT ON COLUMN public.resource_feedback.resource_id IS '资料标识';
COMMENT ON COLUMN public.resource_feedback.account_id IS '反馈主体，注销清理';
COMMENT ON COLUMN public.resource_feedback.content_version IS '反馈内容版本';
COMMENT ON COLUMN public.resource_feedback.kind IS '四类质量反馈';
COMMENT ON COLUMN public.resource_feedback.version IS '本人反馈并发版本';
COMMENT ON COLUMN public.resource_feedback.updated_at IS '反馈更新时间';
ALTER TABLE public.resource_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.resource_feedback FORCE ROW LEVEL SECURITY;
CREATE TABLE public.resource_audits (
id uuid PRIMARY KEY,
actor_id uuid NOT NULL REFERENCES public.admin_accounts(id),
target_id uuid NOT NULL,
operation text NOT NULL,
version bigint NOT NULL,
reason_cipher jsonb,
created_at timestamptz NOT NULL);
COMMENT ON TABLE public.resource_audits IS '资料后台变更审计';
COMMENT ON COLUMN public.resource_audits.id IS '审计标识';
COMMENT ON COLUMN public.resource_audits.actor_id IS '后台操作者';
COMMENT ON COLUMN public.resource_audits.target_id IS '资料或分类技术引用';
COMMENT ON COLUMN public.resource_audits.operation IS '变更动作';
COMMENT ON COLUMN public.resource_audits.version IS '变更后版本';
COMMENT ON COLUMN public.resource_audits.reason_cipher IS '下架理由密文';
COMMENT ON COLUMN public.resource_audits.created_at IS '审计时间';
ALTER TABLE public.resource_audits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.resource_audits FORCE ROW LEVEL SECURITY;
CREATE INDEX resource_materials_page ON public.resource_materials(created_at DESC,id DESC);
CREATE INDEX resource_materials_category ON public.resource_materials(category_id);
CREATE INDEX resource_feedback_actor ON public.resource_feedback(account_id);
ALTER TABLE public.resource_materials ADD CHECK ((status<>'published' OR (published_at IS NOT NULL AND NOT content_blocked AND NOT privacy_blocked)) AND (status<>'draft' OR published_at IS NULL));
CREATE FUNCTION public.is_live_admin_session(token_digest bytea) RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$ SELECT EXISTS(SELECT 1 FROM public.sessions s JOIN public.admin_accounts a ON a.id=s.admin_id WHERE s.token_hash=token_digest AND s.subject_kind='admin' AND a.status='active' AND a.auth_epoch=s.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>statement_timestamp()) $$;
COMMENT ON FUNCTION public.is_live_admin_session(bytea) IS '仅识别已持有有效后台会话的跨域拒绝，不暴露身份';
REVOKE ALL ON FUNCTION public.is_live_admin_session(bytea) FROM PUBLIC;
