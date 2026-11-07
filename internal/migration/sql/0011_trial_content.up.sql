ALTER TABLE public.wish_versions ADD COLUMN public_title text, ADD COLUMN public_reward_text text;
COMMENT ON COLUMN public.wish_versions.public_title IS '当前内容版本已审标题，历史内容为空';
COMMENT ON COLUMN public.wish_versions.public_reward_text IS '当前内容版本已审自定义报答';
ALTER TABLE public.whisper_post_versions ADD COLUMN title_cipher jsonb, ADD COLUMN public_title text;
COMMENT ON COLUMN public.whisper_post_versions.title_cipher IS '当前内容版本标题用途绑定密文，历史内容为空';
COMMENT ON COLUMN public.whisper_post_versions.public_title IS '当前内容版本已审标题，历史内容为空';
ALTER TABLE public.accounts ADD COLUMN avatar_url text NOT NULL DEFAULT '', ADD COLUMN identity_tags jsonb NOT NULL DEFAULT '{}';
COMMENT ON COLUMN public.accounts.avatar_url IS '用户设置的头像地址';
COMMENT ON COLUMN public.accounts.identity_tags IS '用户自选公开标签，不是认证身份';
ALTER TABLE public.wish_versions DROP CONSTRAINT wish_versions_reward_method_check;
ALTER TABLE public.wish_versions ADD CONSTRAINT wish_versions_reward_method_check CHECK (reward_method IN ('milk_tea','meal','reciprocal_gift','skill_exchange','sincere_thanks','custom'));
ALTER TABLE public.wish_versions ADD CONSTRAINT wish_versions_public_title_check CHECK (public_title IS NULL OR (char_length(public_title) BETWEEN 1 AND 40 AND length(btrim(public_title))>0));
ALTER TABLE public.wish_versions ADD CONSTRAINT wish_versions_public_reward_check CHECK (public_reward_text IS NULL OR char_length(public_reward_text)<=100);
ALTER TABLE public.whisper_post_versions ADD CONSTRAINT whisper_post_versions_public_title_check CHECK (public_title IS NULL OR (char_length(public_title) BETWEEN 1 AND 40 AND length(btrim(public_title))>0));
CREATE FUNCTION public.read_whisper_review_title(token_digest bytea, task_ref uuid) RETURNS TABLE(title jsonb)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path TO 'pg_catalog', 'public'
AS $$
SELECT v.title_cipher FROM public.whisper_post_versions v
JOIN public.moderation_tasks t ON t.id=v.review_task_id
WHERE t.id=task_ref AND EXISTS(SELECT 1 FROM public.read_whisper_review_payload(token_digest,task_ref))
$$;
COMMENT ON FUNCTION public.read_whisper_review_title(token_digest bytea, task_ref uuid) IS '复用普通案件精确授权读取标题密文，不暴露其他版本';
REVOKE ALL ON FUNCTION public.read_whisper_review_title(bytea,uuid) FROM PUBLIC;
