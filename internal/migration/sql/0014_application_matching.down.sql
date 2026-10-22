LOCK TABLE public.applications,public.matches IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.applications WHERE review_task_id IS NOT NULL OR owner_epoch IS NOT NULL OR public_message_cipher IS NOT NULL)
 OR EXISTS(SELECT 1 FROM public.matches WHERE application_id IS NOT NULL OR version<>1) THEN
  RAISE EXCEPTION '应愿或匹配已有新协议数据，禁止破坏性回退';
 END IF;
END $$;
DROP INDEX public.applications_review_task_idx;
DROP INDEX public.matches_application_unique;
ALTER TABLE public.matches DROP COLUMN application_id,DROP COLUMN version;
ALTER TABLE public.applications DROP CONSTRAINT applications_projection_status;
ALTER TABLE public.applications DROP COLUMN review_task_id,DROP COLUMN owner_epoch,DROP COLUMN public_message_cipher;
