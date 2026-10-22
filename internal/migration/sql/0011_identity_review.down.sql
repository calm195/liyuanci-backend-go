LOCK TABLE public.identity_reviews IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.identity_reviews WHERE data_cipher IS NOT NULL OR revision_suggestion IS NOT NULL) THEN
 RAISE EXCEPTION '人工认证材料存在，拒绝丢失数据回退';
 END IF;
END $$;
ALTER TABLE public.identity_reviews DROP CONSTRAINT identity_reviews_material_rejection;
ALTER TABLE public.identity_reviews DROP COLUMN revision_suggestion;
ALTER TABLE public.identity_reviews DROP COLUMN data_cipher;
