DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.report_types) OR EXISTS (SELECT 1 FROM public.reports WHERE content_id IS NOT NULL OR description_cipher IS NOT NULL OR decision_reason IS NOT NULL) OR EXISTS (SELECT 1 FROM public.penalties WHERE content_id IS NOT NULL OR reason_cipher IS NOT NULL) OR EXISTS (SELECT 1 FROM public.content_restrictions WHERE reason_cipher IS NOT NULL OR release_reason_cipher IS NOT NULL) OR EXISTS (SELECT 1 FROM public.appeals WHERE version>1) THEN
  RAISE EXCEPTION '存在安全治理数据，拒绝丢失案件版本或处置理由的回退';
 END IF;
END $$;
ALTER TABLE public.content_restrictions DROP COLUMN release_reason_cipher, DROP COLUMN reason_cipher;
DROP TABLE public.report_types;
DROP INDEX public.appeals_queue_idx;
DROP INDEX public.reports_queue_idx;
ALTER TABLE public.penalties DROP COLUMN reason_cipher, DROP COLUMN content_id;
ALTER TABLE public.appeals DROP COLUMN version;
ALTER TABLE public.reports DROP COLUMN decision_reason, DROP COLUMN description_cipher, DROP COLUMN content_id, DROP COLUMN version;
ALTER TABLE public.blocks DROP COLUMN version;
