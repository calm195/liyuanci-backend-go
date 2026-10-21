ALTER TABLE public.identity_reviews ADD COLUMN data_cipher jsonb CHECK(data_cipher IS NULL OR jsonb_typeof(data_cipher)='object');
ALTER TABLE public.identity_reviews ADD COLUMN revision_suggestion text;
COMMENT ON COLUMN public.identity_reviews.data_cipher IS '按申请版本加密的待审姓名与学号，历史未提交材料为空';
COMMENT ON COLUMN public.identity_reviews.revision_suggestion IS '驳回后的修改建议';
ALTER TABLE public.identity_reviews ADD CONSTRAINT identity_reviews_material_rejection CHECK(data_cipher IS NULL OR status<>'rejected' OR (revision_suggestion IS NOT NULL AND length(btrim(revision_suggestion))>0));
