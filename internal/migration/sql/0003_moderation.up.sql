-- 内容审核、安全、隐私和关怀；版本 SQL 是结构定义的唯一来源。

CREATE TABLE public.access_audits (
    id uuid NOT NULL,
    actor_id uuid NOT NULL,
    subject_ref uuid NOT NULL,
    purpose text NOT NULL,
    at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    outcome text NOT NULL,
    CONSTRAINT access_audits_outcome_check CHECK ((outcome = ANY (ARRAY['allowed'::text, 'denied'::text, 'failed'::text]))),
    CONSTRAINT access_audits_purpose_check CHECK ((length(btrim(purpose)) > 0)),
    CONSTRAINT access_audits_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.access_audits IS '普通受控访问留迹，无姓名、正文或令牌，非涉案关联需清理';
COMMENT ON COLUMN public.access_audits.id IS '随机对象标识';
COMMENT ON COLUMN public.access_audits.actor_id IS '访问后台账号';
COMMENT ON COLUMN public.access_audits.subject_ref IS '被访问普通主体技术引用，注销清理须显式处理';
COMMENT ON COLUMN public.access_audits.purpose IS '授权访问目的';
COMMENT ON COLUMN public.access_audits.at IS '访问时间';
COMMENT ON COLUMN public.access_audits.outcome IS '访问结果';

CREATE TABLE public.appeals (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    penalty_id uuid NOT NULL,
    body_cipher jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    handler_id uuid,
    decision_reason text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT appeals_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT appeals_check CHECK (((status <> 'accepted'::text) OR ((decision_reason IS NOT NULL) AND (length(btrim(decision_reason)) > 0)))),
    CONSTRAINT appeals_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'accepted'::text, 'rejected'::text]))),
    CONSTRAINT appeals_version_check CHECK ((version > 0)),
    CONSTRAINT appeals_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.appeals IS '受限账号可提交的处罚申诉，成功解罚必须有理由';
COMMENT ON COLUMN public.appeals.id IS '随机对象标识';
COMMENT ON COLUMN public.appeals.account_id IS '申诉账号';
COMMENT ON COLUMN public.appeals.penalty_id IS '申诉目标处置，主体一致性由命令复核';
COMMENT ON COLUMN public.appeals.body_cipher IS '申诉正文，保存信封密文而非明文';
COMMENT ON COLUMN public.appeals.status IS '申诉处理状态';
COMMENT ON COLUMN public.appeals.handler_id IS '处理人';
COMMENT ON COLUMN public.appeals.decision_reason IS '处置结论理由';
COMMENT ON COLUMN public.appeals.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.appeals.version IS '申诉案件独立乐观版本';

CREATE TABLE public.care_cards (
    id uuid NOT NULL,
    recipient_id uuid NOT NULL,
    resources jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT care_cards_resources_check CHECK ((jsonb_typeof(resources) = 'object'::text)),
    CONSTRAINT care_cards_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.care_cards IS '本人关怀资料投影，注销清理，不携带危机案件正文';
COMMENT ON COLUMN public.care_cards.id IS '关怀通知目标随机标识';
COMMENT ON COLUMN public.care_cards.recipient_id IS '仅本人可读取的普通账号关联';
COMMENT ON COLUMN public.care_cards.resources IS '已显式配置的版本化援助资料，不含危机身份正文';
COMMENT ON COLUMN public.care_cards.created_at IS '关怀创建时间';

CREATE TABLE public.content_registry (
    id uuid NOT NULL,
    kind text NOT NULL,
    entity_id uuid NOT NULL,
    content_version bigint NOT NULL,
    author_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    school_related boolean,
    school_detection_version text NOT NULL,
    school_detection_state text DEFAULT 'pending'::text NOT NULL,
    CONSTRAINT content_registry_check CHECK ((((school_detection_state = 'pending'::text) AND (school_related IS NULL)) OR ((school_detection_state = 'completed'::text) AND (school_related IS NOT NULL)))),
    CONSTRAINT content_registry_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT content_registry_kind_check CHECK ((kind = ANY (ARRAY['wish'::text, 'application'::text, 'message'::text, 'story'::text, 'whisper_post'::text, 'whisper_comment'::text, 'whisper_nickname'::text, 'whisper_capsule'::text]))),
    CONSTRAINT content_registry_school_detection_state_check CHECK ((school_detection_state = ANY (ARRAY['pending'::text, 'completed'::text]))),
    CONSTRAINT content_registry_school_detection_version_check CHECK ((length(btrim(school_detection_version)) > 0)),
    CONSTRAINT content_registry_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'machine_passed'::text, 'human_required'::text, 'approved'::text, 'rejected'::text, 'obsolete'::text, 'cancelled'::text]))),
    CONSTRAINT content_registry_id_content_version_key UNIQUE (id, content_version),
    CONSTRAINT content_registry_kind_entity_id_content_version_key UNIQUE (kind, entity_id, content_version),
    CONSTRAINT content_registry_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.content_registry IS '普通内容精确版本注册，目标存在性由同一事务维护';
