-- 持久任务、事件、通知和幂等回执；版本 SQL 是结构定义的唯一来源。

CREATE TABLE public.command_receipts (
    actor_kind text NOT NULL,
    actor_id uuid NOT NULL,
    command_kind text NOT NULL,
    key_hash bytea NOT NULL,
    request_hash bytea NOT NULL,
    result_ref uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    lifecycle_bound boolean DEFAULT false NOT NULL,
    CONSTRAINT command_receipts_actor_kind_check CHECK ((actor_kind = ANY (ARRAY['student'::text, 'admin'::text]))),
    CONSTRAINT command_receipts_command_kind_check CHECK ((length(btrim(command_kind)) > 0)),
    CONSTRAINT command_receipts_key_hash_check CHECK ((octet_length(key_hash) = 32)),
    CONSTRAINT command_receipts_request_hash_check CHECK ((octet_length(request_hash) = 32)),
    CONSTRAINT command_receipts_pkey PRIMARY KEY (actor_kind, actor_id, command_kind, key_hash)
);
COMMENT ON TABLE public.command_receipts IS '命令幂等回执，重放仍须当前授权且不保存正文快照';
COMMENT ON COLUMN public.command_receipts.actor_kind IS '调用者命名空间';
COMMENT ON COLUMN public.command_receipts.actor_id IS '调用者技术标识，多态主体存在性由命令校验';
COMMENT ON COLUMN public.command_receipts.command_kind IS '受控路由命令类型';
COMMENT ON COLUMN public.command_receipts.key_hash IS '幂等键摘要';
COMMENT ON COLUMN public.command_receipts.request_hash IS '规范请求摘要，不保存原始请求';
COMMENT ON COLUMN public.command_receipts.result_ref IS '命令结果技术引用';
COMMENT ON COLUMN public.command_receipts.expires_at IS '回执清理期限';
COMMENT ON COLUMN public.command_receipts.lifecycle_bound IS '回执绑定业务幂等生命周期，技术到期后仍重验当前访问，不覆盖或按期清理';

CREATE TABLE public.consumer_receipts (
    consumer_kind text NOT NULL,
    operation_id uuid NOT NULL,
    completed_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT consumer_receipts_consumer_kind_check CHECK ((length(btrim(consumer_kind)) > 0)),
    CONSTRAINT consumer_receipts_pkey PRIMARY KEY (consumer_kind, operation_id)
);
COMMENT ON TABLE public.consumer_receipts IS '与业务结果同事务提交的消费幂等回执，不作为永久个人轨迹';
COMMENT ON COLUMN public.consumer_receipts.consumer_kind IS '受控消费者类型';
COMMENT ON COLUMN public.consumer_receipts.operation_id IS '稳定工作操作标识';
COMMENT ON COLUMN public.consumer_receipts.completed_at IS '业务结果完成时间';

CREATE TABLE public.delivery_attempts (
    id uuid NOT NULL,
    notification_id uuid NOT NULL,
    channel text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempted_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT delivery_attempts_channel_check CHECK ((channel = ANY (ARRAY['in_app'::text, 'wechat'::text]))),
    CONSTRAINT delivery_attempts_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'sent'::text, 'failed'::text]))),
    CONSTRAINT delivery_attempts_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.delivery_attempts IS '通知渠道尝试，不保存渠道凭据或正文';
COMMENT ON COLUMN public.delivery_attempts.id IS '随机对象标识';
COMMENT ON COLUMN public.delivery_attempts.notification_id IS '对应站内通知';
COMMENT ON COLUMN public.delivery_attempts.channel IS '投递渠道';
COMMENT ON COLUMN public.delivery_attempts.status IS '单次投递状态';
COMMENT ON COLUMN public.delivery_attempts.attempted_at IS '尝试时间';

