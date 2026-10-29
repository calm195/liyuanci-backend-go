-- 锁后重验所有新增业务和身份引用，非空拒绝破坏性回退。
LOCK TABLE public.whisper_preferences, public.whisper_nickname_versions, public.whisper_posts, public.whisper_post_versions, public.whisper_aliases, public.whisper_comments, public.whisper_comment_versions, public.whisper_hugs, public.whisper_capsules, public.whisper_notification_refs, public.blocks, public.content_registry, public.reports, public.privacy_holds, evidence.crisis_cases IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.whisper_preferences) OR
 EXISTS (SELECT 1 FROM public.whisper_nickname_versions) OR
 EXISTS (SELECT 1 FROM public.whisper_posts) OR
 EXISTS (SELECT 1 FROM public.whisper_post_versions) OR
 EXISTS (SELECT 1 FROM public.whisper_aliases) OR
 EXISTS (SELECT 1 FROM public.whisper_comments) OR
 EXISTS (SELECT 1 FROM public.whisper_comment_versions) OR
 EXISTS (SELECT 1 FROM public.whisper_hugs) OR
 EXISTS (SELECT 1 FROM public.whisper_capsules) OR
 EXISTS (SELECT 1 FROM public.whisper_notification_refs) OR
 EXISTS (SELECT 1 FROM public.blocks) OR
 EXISTS (SELECT 1 FROM public.content_registry WHERE kind LIKE 'whisper_%') OR
 EXISTS (SELECT 1 FROM public.reports WHERE target_kind LIKE 'whisper_%') OR
 EXISTS (SELECT 1 FROM public.privacy_holds WHERE target_kind LIKE 'whisper_%') OR
 EXISTS (SELECT 1 FROM evidence.crisis_cases WHERE content_type LIKE 'whisper_%' OR identity_status IS NOT NULL) THEN RAISE EXCEPTION '树洞或匿名关系数据非空，拒绝回退'; END IF;
END $$;
ALTER TABLE public.whisper_preferences DROP CONSTRAINT whisper_preferences_active_fk, DROP CONSTRAINT whisper_preferences_candidate_fk;
ALTER TABLE public.whisper_posts DROP CONSTRAINT whisper_posts_current_fk, DROP CONSTRAINT whisper_posts_published_fk;
ALTER TABLE public.whisper_comments DROP CONSTRAINT whisper_comments_current_fk;
DROP TABLE public.whisper_notification_refs;
DROP TABLE public.whisper_capsules;
DROP TABLE public.whisper_hugs;
DROP TABLE public.whisper_comment_versions;
DROP TABLE public.whisper_comments;
DROP TABLE public.whisper_aliases;
DROP TABLE public.whisper_post_versions;
DROP TABLE public.whisper_posts;
DROP TABLE public.whisper_nickname_versions;
DROP TABLE public.whisper_preferences;
ALTER TABLE public.blocks DROP COLUMN block_id;
ALTER TABLE evidence.crisis_cases DROP COLUMN identity_status;
ALTER TABLE public.content_registry DROP CONSTRAINT content_registry_kind_check;
ALTER TABLE public.content_registry ADD CONSTRAINT content_registry_kind_check CHECK (kind IN ('wish','application','message','story'));
ALTER TABLE public.reports DROP CONSTRAINT reports_target_kind_check;
ALTER TABLE public.reports ADD CONSTRAINT reports_target_kind_check CHECK (target_kind IN ('wish','application','message','story','account'));
ALTER TABLE public.privacy_holds DROP CONSTRAINT privacy_holds_target_kind_check;
ALTER TABLE public.privacy_holds ADD CONSTRAINT privacy_holds_target_kind_check CHECK (target_kind IN ('wish','story'));
ALTER TABLE evidence.crisis_cases DROP CONSTRAINT crisis_cases_content_type_check;
ALTER TABLE evidence.crisis_cases ADD CONSTRAINT crisis_cases_content_type_check CHECK (content_type IN ('wish','application','message','story'));
