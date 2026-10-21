-- 非空时拒绝撤销防护字段，不能让已替代事件因结构回退重新变成有效唤醒。
LOCK TABLE public.outbox_events IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.outbox_events) THEN
        RAISE EXCEPTION '发件箱非空，拒绝回退唤醒替代防护';
    END IF;
END $$;
DROP INDEX public.outbox_events_work_active_idx;
ALTER TABLE public.outbox_events DROP COLUMN superseded_at;
