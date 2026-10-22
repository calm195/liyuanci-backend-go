-- 有生命周期记录时拒绝丢弃删除屏障或审核绑定。
DO $$ BEGIN
IF EXISTS(SELECT 1 FROM public.wishes WHERE draft_deleted_at IS NOT NULL) OR EXISTS(SELECT 1 FROM public.wish_versions WHERE review_task_id IS NOT NULL) THEN
RAISE EXCEPTION '存在心愿生命周期记录，拒绝回退';
END IF;
END $$;
DROP INDEX public.wishes_visible_owner_idx;
ALTER TABLE public.wish_versions DROP COLUMN review_task_id;
ALTER TABLE public.moderation_tasks DROP COLUMN restriction_epoch;
ALTER TABLE public.wishes DROP COLUMN draft_deleted_at;
