LOCK TABLE public.moderation_tasks IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.moderation_tasks WHERE restriction_epoch=0) THEN
  RAISE EXCEPTION '存在零代次审核任务，拒绝回退';
 END IF;
END $$;
ALTER TABLE public.moderation_tasks DROP CONSTRAINT moderation_tasks_restriction_epoch_check;
ALTER TABLE public.moderation_tasks ADD CONSTRAINT moderation_tasks_restriction_epoch_check CHECK (restriction_epoch IS NULL OR restriction_epoch > 0);
COMMENT ON COLUMN public.moderation_tasks.restriction_epoch IS '提交时内容限制代次，历史空值不得作为当前心愿上架依据';
