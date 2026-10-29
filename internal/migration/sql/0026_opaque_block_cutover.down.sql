DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.interaction_block_policy WHERE opaque_enabled) THEN
 RAISE EXCEPTION '匿名黑名单已经切换，禁止回退泄漏接口';
 END IF;
END $$;
DROP TABLE public.interaction_block_policy;
DROP FUNCTION public.protect_interaction_block_policy();

DROP FUNCTION public.clear_held_whisper_comment(bytea,uuid,jsonb);

ALTER TABLE public.whisper_notification_refs DROP COLUMN suppressed;
