ALTER TABLE public.wordlist_versions DROP CONSTRAINT wordlist_versions_status_check;
UPDATE public.wordlist_versions SET status='archived' WHERE status='retired';
ALTER TABLE public.wordlist_versions ADD CONSTRAINT wordlist_versions_status_check CHECK (status IN ('draft','published','archived'));
ALTER TABLE public.wordlist_versions DROP CONSTRAINT wordlist_versions_type_version_key;
COMMENT ON COLUMN public.wordlist_versions.version IS '快照资源状态乐观版本，不是类型基准代次';
ALTER TABLE public.wordlist_versions ADD COLUMN base_effective_id uuid REFERENCES public.wordlist_versions(id);
COMMENT ON COLUMN public.wordlist_versions.base_effective_id IS '创建完整快照时的生效基准，发布时须与当前生效版本一致';
ALTER TABLE public.moderation_tasks ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0);
COMMENT ON COLUMN public.moderation_tasks.version IS '审核案件独立乐观版本，不是内容版本或心愿版本';
CREATE TABLE public.wordlist_heads (
 kind text PRIMARY KEY CHECK (kind IN ('crisis','school','ordinary_violation')),
 version bigint NOT NULL DEFAULT 1 CHECK (version>0),
 effective_id uuid REFERENCES public.wordlist_versions(id)
);
COMMENT ON TABLE public.wordlist_heads IS '三类词库的并发基准与生效指针，草案创建和发布均推进代次';
COMMENT ON COLUMN public.wordlist_heads.kind IS '固定词库用途类型';
COMMENT ON COLUMN public.wordlist_heads.version IS '并发基准代次，与快照资源状态版本独立';
COMMENT ON COLUMN public.wordlist_heads.effective_id IS '当前生效不可变快照，无正式发布时为空';
DO $$ BEGIN
 IF EXISTS (SELECT type FROM public.wordlist_versions WHERE status='published' GROUP BY type HAVING count(*)>1) THEN
  RAISE EXCEPTION '存在多份生效词库，须先显式协调';
 END IF;
END $$;
CREATE UNIQUE INDEX wordlist_one_published_idx ON public.wordlist_versions(type) WHERE status='published';
INSERT INTO public.wordlist_heads(kind,effective_id) SELECT k.kind,v.id FROM (VALUES ('crisis'),('school'),('ordinary_violation')) k(kind) LEFT JOIN public.wordlist_versions v ON v.type=k.kind AND v.status='published';
CREATE TABLE public.moderation_scans (
 task_id uuid PRIMARY KEY REFERENCES public.moderation_tasks(id),
 crisis_version uuid NOT NULL REFERENCES public.wordlist_versions(id),
 school_version uuid NOT NULL REFERENCES public.wordlist_versions(id),
 ordinary_version uuid NOT NULL REFERENCES public.wordlist_versions(id),
 detector_version text NOT NULL CHECK (length(btrim(detector_version))>0),
 school_related boolean NOT NULL,
 ordinary_hit boolean NOT NULL,
 crisis_hit boolean NOT NULL,
 projection_cipher jsonb NOT NULL
);
COMMENT ON TABLE public.moderation_scans IS '精确案件扫描快照，三类规则版本与加密脱敏候选不可变';
COMMENT ON COLUMN public.moderation_scans.task_id IS '本轮精确审核案件引用';
COMMENT ON COLUMN public.moderation_scans.crisis_version IS '本次使用的危机规则快照';
COMMENT ON COLUMN public.moderation_scans.school_version IS '本次使用的涉校规则快照';
COMMENT ON COLUMN public.moderation_scans.ordinary_version IS '本次使用的普通违规规则快照';
COMMENT ON COLUMN public.moderation_scans.detector_version IS '文本安全与组合信息处理器版本';
COMMENT ON COLUMN public.moderation_scans.school_related IS '当前版本涉校判定结果';
COMMENT ON COLUMN public.moderation_scans.ordinary_hit IS '普通规则或文本安全处理器要求人工复核';
COMMENT ON COLUMN public.moderation_scans.crisis_hit IS '危机命中，只能在危机专用范围处理';
COMMENT ON COLUMN public.moderation_scans.projection_cipher IS '待放行脱敏正文与标签的加密候选，不向列表公开';
CREATE INDEX moderation_tasks_human_idx ON public.moderation_tasks(id) WHERE status='human_required';