COMMENT ON COLUMN public.content_registry.id IS '随机对象标识';
COMMENT ON COLUMN public.content_registry.kind IS '内容类型';
COMMENT ON COLUMN public.content_registry.entity_id IS '内容实体标识，不以多态标识冒充外键';
COMMENT ON COLUMN public.content_registry.content_version IS '内容版本';
COMMENT ON COLUMN public.content_registry.author_id IS '普通内容作者';
COMMENT ON COLUMN public.content_registry.status IS '内容审核状态';
COMMENT ON COLUMN public.content_registry.school_related IS '涉校判定结果，未完成时为空而非默认否';
COMMENT ON COLUMN public.content_registry.school_detection_version IS '涉校检测器版本';
COMMENT ON COLUMN public.content_registry.school_detection_state IS '涉校判定状态';

CREATE TABLE public.content_restrictions (
    id uuid NOT NULL,
    content_id uuid NOT NULL,
    kind text NOT NULL,
    source_ref uuid NOT NULL,
    active boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    reason_cipher jsonb,
    release_reason_cipher jsonb,
    CONSTRAINT content_restrictions_kind_check CHECK ((kind = ANY (ARRAY['violation'::text, 'crisis'::text, 'penalty'::text]))),
    CONSTRAINT content_restrictions_version_check CHECK ((version > 0)),
    CONSTRAINT content_restrictions_content_id_kind_source_ref_key UNIQUE (content_id, kind, source_ref),
    CONSTRAINT content_restrictions_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.content_restrictions IS '独立内容限制原因，解除一种原因不解除其他限制';
COMMENT ON COLUMN public.content_restrictions.id IS '随机对象标识';
COMMENT ON COLUMN public.content_restrictions.content_id IS '受限制的精确内容注册';
COMMENT ON COLUMN public.content_restrictions.kind IS '限制来源类型';
COMMENT ON COLUMN public.content_restrictions.source_ref IS '限制来源标识，不复制正文';
COMMENT ON COLUMN public.content_restrictions.active IS '该限制原因是否仍有效';
COMMENT ON COLUMN public.content_restrictions.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.content_restrictions.reason_cipher IS '人工下线理由信封密文，绑定本限制标识';
COMMENT ON COLUMN public.content_restrictions.release_reason_cipher IS '解除普通限制的理由信封密文，保留原下线理由';

CREATE TABLE public.deletion_jobs (
    job_id uuid NOT NULL,
    subject_ref uuid,
    phase text DEFAULT 'intent'::text NOT NULL,
    remaining_counts jsonb DEFAULT '{}'::jsonb NOT NULL,
    error_code text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    scope text DEFAULT 'account'::text NOT NULL,
    target_ref uuid,
    expires_at timestamp with time zone DEFAULT (CURRENT_TIMESTAMP + '7 days'::interval) NOT NULL,
    next_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    source_module text DEFAULT 'legacy'::text NOT NULL,
    CONSTRAINT deletion_jobs_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT deletion_jobs_check CHECK (((phase <> 'completed'::text) OR (subject_ref IS NULL))),
    CONSTRAINT deletion_jobs_phase_check CHECK ((phase = ANY (ARRAY['intent'::text, 'blocked'::text, 'cleaning'::text, 'verifying'::text, 'completed'::text, 'attention'::text]))),
    CONSTRAINT deletion_jobs_remaining_counts_check CHECK ((jsonb_typeof(remaining_counts) = 'object'::text)),
    CONSTRAINT deletion_jobs_scope_check CHECK ((scope = ANY (ARRAY['account'::text, 'evidence'::text]))),
    CONSTRAINT deletion_jobs_source_module_check CHECK ((source_module = ANY (ARRAY['legacy'::text, 'whispers'::text]))),
    CONSTRAINT deletion_jobs_pkey PRIMARY KEY (job_id)
);
COMMENT ON TABLE public.deletion_jobs IS '普通注销清理进度，完成后不得残留主体关联';
COMMENT ON COLUMN public.deletion_jobs.job_id IS '随机删除回执句柄';
COMMENT ON COLUMN public.deletion_jobs.subject_ref IS '工作期间主体关联，完成后必须清空';
COMMENT ON COLUMN public.deletion_jobs.phase IS '清理阶段';
COMMENT ON COLUMN public.deletion_jobs.remaining_counts IS '受控类别剩余数量，不存正文或身份';
COMMENT ON COLUMN public.deletion_jobs.error_code IS '安全错误码，不存底层异常原文';
COMMENT ON COLUMN public.deletion_jobs.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.deletion_jobs.scope IS '删除范围，普通账号或独立证据';
COMMENT ON COLUMN public.deletion_jobs.target_ref IS '清理期间技术目标，完成后清空，不绑定普通身份外键';
COMMENT ON COLUMN public.deletion_jobs.expires_at IS '匿名回执绝对失效时间，清理工作不因回执过期停止';
COMMENT ON COLUMN public.deletion_jobs.next_at IS '下一次清理时间，失败继续持久重试';
COMMENT ON COLUMN public.deletion_jobs.attempts IS '清理尝试次数，不保存底层错误';
COMMENT ON COLUMN public.deletion_jobs.source_module IS '删除回执投影命名空间，普通和树洞后台不得跨域读取';

CREATE TABLE public.moderation_scans (
    task_id uuid NOT NULL,
    crisis_version uuid NOT NULL,
    school_version uuid NOT NULL,
    ordinary_version uuid NOT NULL,
    detector_version text NOT NULL,
    school_related boolean NOT NULL,
    ordinary_hit boolean NOT NULL,
    crisis_hit boolean NOT NULL,
    projection_cipher jsonb NOT NULL,
    CONSTRAINT moderation_scans_detector_version_check CHECK ((length(btrim(detector_version)) > 0)),
    CONSTRAINT moderation_scans_pkey PRIMARY KEY (task_id)
);
COMMENT ON TABLE public.moderation_scans IS '精确案件扫描快照，三类规则版本与加密脱敏候选不可变';
COMMENT ON COLUMN public.moderation_scans.task_id IS '本轮精确审核案件引用';
COMMENT ON COLUMN public.moderation_scans.crisis_version IS '本次使用的危机规则快照';
COMMENT ON COLUMN public.moderation_scans.school_version IS '本次使用的涉校规则快照';
COMMENT ON COLUMN public.moderation_scans.ordinary_version IS '本次使用的普通违规规则快照';
COMMENT ON COLUMN public.moderation_scans.detector_version IS '文本安全与组合信息处理器版本';
COMMENT ON COLUMN public.moderation_scans.school_related IS '当前版本涉校判定结果';
COMMENT ON COLUMN public.moderation_scans.ordinary_hit IS '普通规则或文本安全处理器要求人工复核';
COMMENT ON COLUMN public.moderation_scans.crisis_hit IS '危机命中，只能在危机专用范围处理';
COMMENT ON COLUMN public.moderation_scans.projection_cipher IS '待放行脱敏正文与标签的加密候选，不向列表公开';

CREATE TABLE public.moderation_tasks (
    id uuid NOT NULL,
    content_id uuid NOT NULL,
    content_version bigint NOT NULL,
    subject_epoch bigint NOT NULL,
    hold_epoch bigint DEFAULT 0 NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    decision text,
    reason text,
    revision_suggestion text,
    reviewer_id uuid,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    restriction_epoch bigint,
    version bigint DEFAULT 1 NOT NULL,
    crisis_recheck boolean DEFAULT false NOT NULL,
    privacy_recheck boolean DEFAULT false NOT NULL,
    CONSTRAINT moderation_tasks_check CHECK (((decision IS DISTINCT FROM 'rejected'::text) OR ((reason IS NOT NULL) AND (length(btrim(reason)) > 0) AND (revision_suggestion IS NOT NULL) AND (length(btrim(revision_suggestion)) > 0)))),
    CONSTRAINT moderation_tasks_check1 CHECK (((status <> 'rejected'::text) OR ((decision IS NOT NULL) AND (decision = 'rejected'::text)))),
    CONSTRAINT moderation_tasks_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT moderation_tasks_decision_check CHECK ((decision = ANY (ARRAY['approved'::text, 'rejected'::text]))),
    CONSTRAINT moderation_tasks_hold_epoch_check CHECK ((hold_epoch >= 0)),
    CONSTRAINT moderation_tasks_restriction_epoch_check CHECK (((restriction_epoch IS NULL) OR (restriction_epoch >= 0))),
    CONSTRAINT moderation_tasks_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'machine_passed'::text, 'human_required'::text, 'approved'::text, 'rejected'::text, 'obsolete'::text, 'cancelled'::text]))),
    CONSTRAINT moderation_tasks_subject_epoch_check CHECK ((subject_epoch > 0)),
    CONSTRAINT moderation_tasks_version_check CHECK ((version > 0)),
    CONSTRAINT moderation_tasks_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.moderation_tasks IS '绑定精确内容版本的审核任务，通过后仍须业务许可复核';
