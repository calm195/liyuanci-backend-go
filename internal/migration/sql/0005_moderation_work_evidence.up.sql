CREATE TABLE public.wordlist_versions (
    id uuid PRIMARY KEY,
    type text NOT NULL CHECK (length(btrim(type)) > 0),
    version bigint NOT NULL CHECK (version > 0),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'retired')),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (type, version)
);
COMMENT ON TABLE public.wordlist_versions IS '词库草案和发布版本，不预置模拟运营规则';
COMMENT ON COLUMN public.wordlist_versions.id IS '随机对象标识';
COMMENT ON COLUMN public.wordlist_versions.type IS '词库用途类型，实际业务资料待交付';
COMMENT ON COLUMN public.wordlist_versions.version IS '同类型词库版本';
COMMENT ON COLUMN public.wordlist_versions.status IS '词库生命周期';
COMMENT ON COLUMN public.wordlist_versions.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE public.wordlist_entries (
    id uuid PRIMARY KEY,
    version_id uuid NOT NULL REFERENCES public.wordlist_versions(id),
    entry text NOT NULL CHECK (length(btrim(entry)) > 0),
    action text NOT NULL CHECK (length(btrim(action)) > 0),
    UNIQUE (version_id, entry)
);
COMMENT ON TABLE public.wordlist_entries IS '指定词库版本的规则条目，不复制用户正文';
COMMENT ON COLUMN public.wordlist_entries.id IS '随机对象标识';
COMMENT ON COLUMN public.wordlist_entries.version_id IS '所属词库版本';
COMMENT ON COLUMN public.wordlist_entries.entry IS '规则词条，非用户输入';
COMMENT ON COLUMN public.wordlist_entries.action IS '命中后的稳定动作码，运营规则交付后验证';

ALTER TABLE public.wish_versions ADD CONSTRAINT wish_versions_wordlist_fk FOREIGN KEY (wordlist_version) REFERENCES public.wordlist_versions(id);
CREATE TABLE public.moderation_tasks (
    id uuid PRIMARY KEY,
    content_id uuid NOT NULL,
    content_version bigint NOT NULL CHECK (content_version > 0),
    subject_epoch bigint NOT NULL CHECK (subject_epoch > 0),
    hold_epoch bigint NOT NULL DEFAULT 0 CHECK (hold_epoch >= 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'machine_passed', 'human_required', 'approved', 'rejected', 'obsolete', 'cancelled')),
    decision text CHECK (decision IN ('approved','rejected')),
    reason text,
    revision_suggestion text,
    reviewer_id uuid REFERENCES public.admin_accounts(id),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (content_id, content_version) REFERENCES public.content_registry(id, content_version),
    CHECK (decision IS DISTINCT FROM 'rejected' OR (reason IS NOT NULL AND length(btrim(reason)) > 0 AND revision_suggestion IS NOT NULL AND length(btrim(revision_suggestion)) > 0)),
    CHECK (status <> 'rejected' OR (decision IS NOT NULL AND decision = 'rejected'))
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

CREATE INDEX moderation_tasks_pending_idx ON public.moderation_tasks(status, created_at, id);
CREATE TABLE public.reports (
    id uuid PRIMARY KEY,
    reporter_id uuid NOT NULL REFERENCES public.accounts(id),
    target_kind text NOT NULL CHECK (target_kind IN ('wish', 'application', 'message', 'story', 'account')),
    target_id uuid NOT NULL,
    type_code text NOT NULL CHECK (length(btrim(type_code)) > 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'resolved', 'rejected')),
    handler_id uuid REFERENCES public.admin_accounts(id),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
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

