ALTER TABLE public.wishes ADD COLUMN draft_deleted_at timestamptz;
COMMENT ON COLUMN public.wishes.draft_deleted_at IS '草稿删除生效时间，保留技术引用但不再允许读取或复活';
ALTER TABLE public.moderation_tasks ADD COLUMN restriction_epoch bigint CHECK (restriction_epoch IS NULL OR restriction_epoch > 0);
COMMENT ON COLUMN public.moderation_tasks.restriction_epoch IS '提交时内容限制代次，历史空值不得作为当前心愿上架依据';
ALTER TABLE public.wish_versions ADD COLUMN review_task_id uuid REFERENCES public.moderation_tasks(id);
COMMENT ON COLUMN public.wish_versions.review_task_id IS '当前显式送审任务引用，旧任务不能覆盖本版本的新审核轮次';
CREATE INDEX wishes_visible_owner_idx ON public.wishes(owner_id,id) WHERE draft_deleted_at IS NULL;
