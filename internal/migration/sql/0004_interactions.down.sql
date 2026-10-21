-- 排他锁覆盖整个版本，防止空表检查之后并发插入；任何数据存在都拒绝回退。
LOCK TABLE public.wishes, public.wish_versions, public.applications, public.matches, public.conversations, public.messages, public.story_versions, public.content_registry IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.wishes) OR
        EXISTS (SELECT 1 FROM public.wish_versions) OR
        EXISTS (SELECT 1 FROM public.applications) OR
        EXISTS (SELECT 1 FROM public.matches) OR
        EXISTS (SELECT 1 FROM public.conversations) OR
        EXISTS (SELECT 1 FROM public.messages) OR
        EXISTS (SELECT 1 FROM public.story_versions) OR
        EXISTS (SELECT 1 FROM public.content_registry) THEN
        RAISE EXCEPTION '业务表非空，拒绝回退';
    END IF;
END $$;
ALTER TABLE public.wishes DROP CONSTRAINT wishes_current_content_fk;
DROP TABLE public.content_registry;
DROP TABLE public.story_versions;
DROP TABLE public.messages;
DROP TABLE public.conversations;
DROP TABLE public.matches;
DROP TABLE public.applications;
DROP TABLE public.wish_versions;
DROP TABLE public.wishes;