COMMENT ON COLUMN public.moderation_tasks.id IS '随机对象标识';
COMMENT ON COLUMN public.moderation_tasks.content_id IS '注册内容标识';
COMMENT ON COLUMN public.moderation_tasks.content_version IS '目标内容版本';
COMMENT ON COLUMN public.moderation_tasks.subject_epoch IS '提交时主体权限代次';
COMMENT ON COLUMN public.moderation_tasks.hold_epoch IS '提交时隐私屏障代次';
COMMENT ON COLUMN public.moderation_tasks.status IS '审核任务状态';
COMMENT ON COLUMN public.moderation_tasks.decision IS '审核结论，不等于公开或送达结果';
COMMENT ON COLUMN public.moderation_tasks.reason IS '审核驳回原因';
COMMENT ON COLUMN public.moderation_tasks.revision_suggestion IS '独立的修改建议';
COMMENT ON COLUMN public.moderation_tasks.reviewer_id IS '人工审核账号';
COMMENT ON COLUMN public.moderation_tasks.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.moderation_tasks.restriction_epoch IS '提交时内容限制代次，树洞允许初始零；历史空值不得作为自动公开依据';
COMMENT ON COLUMN public.moderation_tasks.version IS '审核案件独立乐观版本，不是内容版本或心愿版本';
COMMENT ON COLUMN public.moderation_tasks.crisis_recheck IS '危机误报后新建普通人工复核标志，不创建新的交互或送达序号';
COMMENT ON COLUMN public.moderation_tasks.privacy_recheck IS '注销脱敏新稿强制人工复核，不沿用旧批准';

