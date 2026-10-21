-- 排他锁覆盖整个版本，防止空表检查之后并发插入；任何数据存在都拒绝回退。
LOCK TABLE public.wordlist_versions, public.wordlist_entries, public.moderation_tasks, public.reports, public.content_restrictions, public.privacy_holds, public.work_items, public.outbox_events, public.consumer_receipts, public.notifications, public.delivery_attempts, public.command_receipts, public.access_audits, public.deletion_jobs, evidence.evidence_cases, evidence.crisis_cases, evidence.assets, evidence.evidence_items, evidence.evidence_actions IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.wordlist_versions) OR
        EXISTS (SELECT 1 FROM public.wordlist_entries) OR
        EXISTS (SELECT 1 FROM public.moderation_tasks) OR
        EXISTS (SELECT 1 FROM public.reports) OR
        EXISTS (SELECT 1 FROM public.content_restrictions) OR
        EXISTS (SELECT 1 FROM public.privacy_holds) OR
        EXISTS (SELECT 1 FROM public.work_items) OR
        EXISTS (SELECT 1 FROM public.outbox_events) OR
        EXISTS (SELECT 1 FROM public.consumer_receipts) OR
        EXISTS (SELECT 1 FROM public.notifications) OR
        EXISTS (SELECT 1 FROM public.delivery_attempts) OR
        EXISTS (SELECT 1 FROM public.command_receipts) OR
        EXISTS (SELECT 1 FROM public.access_audits) OR
        EXISTS (SELECT 1 FROM public.deletion_jobs) OR
        EXISTS (SELECT 1 FROM evidence.evidence_cases) OR
        EXISTS (SELECT 1 FROM evidence.crisis_cases) OR
        EXISTS (SELECT 1 FROM evidence.assets) OR
        EXISTS (SELECT 1 FROM evidence.evidence_items) OR
        EXISTS (SELECT 1 FROM evidence.evidence_actions) THEN
        RAISE EXCEPTION '业务表非空，拒绝回退';
    END IF;
END $$;
ALTER TABLE public.wish_versions DROP CONSTRAINT wish_versions_wordlist_fk;
DROP TABLE evidence.evidence_actions;
DROP TABLE evidence.evidence_items;
DROP TABLE evidence.assets;
DROP TABLE evidence.crisis_cases;
DROP TABLE evidence.evidence_cases;
DROP TABLE public.deletion_jobs;
DROP TABLE public.access_audits;
DROP TABLE public.command_receipts;
DROP TABLE public.delivery_attempts;
DROP TABLE public.notifications;
DROP TABLE public.consumer_receipts;
DROP TABLE public.outbox_events;
DROP TABLE public.work_items;
DROP TABLE public.privacy_holds;
DROP TABLE public.content_restrictions;
DROP TABLE public.reports;
DROP TABLE public.moderation_tasks;
DROP TABLE public.wordlist_entries;
DROP TABLE public.wordlist_versions;
DROP SCHEMA evidence;
