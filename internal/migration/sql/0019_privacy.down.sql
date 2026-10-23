LOCK TABLE public.deletion_jobs,evidence.evidence_cases,public.wishes,public.moderation_tasks IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.deletion_jobs) OR EXISTS(SELECT 1 FROM evidence.evidence_cases WHERE version<>1) OR EXISTS(SELECT 1 FROM public.wishes WHERE peer_deleted) OR EXISTS(SELECT 1 FROM public.moderation_tasks WHERE privacy_recheck) THEN RAISE EXCEPTION '隐私清理数据存在，拒绝丢失数据回退'; END IF;
END $$;
ALTER TABLE public.wishes DROP COLUMN peer_deleted;
ALTER TABLE evidence.evidence_cases DROP COLUMN version;
DROP INDEX public.deletion_jobs_pending_idx;
DROP POLICY IF EXISTS deletion_account_scope ON public.deletion_jobs;
DROP POLICY IF EXISTS deletion_evidence_scope ON public.deletion_jobs;
ALTER TABLE public.deletion_jobs DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.deletion_jobs DROP COLUMN scope,DROP COLUMN target_ref,DROP COLUMN expires_at,DROP COLUMN next_at,DROP COLUMN attempts;

ALTER TABLE public.moderation_tasks DROP COLUMN privacy_recheck;
