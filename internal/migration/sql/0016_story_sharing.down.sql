DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.wishes WHERE current_story_version<>0) OR EXISTS (SELECT 1 FROM public.story_versions WHERE review_task_id IS NOT NULL) OR EXISTS (SELECT 1 FROM public.share_links) THEN
 RAISE EXCEPTION '存在故事或分享业务数据，拒绝有损回滚';
 END IF;
END $$;
DROP TABLE public.share_links;
ALTER TABLE public.story_versions DROP COLUMN review_task_id,DROP COLUMN identity_tags,DROP COLUMN wordlist_version,DROP COLUMN source_match_id,DROP COLUMN peer_epoch,DROP COLUMN parent_hold_epoch;
ALTER TABLE public.wishes DROP COLUMN current_story_version;
