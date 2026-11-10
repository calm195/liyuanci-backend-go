ALTER TABLE public.conversations
    ALTER COLUMN match_id DROP NOT NULL,
    ADD COLUMN matchmaking_pair_id uuid,
    ADD CONSTRAINT conversations_matchmaking_pair_key UNIQUE (matchmaking_pair_id),
    ADD CONSTRAINT conversations_matchmaking_pair_fkey FOREIGN KEY (matchmaking_pair_id) REFERENCES public.matchmaking_pairs(id),
    ADD CONSTRAINT conversations_source_check CHECK (num_nonnulls(match_id, matchmaking_pair_id) = 1);

COMMENT ON COLUMN public.conversations.match_id IS '心愿会话对应的唯一匹配关系，包分配会话为空';
COMMENT ON COLUMN public.conversations.matchmaking_pair_id IS '包分配会话对应的唯一已发布配对，心愿会话为空';

INSERT INTO public.conversations (id, matchmaking_pair_id, created_at)
SELECT gen_random_uuid(), id, published_at FROM public.matchmaking_pairs
WHERE status = 'published';