CREATE TABLE public.penalties (
    id uuid NOT NULL,
    account_id uuid NOT NULL,
    level text NOT NULL,
    action_kind text NOT NULL,
    violation_ref uuid NOT NULL,
    content_version bigint NOT NULL,
    starts_at timestamp with time zone NOT NULL,
    ends_at timestamp with time zone,
    reason_ref uuid NOT NULL,
    first_displayed_at timestamp with time zone,
    confirmable_at timestamp with time zone,
    acknowledged_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    revoked_at timestamp with time zone,
    content_id uuid,
    reason_cipher jsonb,
    source_kind text DEFAULT 'case'::text NOT NULL,
    revoke_reason_cipher jsonb,
    revoked_by uuid,
    CONSTRAINT direct_penalty_scope CHECK (((source_kind <> 'direct'::text) OR ((content_id IS NULL) AND (content_version = 1) AND (action_kind = ANY (ARRAY['mute'::text, 'ban'::text]))))),
    CONSTRAINT penalties_action_kind_check CHECK ((action_kind = ANY (ARRAY['warning'::text, 'mute'::text, 'ban'::text]))),
    CONSTRAINT penalties_check CHECK ((((level = 'minor'::text) AND (action_kind = 'warning'::text) AND (ends_at IS NULL)) OR ((level = 'general'::text) AND (action_kind = 'mute'::text) AND (ends_at IS NOT NULL) AND (ends_at = (starts_at + '168:00:00'::interval))) OR ((level = 'severe'::text) AND (action_kind = 'ban'::text) AND (ends_at IS NULL)))),
    CONSTRAINT penalties_check1 CHECK ((((first_displayed_at IS NULL) AND (confirmable_at IS NULL) AND (acknowledged_at IS NULL)) OR ((action_kind = 'warning'::text) AND (first_displayed_at IS NOT NULL) AND (confirmable_at IS NOT NULL) AND (confirmable_at = (first_displayed_at + '00:00:30'::interval)) AND ((acknowledged_at IS NULL) OR (acknowledged_at >= confirmable_at))))),
    CONSTRAINT penalties_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT penalties_level_check CHECK ((level = ANY (ARRAY['minor'::text, 'general'::text, 'severe'::text]))),
    CONSTRAINT penalties_source_kind_check CHECK ((source_kind = ANY (ARRAY['case'::text, 'direct'::text]))),
    CONSTRAINT penalties_version_check CHECK ((version > 0)),
    CONSTRAINT penalties_account_id_violation_ref_action_kind_key UNIQUE (account_id, violation_ref, action_kind),
    CONSTRAINT penalties_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.penalties IS '警告、七天禁言与永久封禁，是否限制交互由动作及当前时间判定';
COMMENT ON COLUMN public.penalties.id IS '随机对象标识';
COMMENT ON COLUMN public.penalties.account_id IS '受处置账号';
COMMENT ON COLUMN public.penalties.level IS '违规等级';
COMMENT ON COLUMN public.penalties.action_kind IS '处置动作，warning 不限制交互';
COMMENT ON COLUMN public.penalties.violation_ref IS '违规事件引用，不直接依赖证据主体存续';
COMMENT ON COLUMN public.penalties.content_version IS '案件精确内容版本；direct来源固定为1且content_id为空，不代表存在内容';
COMMENT ON COLUMN public.penalties.starts_at IS '处置开始时间';
COMMENT ON COLUMN public.penalties.ends_at IS '七天禁言的结束时间，警告和永久封禁为空';
COMMENT ON COLUMN public.penalties.reason_ref IS '处置原因技术引用，不复制正文';
COMMENT ON COLUMN public.penalties.first_displayed_at IS '首次警告展示时间，后续重进不得重置';
COMMENT ON COLUMN public.penalties.confirmable_at IS '首次展示三十秒后的可确认时间';
COMMENT ON COLUMN public.penalties.acknowledged_at IS '本人确认警告时间，不得提前';
COMMENT ON COLUMN public.penalties.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.penalties.revoked_at IS '经授权撤销时间';
COMMENT ON COLUMN public.penalties.content_id IS '触发处罚的精确内容注册，历史处罚可为空';
COMMENT ON COLUMN public.penalties.reason_cipher IS '授权处置理由的信封密文';
COMMENT ON COLUMN public.penalties.source_kind IS '处罚来源：case案件或direct管理员直接处置；直接处置不关联内容';
COMMENT ON COLUMN public.penalties.revoke_reason_cipher IS '管理员直接解除理由信封密文，绑定处罚标识，随处罚隐私清理';
COMMENT ON COLUMN public.penalties.revoked_by IS '直接解除的后台操作者技术标识，不存身份正文';

CREATE TABLE public.privacy_holds (
    id uuid NOT NULL,
    target_kind text NOT NULL,
    target_id uuid NOT NULL,
    hold_epoch bigint NOT NULL,
    required_content_version bigint NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    released_at timestamp with time zone,
    CONSTRAINT privacy_holds_check CHECK ((((status = 'active'::text) AND (released_at IS NULL)) OR ((status = 'released'::text) AND (released_at IS NOT NULL)))),
    CONSTRAINT privacy_holds_hold_epoch_check CHECK ((hold_epoch > 0)),
    CONSTRAINT privacy_holds_required_content_version_check CHECK ((required_content_version > 0)),
    CONSTRAINT privacy_holds_status_check CHECK ((status = ANY (ARRAY['active'::text, 'released'::text]))),
    CONSTRAINT privacy_holds_target_kind_check CHECK ((target_kind = ANY (ARRAY['wish'::text, 'story'::text, 'whisper_post'::text, 'whisper_comment'::text, 'whisper_nickname'::text, 'whisper_capsule'::text]))),
    CONSTRAINT privacy_holds_pkey PRIMARY KEY (id),
    CONSTRAINT privacy_holds_target_kind_target_id_hold_epoch_key UNIQUE (target_kind, target_id, hold_epoch)
);
COMMENT ON TABLE public.privacy_holds IS '幸存内容公开屏障，不通过普通账号或匹配外键级联删除';
COMMENT ON COLUMN public.privacy_holds.id IS '随机对象标识';
COMMENT ON COLUMN public.privacy_holds.target_kind IS '受保护的幸存内容类型';
COMMENT ON COLUMN public.privacy_holds.target_id IS '幸存内容标识，不保存注销者身份';
COMMENT ON COLUMN public.privacy_holds.hold_epoch IS '屏障代次，旧审核不得解除';
COMMENT ON COLUMN public.privacy_holds.required_content_version IS '要求重新脱敏审核的内容版本';
COMMENT ON COLUMN public.privacy_holds.status IS '隐私屏障状态';
COMMENT ON COLUMN public.privacy_holds.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.privacy_holds.released_at IS '经新版本审核复核后的释放时间';

CREATE TABLE public.report_types (
    code text NOT NULL,
    label text NOT NULL,
    published boolean DEFAULT false NOT NULL,
    CONSTRAINT report_types_code_check CHECK ((length(btrim(code)) > 0)),
    CONSTRAINT report_types_label_check CHECK ((length(btrim(label)) > 0)),
    CONSTRAINT report_types_pkey PRIMARY KEY (code)
);
COMMENT ON TABLE public.report_types IS '运营显式交付并发布的举报类型，不预置合成业务规则';
COMMENT ON COLUMN public.report_types.code IS '稳定举报类型码';
COMMENT ON COLUMN public.report_types.label IS '面向举报人的类型名称';
COMMENT ON COLUMN public.report_types.published IS '是否已由运营发布供举报使用';

CREATE TABLE public.reports (
    id uuid NOT NULL,
    reporter_id uuid NOT NULL,
    target_kind text NOT NULL,
    target_id uuid NOT NULL,
    type_code text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    handler_id uuid,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    content_id uuid,
    description_cipher jsonb,
    decision_reason text,
    CONSTRAINT reports_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'resolved'::text, 'rejected'::text]))),
    CONSTRAINT reports_target_kind_check CHECK ((target_kind = ANY (ARRAY['wish'::text, 'application'::text, 'message'::text, 'story'::text, 'account'::text, 'whisper_post'::text, 'whisper_comment'::text]))),
    CONSTRAINT reports_type_code_check CHECK ((length(btrim(type_code)) > 0)),
    CONSTRAINT reports_version_check CHECK ((version > 0)),
    CONSTRAINT reports_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.reports IS '普通举报案件，提交不自动升级为永久证据';
