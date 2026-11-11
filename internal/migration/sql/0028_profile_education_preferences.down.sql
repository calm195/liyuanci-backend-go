-- 有半分数据时拒绝回退，避免静默丢失已经保存的评分。
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.matchmaking_pairs WHERE score <> trunc(score) OR score > 200) OR EXISTS (SELECT 1 FROM public.matchmaking_auto_jobs WHERE total_score <> trunc(total_score)) THEN
  RAISE EXCEPTION '存在半分评分，不能回退为整数';
 END IF;
END $$;
ALTER TABLE public.matchmaking_pairs ALTER COLUMN score TYPE integer USING score::integer;
ALTER TABLE public.matchmaking_auto_jobs ALTER COLUMN total_score TYPE integer USING total_score::integer;
ALTER TABLE public.accounts DROP CONSTRAINT accounts_education_check, DROP COLUMN grade_override, DROP COLUMN education_level, DROP COLUMN enrollment_year;
COMMENT ON COLUMN public.matchmaking_pairs.score IS '冻结推荐分数';
COMMENT ON COLUMN public.matchmaking_auto_jobs.total_score IS '本轮方案总分';

ALTER TABLE public.matchmaking_pairs DROP CONSTRAINT matchmaking_pairs_score_check;
ALTER TABLE public.matchmaking_pairs ADD CONSTRAINT matchmaking_pairs_score_check CHECK (score BETWEEN 0 AND 200);
