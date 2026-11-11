ALTER TABLE public.accounts
 ADD COLUMN enrollment_year integer NOT NULL DEFAULT 0,
 ADD COLUMN education_level text NOT NULL DEFAULT '',
 ADD COLUMN grade_override text NOT NULL DEFAULT '',
 ADD CONSTRAINT accounts_education_check CHECK (
  (enrollment_year=0 AND education_level='' AND grade_override='') OR
  (enrollment_year BETWEEN 1900 AND 9999 AND education_level IN ('undergraduate','master','doctor'))
 );
COMMENT ON COLUMN public.accounts.enrollment_year IS '入学年份，零表示尚未补全';
COMMENT ON COLUMN public.accounts.education_level IS '学历层级：本科、硕士研究生、博士研究生';
COMMENT ON COLUMN public.accounts.grade_override IS '手动年级代码，空值表示按入学年份自动计算';
ALTER TABLE public.matchmaking_pairs DROP CONSTRAINT matchmaking_pairs_score_check;
ALTER TABLE public.matchmaking_pairs ADD CONSTRAINT matchmaking_pairs_score_check CHECK (score BETWEEN 0 AND 215);
ALTER TABLE public.matchmaking_pairs ALTER COLUMN score TYPE numeric(12,1) USING score::numeric;
ALTER TABLE public.matchmaking_auto_jobs ALTER COLUMN total_score TYPE numeric(12,1) USING total_score::numeric;
COMMENT ON COLUMN public.matchmaking_pairs.score IS '冻结推荐分数，保留半分';
COMMENT ON COLUMN public.matchmaking_auto_jobs.total_score IS '本轮方案总分，保留半分';
-- 旧预览与新计分规则不兼容，已保存配对保持不变。
UPDATE public.matchmaking_auto_jobs SET status='stale',version=version+1,error='评分规则已更新，请重新生成',result_cipher=NULL WHERE status IN ('pending','running','ready');
