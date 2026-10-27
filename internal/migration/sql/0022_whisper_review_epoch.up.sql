-- 树洞限制代次从零开始；不能用偏移后的数字冒充权威快照。
ALTER TABLE public.moderation_tasks DROP CONSTRAINT moderation_tasks_restriction_epoch_check;
ALTER TABLE public.moderation_tasks ADD CONSTRAINT moderation_tasks_restriction_epoch_check CHECK (restriction_epoch IS NULL OR restriction_epoch >= 0);
COMMENT ON COLUMN public.moderation_tasks.restriction_epoch IS '提交时内容限制代次，树洞允许初始零；历史空值不得作为自动公开依据';