COMMENT ON COLUMN public.reports.id IS '随机对象标识';
COMMENT ON COLUMN public.reports.reporter_id IS '举报账号，注销需清理普通关联';
COMMENT ON COLUMN public.reports.target_kind IS '被举报类型';
COMMENT ON COLUMN public.reports.target_id IS '被举报对象标识，按类型由命令复核';
COMMENT ON COLUMN public.reports.type_code IS '交付举报类型清单中的稳定码';
COMMENT ON COLUMN public.reports.status IS '举报处置状态';
COMMENT ON COLUMN public.reports.handler_id IS '处理人';
COMMENT ON COLUMN public.reports.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.reports.version IS '举报案件独立乐观版本';
COMMENT ON COLUMN public.reports.content_id IS '举报时可见的精确内容注册，不随内容更新漂移';
COMMENT ON COLUMN public.reports.description_cipher IS '可选举报说明的信封密文';
COMMENT ON COLUMN public.reports.decision_reason IS '后台处置理由，仅案件授权范围使用';

CREATE TABLE public.wordlist_entries (
    id uuid NOT NULL,
    version_id uuid NOT NULL,
    entry text NOT NULL,
    action text NOT NULL,
    CONSTRAINT wordlist_entries_action_check CHECK ((length(btrim(action)) > 0)),
    CONSTRAINT wordlist_entries_entry_check CHECK ((length(btrim(entry)) > 0)),
    CONSTRAINT wordlist_entries_pkey PRIMARY KEY (id),
    CONSTRAINT wordlist_entries_version_id_entry_key UNIQUE (version_id, entry)
);
COMMENT ON TABLE public.wordlist_entries IS '指定词库版本的规则条目，不复制用户正文';
COMMENT ON COLUMN public.wordlist_entries.id IS '随机对象标识';
COMMENT ON COLUMN public.wordlist_entries.version_id IS '所属词库版本';
COMMENT ON COLUMN public.wordlist_entries.entry IS '规则词条，非用户输入';
COMMENT ON COLUMN public.wordlist_entries.action IS '命中后的稳定动作码，运营规则交付后验证';

