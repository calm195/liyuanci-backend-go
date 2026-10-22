ALTER TABLE public.blocks ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0);
COMMENT ON COLUMN public.blocks.version IS '有向拉黑关系版本，解除时校验；重复拉黑不递增';
ALTER TABLE public.reports ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0), ADD COLUMN content_id uuid REFERENCES public.content_registry(id), ADD COLUMN description_cipher jsonb, ADD COLUMN decision_reason text;
COMMENT ON COLUMN public.reports.version IS '举报案件独立乐观版本';
COMMENT ON COLUMN public.reports.content_id IS '举报时可见的精确内容注册，不随内容更新漂移';
COMMENT ON COLUMN public.reports.description_cipher IS '可选举报说明的信封密文';
COMMENT ON COLUMN public.reports.decision_reason IS '后台处置理由，仅案件授权范围使用';
ALTER TABLE public.appeals ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0);
COMMENT ON COLUMN public.appeals.version IS '申诉案件独立乐观版本';
ALTER TABLE public.penalties ADD COLUMN content_id uuid REFERENCES public.content_registry(id), ADD COLUMN reason_cipher jsonb;
COMMENT ON COLUMN public.penalties.content_id IS '触发处罚的精确内容注册，历史处罚可为空';
COMMENT ON COLUMN public.penalties.reason_cipher IS '授权处置理由的信封密文';
CREATE TABLE public.report_types (
 code text PRIMARY KEY CHECK (length(btrim(code))>0),
 label text NOT NULL CHECK (length(btrim(label))>0),
 published boolean NOT NULL DEFAULT false
);
COMMENT ON TABLE public.report_types IS '运营显式交付并发布的举报类型，不预置合成业务规则';
COMMENT ON COLUMN public.report_types.code IS '稳定举报类型码';
COMMENT ON COLUMN public.report_types.label IS '面向举报人的类型名称';
COMMENT ON COLUMN public.report_types.published IS '是否已由运营发布供举报使用';
CREATE INDEX reports_queue_idx ON public.reports(created_at DESC,id DESC);
CREATE INDEX appeals_queue_idx ON public.appeals(created_at DESC,id DESC);
ALTER TABLE public.content_restrictions ADD COLUMN reason_cipher jsonb, ADD COLUMN release_reason_cipher jsonb;
COMMENT ON COLUMN public.content_restrictions.reason_cipher IS '人工下线理由信封密文，绑定本限制标识';
COMMENT ON COLUMN public.content_restrictions.release_reason_cipher IS '解除普通限制的理由信封密文，保留原下线理由';