CREATE TABLE public.content_restrictions (
    id uuid PRIMARY KEY,
    content_id uuid NOT NULL REFERENCES public.content_registry(id),
    kind text NOT NULL CHECK (kind IN ('violation', 'crisis', 'penalty')),
    source_ref uuid NOT NULL,
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    UNIQUE (content_id, kind, source_ref)
);
COMMENT ON TABLE public.content_restrictions IS '独立内容限制原因，解除一种原因不解除其他限制';
COMMENT ON COLUMN public.content_restrictions.id IS '随机对象标识';
COMMENT ON COLUMN public.content_restrictions.content_id IS '受限制的精确内容注册';
COMMENT ON COLUMN public.content_restrictions.kind IS '限制来源类型';
COMMENT ON COLUMN public.content_restrictions.source_ref IS '限制来源标识，不复制正文';
COMMENT ON COLUMN public.content_restrictions.active IS '该限制原因是否仍有效';
COMMENT ON COLUMN public.content_restrictions.version IS '聚合版本，用于期望版本校验';

CREATE INDEX content_restrictions_active_idx ON public.content_restrictions(content_id) WHERE active;
CREATE TABLE public.privacy_holds (
    id uuid PRIMARY KEY,
    target_kind text NOT NULL CHECK (target_kind IN ('wish', 'story')),
    target_id uuid NOT NULL,
    hold_epoch bigint NOT NULL CHECK (hold_epoch > 0),
    required_content_version bigint NOT NULL CHECK (required_content_version > 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'released')),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    released_at timestamptz,
    UNIQUE (target_kind, target_id, hold_epoch),
    CHECK ((status = 'active' AND released_at IS NULL) OR (status = 'released' AND released_at IS NOT NULL))
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

CREATE UNIQUE INDEX privacy_holds_active_unique ON public.privacy_holds(target_kind, target_id) WHERE status = 'active';
CREATE TABLE public.work_items (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (length(btrim(kind)) > 0),
    target_kind text NOT NULL CHECK (length(btrim(target_kind)) > 0),
    target_id uuid NOT NULL,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    operation_id uuid NOT NULL UNIQUE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'retry_wait', 'completed', 'cancelled', 'attention')),
    lease_until timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status <> 'leased' OR lease_until IS NOT NULL)
);
COMMENT ON TABLE public.work_items IS '持久工作源，不携带身份明文或内容正文';
COMMENT ON COLUMN public.work_items.id IS '随机对象标识';
COMMENT ON COLUMN public.work_items.kind IS '受控工作类型';
COMMENT ON COLUMN public.work_items.target_kind IS '技术目标类型';
COMMENT ON COLUMN public.work_items.target_id IS '技术目标随机标识';
COMMENT ON COLUMN public.work_items.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.work_items.operation_id IS '跨重试稳定操作标识';
COMMENT ON COLUMN public.work_items.status IS '工作状态，异常待办不能绕过取消';
COMMENT ON COLUMN public.work_items.lease_until IS '当前处理租约期限';
COMMENT ON COLUMN public.work_items.attempts IS '已尝试次数';
COMMENT ON COLUMN public.work_items.next_at IS '最早可再次执行时间';
COMMENT ON COLUMN public.work_items.created_at IS '创建时间，以带时区时间保存';

CREATE INDEX work_items_due_idx ON public.work_items(status, next_at, id);
CREATE TABLE public.outbox_events (
    event_id uuid PRIMARY KEY,
    work_id uuid NOT NULL REFERENCES public.work_items(id),
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL CHECK (aggregate_version > 0),
    event_type text NOT NULL CHECK (length(btrim(event_type)) > 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_until timestamptz,
    sent_at timestamptz,
    next_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0)
);
COMMENT ON TABLE public.outbox_events IS '同业务事务写入的最小唤醒事件，不保存正文或身份载荷';
COMMENT ON COLUMN public.outbox_events.event_id IS '随机事件标识';
COMMENT ON COLUMN public.outbox_events.work_id IS '持久工作源，消费不得从事件重建取消工作';
COMMENT ON COLUMN public.outbox_events.aggregate_id IS '聚合随机技术标识';
COMMENT ON COLUMN public.outbox_events.aggregate_version IS '事件关联的聚合版本';
COMMENT ON COLUMN public.outbox_events.event_type IS '稳定事件类型';
COMMENT ON COLUMN public.outbox_events.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.outbox_events.lease_until IS '转发租约期限';
COMMENT ON COLUMN public.outbox_events.sent_at IS '已确认发送时间';
COMMENT ON COLUMN public.outbox_events.next_at IS '最早重试时间';
COMMENT ON COLUMN public.outbox_events.attempts IS '转发尝试次数';