CREATE TABLE public.notifications (
    id uuid NOT NULL,
    recipient_id uuid NOT NULL,
    type text NOT NULL,
    target_ref uuid NOT NULL,
    read_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT notifications_type_check CHECK ((length(btrim(type)) > 0)),
    CONSTRAINT notifications_version_check CHECK ((version > 0)),
    CONSTRAINT notifications_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.notifications IS '通用站内通知，不复制业务正文或学号';
COMMENT ON COLUMN public.notifications.id IS '随机对象标识';
COMMENT ON COLUMN public.notifications.recipient_id IS '通知接收人';
COMMENT ON COLUMN public.notifications.type IS '通用通知类型';
COMMENT ON COLUMN public.notifications.target_ref IS '目标随机引用，访问时重新验证授权';
COMMENT ON COLUMN public.notifications.read_at IS '本人阅读时间';
COMMENT ON COLUMN public.notifications.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.notifications.version IS '通知独立乐观版本，首次已读时递增';

CREATE TABLE public.outbox_events (
    event_id uuid NOT NULL,
    work_id uuid NOT NULL,
    aggregate_id uuid NOT NULL,
    aggregate_version bigint NOT NULL,
    event_type text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    lease_until timestamp with time zone,
    sent_at timestamp with time zone,
    next_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    superseded_at timestamp with time zone,
    CONSTRAINT outbox_events_aggregate_version_check CHECK ((aggregate_version > 0)),
    CONSTRAINT outbox_events_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT outbox_events_event_type_check CHECK ((length(btrim(event_type)) > 0)),
    CONSTRAINT outbox_events_pkey PRIMARY KEY (event_id)
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
COMMENT ON COLUMN public.outbox_events.superseded_at IS '被已授权重驱动替代的技术时间，非空后禁止领取、确认和消费旧唤醒';

CREATE TABLE public.work_items (
    id uuid NOT NULL,
    kind text NOT NULL,
    target_kind text NOT NULL,
    target_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    operation_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    lease_until timestamp with time zone,
    attempts integer DEFAULT 0 NOT NULL,
    next_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT work_items_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT work_items_check CHECK (((status <> 'leased'::text) OR (lease_until IS NOT NULL))),
    CONSTRAINT work_items_kind_check CHECK ((length(btrim(kind)) > 0)),
    CONSTRAINT work_items_status_check CHECK (((status = ANY (ARRAY['pending'::text, 'leased'::text, 'retry_wait'::text, 'completed'::text, 'cancelled'::text, 'attention'::text])) OR ((status = 'waiting'::text) AND (kind = 'whisper_capsule_unlock'::text)))),
    CONSTRAINT work_items_target_kind_check CHECK ((length(btrim(target_kind)) > 0)),
    CONSTRAINT work_items_version_check CHECK ((version > 0)),
    CONSTRAINT work_items_operation_id_key UNIQUE (operation_id),
    CONSTRAINT work_items_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.work_items IS '持久工作源，不携带身份明文或内容正文';
COMMENT ON COLUMN public.work_items.id IS '随机对象标识';
COMMENT ON COLUMN public.work_items.kind IS '受控工作类型';
COMMENT ON COLUMN public.work_items.target_kind IS '技术目标类型';
COMMENT ON COLUMN public.work_items.target_id IS '技术目标随机标识';
COMMENT ON COLUMN public.work_items.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.work_items.operation_id IS '跨重试稳定操作标识';
COMMENT ON COLUMN public.work_items.status IS '工作状态；胶囊解锁waiting等待安全批准事件，异常attention仍需独立处理';
COMMENT ON COLUMN public.work_items.lease_until IS '当前处理租约期限';
COMMENT ON COLUMN public.work_items.attempts IS '已尝试次数';
COMMENT ON COLUMN public.work_items.next_at IS '最早可再次执行时间';
COMMENT ON COLUMN public.work_items.created_at IS '创建时间，以带时区时间保存';

CREATE INDEX command_receipts_expiry_idx ON public.command_receipts USING btree (expires_at);

CREATE INDEX notifications_recipient_idx ON public.notifications USING btree (recipient_id, created_at DESC, id DESC);

CREATE INDEX outbox_events_unsent_idx ON public.outbox_events USING btree (next_at, event_id) WHERE (sent_at IS NULL);

CREATE INDEX outbox_events_work_active_idx ON public.outbox_events USING btree (work_id, event_type, created_at, event_id) WHERE (superseded_at IS NULL);
COMMENT ON INDEX public.outbox_events_work_active_idx IS '数据库补偿及重驱动读取当前工作唤醒关联';

CREATE UNIQUE INDEX whisper_capsule_unlock_unique ON public.work_items USING btree (target_id) WHERE (kind = 'whisper_capsule_unlock'::text);
COMMENT ON INDEX public.whisper_capsule_unlock_unique IS '每个本人胶囊只有一个持久解锁工作，取消和终结后仍保留去重标识';

CREATE INDEX work_items_due_idx ON public.work_items USING btree (status, next_at, id);
