LOCK TABLE public.moderation_scans,public.wordlist_heads,public.wordlist_versions,public.moderation_tasks IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.moderation_scans) OR EXISTS(SELECT 1 FROM public.wordlist_versions) OR EXISTS(SELECT 1 FROM public.moderation_tasks) THEN
  RAISE EXCEPTION '审核或词库已有业务记录，禁止破坏性回退';
 END IF;
END $$;
DROP TABLE public.moderation_scans;
DROP TABLE public.wordlist_heads;
DROP INDEX public.wordlist_one_published_idx;
DROP INDEX public.moderation_tasks_human_idx;
ALTER TABLE public.moderation_tasks DROP COLUMN version;
ALTER TABLE public.wordlist_versions DROP COLUMN base_effective_id;
ALTER TABLE public.wordlist_versions DROP CONSTRAINT wordlist_versions_status_check;
ALTER TABLE public.wordlist_versions ADD CONSTRAINT wordlist_versions_status_check CHECK(status IN ('draft','published','retired'));
ALTER TABLE public.wordlist_versions ADD CONSTRAINT wordlist_versions_type_version_key UNIQUE(type,version);