CREATE INDEX outbox_events_unsent_idx ON public.outbox_events(next_at, event_id) WHERE sent_at IS NULL;
CREATE TABLE public.consumer_receipts (
    consumer_kind text NOT NULL CHECK (length(btrim(consumer_kind)) > 0),
    operation_id uuid NOT NULL,
    completed_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (consumer_kind, operation_id)
);
COMMENT ON TABLE public.consumer_receipts IS '与业务结果同事务提交的消费幂等回执，不作为永久个人轨迹';
COMMENT ON COLUMN public.consumer_receipts.consumer_kind IS '受控消费者类型';
COMMENT ON COLUMN public.consumer_receipts.operation_id IS '稳定工作操作标识';
COMMENT ON COLUMN public.consumer_receipts.completed_at IS '业务结果完成时间';

CREATE TABLE public.notifications (
    id uuid PRIMARY KEY,
    recipient_id uuid NOT NULL REFERENCES public.accounts(id),
    type text NOT NULL CHECK (length(btrim(type)) > 0),
    target_ref uuid NOT NULL,
    read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE public.notifications IS '通用站内通知，不复制业务正文或学号';
COMMENT ON COLUMN public.notifications.id IS '随机对象标识';
COMMENT ON COLUMN public.notifications.recipient_id IS '通知接收人';
COMMENT ON COLUMN public.notifications.type IS '通用通知类型';
COMMENT ON COLUMN public.notifications.target_ref IS '目标随机引用，访问时重新验证授权';
COMMENT ON COLUMN public.notifications.read_at IS '本人阅读时间';
COMMENT ON COLUMN public.notifications.created_at IS '创建时间，以带时区时间保存';

CREATE INDEX notifications_recipient_idx ON public.notifications(recipient_id, created_at DESC, id DESC);
CREATE TABLE public.delivery_attempts (
    id uuid PRIMARY KEY,
    notification_id uuid NOT NULL REFERENCES public.notifications(id),
    channel text NOT NULL CHECK (channel IN ('in_app', 'wechat')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed')),
    attempted_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE public.delivery_attempts IS '通知渠道尝试，不保存渠道凭据或正文';
COMMENT ON COLUMN public.delivery_attempts.id IS '随机对象标识';
COMMENT ON COLUMN public.delivery_attempts.notification_id IS '对应站内通知';
COMMENT ON COLUMN public.delivery_attempts.channel IS '投递渠道';
COMMENT ON COLUMN public.delivery_attempts.status IS '单次投递状态';
COMMENT ON COLUMN public.delivery_attempts.attempted_at IS '尝试时间';

CREATE TABLE public.command_receipts (
    actor_kind text NOT NULL CHECK (actor_kind IN ('student', 'admin')),
    actor_id uuid NOT NULL,
    command_kind text NOT NULL CHECK (length(btrim(command_kind)) > 0),
    key_hash bytea NOT NULL CHECK (octet_length(key_hash) = 32),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    result_ref uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (actor_kind, actor_id, command_kind, key_hash)
);
COMMENT ON TABLE public.command_receipts IS '命令幂等回执，重放仍须当前授权且不保存正文快照';
COMMENT ON COLUMN public.command_receipts.actor_kind IS '调用者命名空间';
COMMENT ON COLUMN public.command_receipts.actor_id IS '调用者技术标识，多态主体存在性由命令校验';
COMMENT ON COLUMN public.command_receipts.command_kind IS '受控路由命令类型';
COMMENT ON COLUMN public.command_receipts.key_hash IS '幂等键摘要';
COMMENT ON COLUMN public.command_receipts.request_hash IS '规范请求摘要，不保存原始请求';
COMMENT ON COLUMN public.command_receipts.result_ref IS '命令结果技术引用';
COMMENT ON COLUMN public.command_receipts.expires_at IS '回执清理期限';

CREATE INDEX command_receipts_expiry_idx ON public.command_receipts(expires_at);
CREATE TABLE public.access_audits (
    id uuid PRIMARY KEY,
    actor_id uuid NOT NULL REFERENCES public.admin_accounts(id),
    subject_ref uuid NOT NULL,
    purpose text NOT NULL CHECK (length(btrim(purpose)) > 0),
    at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    outcome text NOT NULL CHECK (outcome IN ('allowed', 'denied', 'failed'))
);
COMMENT ON TABLE public.access_audits IS '普通受控访问留迹，无姓名、正文或令牌，非涉案关联需清理';
COMMENT ON COLUMN public.access_audits.id IS '随机对象标识';
COMMENT ON COLUMN public.access_audits.actor_id IS '访问后台账号';
COMMENT ON COLUMN public.access_audits.subject_ref IS '被访问普通主体技术引用，注销清理须显式处理';
COMMENT ON COLUMN public.access_audits.purpose IS '授权访问目的';
COMMENT ON COLUMN public.access_audits.at IS '访问时间';
COMMENT ON COLUMN public.access_audits.outcome IS '访问结果';

CREATE TABLE public.deletion_jobs (
    job_id uuid PRIMARY KEY,
    subject_ref uuid REFERENCES public.accounts(id),
    phase text NOT NULL DEFAULT 'intent' CHECK (phase IN ('intent', 'blocked', 'cleaning', 'verifying', 'completed', 'attention')),
    remaining_counts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(remaining_counts) = 'object'),
    error_code text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (phase <> 'completed' OR subject_ref IS NULL)
);
COMMENT ON TABLE public.deletion_jobs IS '普通注销清理进度，完成后不得残留主体关联';
COMMENT ON COLUMN public.deletion_jobs.job_id IS '随机删除回执句柄';
COMMENT ON COLUMN public.deletion_jobs.subject_ref IS '工作期间主体关联，完成后必须清空';
COMMENT ON COLUMN public.deletion_jobs.phase IS '清理阶段';
COMMENT ON COLUMN public.deletion_jobs.remaining_counts IS '受控类别剩余数量，不存正文或身份';
COMMENT ON COLUMN public.deletion_jobs.error_code IS '安全错误码，不存底层异常原文';
COMMENT ON COLUMN public.deletion_jobs.created_at IS '创建时间，以带时区时间保存';

CREATE SCHEMA evidence;
COMMENT ON SCHEMA evidence IS '限定违规及危机证据隔离域，不与普通账号级联删除';
REVOKE ALL ON SCHEMA evidence FROM PUBLIC;
CREATE TABLE evidence.evidence_cases (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('violation', 'crisis')),
    source_ref uuid NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'deletion_pending')),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (kind, source_ref)
);
COMMENT ON TABLE evidence.evidence_cases IS '仅限定违规认定或危机事件的证据范围，不自动保留所有举报';
COMMENT ON COLUMN evidence.evidence_cases.id IS '随机对象标识';
COMMENT ON COLUMN evidence.evidence_cases.kind IS '证据范围来源';
COMMENT ON COLUMN evidence.evidence_cases.source_ref IS '来源事件技术标识，不外键绑定普通数据';
COMMENT ON COLUMN evidence.evidence_cases.status IS '证据案件状态';
COMMENT ON COLUMN evidence.evidence_cases.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE evidence.crisis_cases (
    id uuid PRIMARY KEY,
    evidence_case_id uuid NOT NULL UNIQUE REFERENCES evidence.evidence_cases(id),
    content_type text NOT NULL CHECK (content_type IN ('wish', 'application', 'message', 'story')),
    status text NOT NULL DEFAULT 'isolated' CHECK (status IN ('isolated', 'pending_handling', 'handling', 'resolved', 'false_positive')),
    detected_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolution text,
    handler_id uuid
);
COMMENT ON TABLE evidence.crisis_cases IS '危机事件流程，主体及正文仅存加密证据项';
COMMENT ON COLUMN evidence.crisis_cases.id IS '随机对象标识';
COMMENT ON COLUMN evidence.crisis_cases.evidence_case_id IS '证据范围';
COMMENT ON COLUMN evidence.crisis_cases.content_type IS '危机来源内容类型';
COMMENT ON COLUMN evidence.crisis_cases.status IS '危机处理状态';
COMMENT ON COLUMN evidence.crisis_cases.detected_at IS '检测时间';
COMMENT ON COLUMN evidence.crisis_cases.resolution IS '安全处理结论码，不复制敏感正文';
COMMENT ON COLUMN evidence.crisis_cases.handler_id IS '授权处理人技术标识，不依赖普通账号或后台外键存续';

