ALTER TABLE public.moderation_tasks ADD COLUMN wechat_manual_review boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN public.moderation_tasks.wechat_manual_review IS '微信内容安全要求该版本人工复核';
