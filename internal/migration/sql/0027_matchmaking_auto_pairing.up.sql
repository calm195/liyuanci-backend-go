ALTER TABLE public.matchmaking_trait_selections ADD COLUMN purpose text NOT NULL DEFAULT 'ideal' CHECK (purpose IN ('personal','ideal'));
COMMENT ON COLUMN public.matchmaking_trait_selections.purpose IS '选用用途，个人特质或理想型；旧数据属于理想型';
ALTER TABLE public.matchmaking_trait_selections DROP CONSTRAINT matchmaking_trait_selections_pkey;
ALTER TABLE public.matchmaking_trait_selections ADD PRIMARY KEY (account_id,trait_id,purpose);
ALTER TABLE public.matchmaking_pairs DROP CONSTRAINT matchmaking_pairs_score_check;
ALTER TABLE public.matchmaking_pairs ADD CONSTRAINT matchmaking_pairs_score_check CHECK (score BETWEEN 0 AND 200);

CREATE FUNCTION public.lock_command_accounts(subjects uuid[])
RETURNS TABLE(id uuid,lifecycle text,verification text,auth_epoch bigint,version bigint)
LANGUAGE plpgsql SET search_path TO 'pg_catalog' AS $$
DECLARE subject uuid;
BEGIN
 FOR subject IN SELECT DISTINCT value FROM unnest(subjects) value ORDER BY value LOOP
  RETURN QUERY SELECT * FROM public.lock_command_account(subject);
 END LOOP;
END $$;
COMMENT ON FUNCTION public.lock_command_accounts(uuid[]) IS '在一次数据库往返内按统一账号顺序锁定完整参与集合';
REVOKE ALL ON FUNCTION public.lock_command_accounts(uuid[]) FROM PUBLIC;

CREATE TABLE public.matchmaking_auto_jobs (
 id uuid PRIMARY KEY,
 activity_id uuid NOT NULL REFERENCES public.matchmaking_activities(id) ON DELETE CASCADE,
 version bigint NOT NULL CHECK (version>0),
 activity_version bigint NOT NULL CHECK (activity_version>0),
 status text NOT NULL CHECK (status IN ('pending','running','ready','applied','stale','failed')),
 seed bigint NOT NULL,
 algorithm text NOT NULL,
 input_hash text NOT NULL,
 result_cipher jsonb,
 participant_count integer NOT NULL DEFAULT 0,
 eligible_count integer NOT NULL DEFAULT 0,
 paired_count integer NOT NULL DEFAULT 0,
 unmatched_count integer NOT NULL DEFAULT 0,
 excluded_count integer NOT NULL DEFAULT 0,
 total_score integer NOT NULL DEFAULT 0,
 error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 requested_by uuid REFERENCES public.admin_accounts(id) ON DELETE SET NULL
);
COMMENT ON TABLE public.matchmaking_auto_jobs IS '全员自动分配持久预览任务；结果加密保存，确认才建立草稿';
COMMENT ON COLUMN public.matchmaking_auto_jobs.id IS '预览任务标识';
COMMENT ON COLUMN public.matchmaking_auto_jobs.activity_id IS '所属活动';
COMMENT ON COLUMN public.matchmaking_auto_jobs.version IS '任务乐观版本';
COMMENT ON COLUMN public.matchmaking_auto_jobs.activity_version IS '生成时活动版本';
COMMENT ON COLUMN public.matchmaking_auto_jobs.status IS '任务阶段';
COMMENT ON COLUMN public.matchmaking_auto_jobs.seed IS '每次生成独立随机种子，重试不变';
COMMENT ON COLUMN public.matchmaking_auto_jobs.algorithm IS '计分及求解规则版本';
COMMENT ON COLUMN public.matchmaking_auto_jobs.input_hash IS '全部报名、资格、占用和拉黑快照指纹';
COMMENT ON COLUMN public.matchmaking_auto_jobs.result_cipher IS '预览配对和落单结果密文，不保存公开个人资料';
COMMENT ON COLUMN public.matchmaking_auto_jobs.participant_count IS '活动报名总人数';
COMMENT ON COLUMN public.matchmaking_auto_jobs.eligible_count IS '本轮合格且未占用人数';
COMMENT ON COLUMN public.matchmaking_auto_jobs.paired_count IS '本轮方案配对组数';
COMMENT ON COLUMN public.matchmaking_auto_jobs.unmatched_count IS '合格但未配对人数';
COMMENT ON COLUMN public.matchmaking_auto_jobs.excluded_count IS '不参与本轮分配人数';
COMMENT ON COLUMN public.matchmaking_auto_jobs.total_score IS '本轮方案总分';
COMMENT ON COLUMN public.matchmaking_auto_jobs.error IS '安全失败或过期说明，不记录底层错误';
COMMENT ON COLUMN public.matchmaking_auto_jobs.created_at IS '任务创建时间';
COMMENT ON COLUMN public.matchmaking_auto_jobs.expires_at IS '预览最晚确认时间';
COMMENT ON COLUMN public.matchmaking_auto_jobs.requested_by IS '创建任务的运营账号';
CREATE UNIQUE INDEX matchmaking_auto_jobs_active_idx ON public.matchmaking_auto_jobs(activity_id) WHERE status IN ('pending','running');
CREATE INDEX matchmaking_auto_jobs_activity_idx ON public.matchmaking_auto_jobs(activity_id,created_at DESC,id);
ALTER TABLE public.matchmaking_auto_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_auto_jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_auto_jobs USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_auto_jobs FROM PUBLIC;
