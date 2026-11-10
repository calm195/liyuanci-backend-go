-- 已存在消息时，外键拒绝回退，保留会话及正文。
DELETE FROM public.conversations WHERE matchmaking_pair_id IS NOT NULL;
ALTER TABLE public.conversations
    DROP CONSTRAINT conversations_source_check,
    DROP COLUMN matchmaking_pair_id,
    ALTER COLUMN match_id SET NOT NULL;
COMMENT ON COLUMN public.conversations.match_id IS '唯一对应的匹配关系';
