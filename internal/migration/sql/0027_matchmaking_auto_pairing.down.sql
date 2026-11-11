DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.matchmaking_pairs WHERE score>100) OR
 EXISTS(SELECT 1 FROM public.matchmaking_trait_selections GROUP BY account_id,trait_id HAVING count(*)>1) THEN
  RAISE EXCEPTION '存在新评分或双用途标签数据，拒绝不兼容降级';
 END IF;
END $$;
DROP TABLE public.matchmaking_auto_jobs;
DROP FUNCTION public.lock_command_accounts(uuid[]);
ALTER TABLE public.matchmaking_pairs DROP CONSTRAINT matchmaking_pairs_score_check;
ALTER TABLE public.matchmaking_pairs ADD CONSTRAINT matchmaking_pairs_score_check CHECK (score BETWEEN 0 AND 100);
ALTER TABLE public.matchmaking_trait_selections DROP CONSTRAINT matchmaking_trait_selections_pkey;
ALTER TABLE public.matchmaking_trait_selections DROP COLUMN purpose;
ALTER TABLE public.matchmaking_trait_selections ADD PRIMARY KEY (account_id,trait_id);