CREATE TABLE evidence.assets (
    id uuid PRIMARY KEY,
    case_id uuid NOT NULL REFERENCES evidence.evidence_cases(id),
    encrypted_object_key jsonb NOT NULL CHECK (encrypted_object_key IS NULL OR jsonb_typeof(encrypted_object_key) = 'object'),
    object_version text NOT NULL CHECK (length(btrim(object_version)) > 0),
    wrapped_key_ref text NOT NULL CHECK (length(btrim(wrapped_key_ref)) > 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deletion_pending', 'deleted')),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (id, case_id)
);
COMMENT ON TABLE evidence.assets IS '独立证据对象元数据，不通过普通学生卡外键依赖注销者存续';
COMMENT ON COLUMN evidence.assets.id IS '随机对象标识';
COMMENT ON COLUMN evidence.assets.case_id IS '限定证据案件';
COMMENT ON COLUMN evidence.assets.encrypted_object_key IS '证据私有对象键，保存信封密文而非明文';
COMMENT ON COLUMN evidence.assets.object_version IS '证据对象版本';
COMMENT ON COLUMN evidence.assets.wrapped_key_ref IS '证据专用包裹密钥引用';
COMMENT ON COLUMN evidence.assets.status IS '证据对象状态';
COMMENT ON COLUMN evidence.assets.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE evidence.evidence_items (
    id uuid PRIMARY KEY,
    case_id uuid NOT NULL REFERENCES evidence.evidence_cases(id),
    subject_cipher jsonb NOT NULL CHECK (subject_cipher IS NULL OR jsonb_typeof(subject_cipher) = 'object'),
    body_cipher jsonb NOT NULL CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    asset_ref uuid,
    key_ref text NOT NULL CHECK (length(btrim(key_ref)) > 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (asset_ref, case_id) REFERENCES evidence.assets(id, case_id)
);
COMMENT ON TABLE evidence.evidence_items IS '限定证据主体及正文密文，不保存普通账号外键';
COMMENT ON COLUMN evidence.evidence_items.id IS '随机对象标识';
COMMENT ON COLUMN evidence.evidence_items.case_id IS '限定证据范围';
COMMENT ON COLUMN evidence.evidence_items.subject_cipher IS '限定证据主体，保存信封密文而非明文';
COMMENT ON COLUMN evidence.evidence_items.body_cipher IS '限定证据正文，保存信封密文而非明文';
COMMENT ON COLUMN evidence.evidence_items.asset_ref IS '可选证据域对象标识';
COMMENT ON COLUMN evidence.evidence_items.key_ref IS '证据外部密钥引用，不存密钥材料';
COMMENT ON COLUMN evidence.evidence_items.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE evidence.evidence_actions (
    id uuid PRIMARY KEY,
    case_id uuid NOT NULL REFERENCES evidence.evidence_cases(id),
    actor uuid NOT NULL,
    action text NOT NULL CHECK (length(btrim(action)) > 0),
    at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE evidence.evidence_actions IS '限定涉案必要处置留迹，不承接所有普通浏览日志';
COMMENT ON COLUMN evidence.evidence_actions.id IS '随机对象标识';
COMMENT ON COLUMN evidence.evidence_actions.case_id IS '限定案件';
COMMENT ON COLUMN evidence.evidence_actions.actor IS '后台处理者技术标识，不依赖普通账号存续';
COMMENT ON COLUMN evidence.evidence_actions.action IS '必要处置动作码，不存身份正文';
COMMENT ON COLUMN evidence.evidence_actions.at IS '处置时间';