CREATE TABLE public.wordlist_heads (
    kind text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    effective_id uuid,
    CONSTRAINT wordlist_heads_kind_check CHECK ((kind = ANY (ARRAY['crisis'::text, 'school'::text, 'ordinary_violation'::text]))),
    CONSTRAINT wordlist_heads_version_check CHECK ((version > 0)),
    CONSTRAINT wordlist_heads_pkey PRIMARY KEY (kind)
);
COMMENT ON TABLE public.wordlist_heads IS '三类词库的并发基准与生效指针，草案创建和发布均推进代次';
COMMENT ON COLUMN public.wordlist_heads.kind IS '固定词库用途类型';
COMMENT ON COLUMN public.wordlist_heads.version IS '并发基准代次，与快照资源状态版本独立';
COMMENT ON COLUMN public.wordlist_heads.effective_id IS '当前生效不可变快照，无正式发布时为空';

CREATE TABLE public.wordlist_versions (
    id uuid NOT NULL,
    type text NOT NULL,
    version bigint NOT NULL,
    status text DEFAULT 'draft'::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    base_effective_id uuid,
    CONSTRAINT wordlist_versions_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'published'::text, 'archived'::text]))),
    CONSTRAINT wordlist_versions_type_check CHECK ((length(btrim(type)) > 0)),
    CONSTRAINT wordlist_versions_version_check CHECK ((version > 0)),
    CONSTRAINT wordlist_versions_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.wordlist_versions IS '词库草案和发布版本，不预置模拟运营规则';
