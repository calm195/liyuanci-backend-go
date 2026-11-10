CREATE TABLE public.matchmaking_activities (
    id uuid PRIMARY KEY,
    version bigint NOT NULL DEFAULT 1 CHECK (version>0),
    title text NOT NULL,
    description text NOT NULL,
    status text NOT NULL CHECK (status IN ('draft','published','cancelled')),
    published_at timestamptz,
    tasks_published_at timestamptz,
    campuses jsonb NOT NULL,
    registration_opens_at timestamptz NOT NULL,
    registration_closes_at timestamptz NOT NULL,
    results_at timestamptz NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz NOT NULL,
    days integer NOT NULL CHECK (days BETWEEN 1 AND 30),
    created_at timestamptz NOT NULL,
    CHECK (registration_opens_at < registration_closes_at AND registration_closes_at <= results_at AND results_at <= starts_at AND starts_at < ends_at)
);
COMMENT ON TABLE public.matchmaking_activities IS '包分配活动';
COMMENT ON COLUMN public.matchmaking_activities.id IS '活动随机标识';
COMMENT ON COLUMN public.matchmaking_activities.version IS '乐观版本';
COMMENT ON COLUMN public.matchmaking_activities.title IS '活动名称';
COMMENT ON COLUMN public.matchmaking_activities.description IS '经运营审核的活动说明';
COMMENT ON COLUMN public.matchmaking_activities.status IS '活动发布状态';
COMMENT ON COLUMN public.matchmaking_activities.published_at IS '首次发布时刻';
COMMENT ON COLUMN public.matchmaking_activities.tasks_published_at IS '首次任务发布时刻，关系清理不解冻题库';
COMMENT ON COLUMN public.matchmaking_activities.campuses IS '参与校区代码';
COMMENT ON COLUMN public.matchmaking_activities.registration_opens_at IS '报名开始';
COMMENT ON COLUMN public.matchmaking_activities.registration_closes_at IS '报名截止';
COMMENT ON COLUMN public.matchmaking_activities.results_at IS '最早结果发布时间';
COMMENT ON COLUMN public.matchmaking_activities.starts_at IS '每日任务起始时刻';
COMMENT ON COLUMN public.matchmaking_activities.ends_at IS '活动结束';
COMMENT ON COLUMN public.matchmaking_activities.days IS '任务天数';
COMMENT ON COLUMN public.matchmaking_activities.created_at IS '创建时间';
ALTER TABLE public.matchmaking_activities ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_activities FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_activities USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_activities FROM PUBLIC;
CREATE TABLE public.matchmaking_enrollments (
    id uuid PRIMARY KEY,
    activity_id uuid NOT NULL REFERENCES public.matchmaking_activities(id),
    account_id uuid NOT NULL REFERENCES public.accounts(id),
    version bigint NOT NULL DEFAULT 1,
    profile_version bigint NOT NULL DEFAULT 1,
    profile_cipher jsonb NOT NULL,
    status text NOT NULL CHECK (status IN ('pending','approved','rejected','withdrawn')),
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    UNIQUE(activity_id,account_id),
    UNIQUE(id,activity_id,account_id)
);
COMMENT ON TABLE public.matchmaking_enrollments IS '活动报名与审核';
COMMENT ON COLUMN public.matchmaking_enrollments.id IS '报名随机标识';
COMMENT ON COLUMN public.matchmaking_enrollments.activity_id IS '所属活动';
COMMENT ON COLUMN public.matchmaking_enrollments.account_id IS '报名账号';
COMMENT ON COLUMN public.matchmaking_enrollments.version IS '报名乐观版本';
COMMENT ON COLUMN public.matchmaking_enrollments.profile_version IS '密文资料版本';
COMMENT ON COLUMN public.matchmaking_enrollments.profile_cipher IS '本人资料和联系方式密文';
COMMENT ON COLUMN public.matchmaking_enrollments.status IS '活动资料审核状态';
COMMENT ON COLUMN public.matchmaking_enrollments.reason IS '审核驳回或退出原因';
COMMENT ON COLUMN public.matchmaking_enrollments.created_at IS '报名时间';
ALTER TABLE public.matchmaking_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_enrollments FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_enrollments USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_enrollments FROM PUBLIC;
CREATE TABLE public.matchmaking_pairs (
    id uuid PRIMARY KEY,
    activity_id uuid NOT NULL REFERENCES public.matchmaking_activities(id),
    version bigint NOT NULL DEFAULT 1,
    status text NOT NULL CHECK (status IN ('draft','published','ended')),
    score integer NOT NULL CHECK (score BETWEEN 0 AND 100),
    score_details jsonb NOT NULL,
    algorithm text NOT NULL,
    published_at timestamptz,
    ended_at timestamptz,
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    UNIQUE(id,activity_id)
);
COMMENT ON TABLE public.matchmaking_pairs IS '独立活动配对';
COMMENT ON COLUMN public.matchmaking_pairs.id IS '配对随机标识';
COMMENT ON COLUMN public.matchmaking_pairs.activity_id IS '所属活动';
COMMENT ON COLUMN public.matchmaking_pairs.version IS '配对乐观版本';
COMMENT ON COLUMN public.matchmaking_pairs.status IS '配对状态';
COMMENT ON COLUMN public.matchmaking_pairs.score IS '冻结推荐分数';
COMMENT ON COLUMN public.matchmaking_pairs.score_details IS '分项得分';
COMMENT ON COLUMN public.matchmaking_pairs.algorithm IS '评分规则版本';
COMMENT ON COLUMN public.matchmaking_pairs.published_at IS '发布时刻';
COMMENT ON COLUMN public.matchmaking_pairs.ended_at IS '解除时刻';
COMMENT ON COLUMN public.matchmaking_pairs.reason IS '解除原因';
COMMENT ON COLUMN public.matchmaking_pairs.created_at IS '创建时间';
ALTER TABLE public.matchmaking_pairs ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_pairs FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_pairs USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_pairs FROM PUBLIC;
CREATE TABLE public.matchmaking_members (
    pair_id uuid NOT NULL REFERENCES public.matchmaking_pairs(id) ON DELETE CASCADE,
    enrollment_id uuid NOT NULL REFERENCES public.matchmaking_enrollments(id),
    activity_id uuid NOT NULL REFERENCES public.matchmaking_activities(id),
    account_id uuid NOT NULL REFERENCES public.accounts(id),
    profile_version bigint NOT NULL,
    active boolean NOT NULL,
    PRIMARY KEY(pair_id,enrollment_id),
    FOREIGN KEY(pair_id,activity_id) REFERENCES public.matchmaking_pairs(id,activity_id) ON DELETE CASCADE,
    FOREIGN KEY(enrollment_id,activity_id,account_id) REFERENCES public.matchmaking_enrollments(id,activity_id,account_id)
);
COMMENT ON TABLE public.matchmaking_members IS '活动配对成员及唯一占用';
COMMENT ON COLUMN public.matchmaking_members.pair_id IS '所属配对';
COMMENT ON COLUMN public.matchmaking_members.enrollment_id IS '报名引用';
COMMENT ON COLUMN public.matchmaking_members.activity_id IS '所属活动';
COMMENT ON COLUMN public.matchmaking_members.account_id IS '成员账号';
COMMENT ON COLUMN public.matchmaking_members.profile_version IS '配对时冻结的资料版本';
COMMENT ON COLUMN public.matchmaking_members.active IS '是否占用本活动配对名额';
ALTER TABLE public.matchmaking_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_members FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_members USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_members FROM PUBLIC;
CREATE TABLE public.matchmaking_templates (
    id uuid PRIMARY KEY,
    activity_id uuid NOT NULL REFERENCES public.matchmaking_activities(id),
    kind text NOT NULL CHECK (kind IN ('online','offline')),
    body text NOT NULL,
    fixed_day integer NOT NULL CHECK (fixed_day BETWEEN 0 AND 30)
);
COMMENT ON TABLE public.matchmaking_templates IS '每日任务模板';
COMMENT ON COLUMN public.matchmaking_templates.id IS '模板随机标识';
COMMENT ON COLUMN public.matchmaking_templates.activity_id IS '所属活动';
COMMENT ON COLUMN public.matchmaking_templates.kind IS '线上或线下类型';
COMMENT ON COLUMN public.matchmaking_templates.body IS '任务正文';
COMMENT ON COLUMN public.matchmaking_templates.fixed_day IS '固定第几天，零表示随机候选';
ALTER TABLE public.matchmaking_templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_templates FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_templates USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_templates FROM PUBLIC;
CREATE TABLE public.matchmaking_tasks (
    id uuid PRIMARY KEY,
    pair_id uuid NOT NULL REFERENCES public.matchmaking_pairs(id) ON DELETE CASCADE,
    day_index integer NOT NULL CHECK (day_index BETWEEN 1 AND 30),
    kind text NOT NULL CHECK (kind IN ('online','offline')),
    body text NOT NULL,
    unlocks_at timestamptz NOT NULL,
    UNIQUE(pair_id,day_index)
);
COMMENT ON TABLE public.matchmaking_tasks IS '配对任务不可变快照';
COMMENT ON COLUMN public.matchmaking_tasks.id IS '任务随机标识';
COMMENT ON COLUMN public.matchmaking_tasks.pair_id IS '所属配对';
COMMENT ON COLUMN public.matchmaking_tasks.day_index IS '从一开始的任务天数';
COMMENT ON COLUMN public.matchmaking_tasks.kind IS '任务类型快照';
COMMENT ON COLUMN public.matchmaking_tasks.body IS '任务正文快照';
COMMENT ON COLUMN public.matchmaking_tasks.unlocks_at IS '完整解锁时间';
ALTER TABLE public.matchmaking_tasks ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_tasks FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_tasks USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_tasks FROM PUBLIC;
CREATE TABLE public.matchmaking_opens (
    task_id uuid NOT NULL REFERENCES public.matchmaking_tasks(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES public.accounts(id),
    opened_at timestamptz NOT NULL,
    PRIMARY KEY(task_id,account_id)
);
COMMENT ON TABLE public.matchmaking_opens IS '成员独立任务查看记录';
COMMENT ON COLUMN public.matchmaking_opens.task_id IS '所属任务';
COMMENT ON COLUMN public.matchmaking_opens.account_id IS '查看账号';
COMMENT ON COLUMN public.matchmaking_opens.opened_at IS '本人首次查看时间';
ALTER TABLE public.matchmaking_opens ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_opens FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_opens USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_opens FROM PUBLIC;
CREATE TABLE public.matchmaking_audits (
    id uuid PRIMARY KEY,
    activity_id uuid NOT NULL REFERENCES public.matchmaking_activities(id),
    actor_id uuid NOT NULL,
    subject_ref uuid NOT NULL,
    action text NOT NULL,
    reason text NOT NULL DEFAULT '',
    at timestamptz NOT NULL
);
COMMENT ON TABLE public.matchmaking_audits IS '活动运营操作审计';
COMMENT ON COLUMN public.matchmaking_audits.id IS '审计随机标识';
COMMENT ON COLUMN public.matchmaking_audits.activity_id IS '所属活动';
COMMENT ON COLUMN public.matchmaking_audits.actor_id IS '操作主体技术引用';
COMMENT ON COLUMN public.matchmaking_audits.subject_ref IS '操作对象技术引用，注销时清理';
COMMENT ON COLUMN public.matchmaking_audits.action IS '操作代码';
COMMENT ON COLUMN public.matchmaking_audits.reason IS '必要操作原因';
COMMENT ON COLUMN public.matchmaking_audits.at IS '操作时间';
ALTER TABLE public.matchmaking_audits ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.matchmaking_audits FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.matchmaking_audits USING (true) WITH CHECK (true);
REVOKE ALL ON public.matchmaking_audits FROM PUBLIC;
CREATE UNIQUE INDEX matchmaking_active_member ON public.matchmaking_members(activity_id,account_id) WHERE active;
CREATE UNIQUE INDEX matchmaking_fixed_day ON public.matchmaking_templates(activity_id,fixed_day) WHERE fixed_day>0;
CREATE INDEX matchmaking_enrollment_activity ON public.matchmaking_enrollments(activity_id,created_at,id);
CREATE INDEX matchmaking_pair_activity ON public.matchmaking_pairs(activity_id,status);