COMMENT ON COLUMN public.wordlist_versions.id IS '随机对象标识';
COMMENT ON COLUMN public.wordlist_versions.type IS '词库用途类型，实际业务资料待交付';
COMMENT ON COLUMN public.wordlist_versions.version IS '快照资源状态乐观版本，不是类型基准代次';
COMMENT ON COLUMN public.wordlist_versions.status IS '词库生命周期';
COMMENT ON COLUMN public.wordlist_versions.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.wordlist_versions.base_effective_id IS '创建完整快照时的生效基准，发布时须与当前生效版本一致';

CREATE INDEX appeals_queue_idx ON public.appeals USING btree (created_at DESC, id DESC);

CREATE INDEX content_restrictions_active_idx ON public.content_restrictions USING btree (content_id) WHERE active;

CREATE INDEX deletion_jobs_pending_idx ON public.deletion_jobs USING btree (next_at, job_id) WHERE (phase <> 'completed'::text);

CREATE INDEX moderation_tasks_human_idx ON public.moderation_tasks USING btree (id) WHERE (status = 'human_required'::text);

CREATE INDEX moderation_tasks_pending_idx ON public.moderation_tasks USING btree (status, created_at, id);

CREATE INDEX penalties_account_idx ON public.penalties USING btree (account_id, action_kind, starts_at);

CREATE UNIQUE INDEX privacy_holds_active_unique ON public.privacy_holds USING btree (target_kind, target_id) WHERE (status = 'active'::text);

CREATE INDEX reports_queue_idx ON public.reports USING btree (created_at DESC, id DESC);

CREATE UNIQUE INDEX wordlist_one_published_idx ON public.wordlist_versions USING btree (type) WHERE (status = 'published'::text);

INSERT INTO public.wordlist_heads(kind) VALUES ('crisis'), ('school'), ('ordinary_violation');
