-- 独立证据域；版本 SQL 是结构定义的唯一来源。

CREATE SCHEMA evidence;

COMMENT ON SCHEMA evidence IS '限定违规及危机证据隔离域，不与普通账号级联删除';

CREATE TABLE evidence.assets (
    id uuid NOT NULL,
    case_id uuid NOT NULL,
    encrypted_object_key jsonb NOT NULL,
    object_version text NOT NULL,
    wrapped_key_ref text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT assets_encrypted_object_key_check CHECK (((encrypted_object_key IS NULL) OR (jsonb_typeof(encrypted_object_key) = 'object'::text))),
    CONSTRAINT assets_object_version_check CHECK ((length(btrim(object_version)) > 0)),
    CONSTRAINT assets_status_check CHECK ((status = ANY (ARRAY['active'::text, 'deletion_pending'::text, 'deleted'::text]))),
    CONSTRAINT assets_wrapped_key_ref_check CHECK ((length(btrim(wrapped_key_ref)) > 0)),
    CONSTRAINT assets_id_case_id_key UNIQUE (id, case_id),
    CONSTRAINT assets_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE evidence.assets IS '独立证据对象元数据，不通过普通学生卡外键依赖注销者存续';
COMMENT ON COLUMN evidence.assets.id IS '随机对象标识';
COMMENT ON COLUMN evidence.assets.case_id IS '限定证据案件';
COMMENT ON COLUMN evidence.assets.encrypted_object_key IS '证据私有对象键，保存信封密文而非明文';
COMMENT ON COLUMN evidence.assets.object_version IS '证据对象版本';
COMMENT ON COLUMN evidence.assets.wrapped_key_ref IS '证据专用包裹密钥引用';
COMMENT ON COLUMN evidence.assets.status IS '证据对象状态';
COMMENT ON COLUMN evidence.assets.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE evidence.crisis_cases (
    id uuid NOT NULL,
    evidence_case_id uuid NOT NULL,
    content_type text NOT NULL,
    status text DEFAULT 'isolated'::text NOT NULL,
    detected_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    resolution text,
    handler_id uuid,
    version bigint DEFAULT 1 NOT NULL,
    source_task_id uuid,
    handoff_at timestamp with time zone,
    identity_status text,
    source_entity_id uuid,
    source_content_version bigint,
    CONSTRAINT crisis_cases_content_type_check CHECK ((content_type = ANY (ARRAY['wish'::text, 'application'::text, 'message'::text, 'story'::text, 'whisper_post'::text, 'whisper_comment'::text, 'whisper_nickname'::text, 'whisper_capsule'::text]))),
    CONSTRAINT crisis_cases_identity_status_check CHECK ((identity_status = ANY (ARRAY['verified'::text, 'not_verified'::text]))),
    CONSTRAINT crisis_cases_status_check CHECK ((status = ANY (ARRAY['isolated'::text, 'pending_handling'::text, 'handling'::text, 'resolved'::text, 'false_positive'::text]))),
    CONSTRAINT crisis_cases_version_check CHECK ((version > 0)),
    CONSTRAINT crisis_whisper_source CHECK (((content_type <> ALL (ARRAY['whisper_post'::text, 'whisper_comment'::text, 'whisper_nickname'::text, 'whisper_capsule'::text])) OR ((source_entity_id IS NOT NULL) AND (source_content_version IS NOT NULL) AND (source_content_version > 0)))),
    CONSTRAINT crisis_cases_evidence_case_id_key UNIQUE (evidence_case_id),
    CONSTRAINT crisis_cases_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE evidence.crisis_cases IS '危机事件流程，主体及正文仅存加密证据项';
COMMENT ON COLUMN evidence.crisis_cases.id IS '随机对象标识';
COMMENT ON COLUMN evidence.crisis_cases.evidence_case_id IS '证据范围';
COMMENT ON COLUMN evidence.crisis_cases.content_type IS '危机来源内容类型';
COMMENT ON COLUMN evidence.crisis_cases.status IS '危机处理状态';
COMMENT ON COLUMN evidence.crisis_cases.detected_at IS '检测时间';
COMMENT ON COLUMN evidence.crisis_cases.resolution IS '安全处理结论码，不复制敏感正文';
COMMENT ON COLUMN evidence.crisis_cases.handler_id IS '授权处理人技术标识，不依赖普通账号或后台外键存续';
COMMENT ON COLUMN evidence.crisis_cases.version IS '危机案件独立乐观版本';
COMMENT ON COLUMN evidence.crisis_cases.source_task_id IS '检测时审核任务技术引用，不依赖普通数据存续';
COMMENT ON COLUMN evidence.crisis_cases.handoff_at IS '北京时间人工接手窗口起点，不表示首次响应承诺';
COMMENT ON COLUMN evidence.crisis_cases.identity_status IS '检测时认证身份状态，未认证不伪造学号，历史未记录保持空';
COMMENT ON COLUMN evidence.crisis_cases.source_entity_id IS '树洞案件的安全来源随机标识，普通数据删除后仍可限定投影';
COMMENT ON COLUMN evidence.crisis_cases.source_content_version IS '树洞案件精确正文版本，不随普通数据删除或换版变化';

CREATE TABLE evidence.crisis_deliveries (
    id uuid NOT NULL,
    case_id uuid NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts bigint DEFAULT 0 NOT NULL,
    next_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    lease_id uuid,
    lease_until timestamp with time zone,
    CONSTRAINT crisis_deliveries_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT crisis_deliveries_kind_check CHECK (kind = 'care'::text),
    CONSTRAINT crisis_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'leased'::text, 'sent'::text, 'cancelled'::text]))),
    CONSTRAINT crisis_deliveries_case_id_kind_key UNIQUE (case_id, kind),
    CONSTRAINT crisis_deliveries_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE evidence.crisis_deliveries IS '危机站内关怀持久重试队列，不提供外部通知';
COMMENT ON COLUMN evidence.crisis_deliveries.id IS '随机站内关怀投递标识';
COMMENT ON COLUMN evidence.crisis_deliveries.case_id IS '限定危机案件';
COMMENT ON COLUMN evidence.crisis_deliveries.kind IS '仅站内关怀用途';
COMMENT ON COLUMN evidence.crisis_deliveries.status IS '持久投递状态，失败不删除待办';
COMMENT ON COLUMN evidence.crisis_deliveries.attempts IS '实际取得租约次数';
COMMENT ON COLUMN evidence.crisis_deliveries.next_at IS '下次可尝试时间';
COMMENT ON COLUMN evidence.crisis_deliveries.lease_id IS '本次租约令牌，防止晚到确认';
COMMENT ON COLUMN evidence.crisis_deliveries.lease_until IS '租约到期后允许崩溃补偿';

CREATE TABLE evidence.evidence_actions (
    id uuid NOT NULL,
    case_id uuid NOT NULL,
    actor uuid NOT NULL,
    action text NOT NULL,
    at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    reason_cipher jsonb,
    CONSTRAINT evidence_actions_action_check CHECK ((length(btrim(action)) > 0)),
    CONSTRAINT evidence_actions_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE evidence.evidence_actions IS '限定涉案必要处置留迹，不承接所有普通浏览日志';
COMMENT ON COLUMN evidence.evidence_actions.id IS '随机对象标识';
COMMENT ON COLUMN evidence.evidence_actions.case_id IS '限定案件';
COMMENT ON COLUMN evidence.evidence_actions.actor IS '后台处理者技术标识，不依赖普通账号存续';
COMMENT ON COLUMN evidence.evidence_actions.action IS '必要处置动作码，不存身份正文';
COMMENT ON COLUMN evidence.evidence_actions.at IS '处置时间';
COMMENT ON COLUMN evidence.evidence_actions.reason_cipher IS '处置理由信封密文，绑定本动作';

CREATE TABLE evidence.evidence_cases (
    id uuid NOT NULL,
    kind text NOT NULL,
    source_ref uuid NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT evidence_cases_kind_check CHECK ((kind = ANY (ARRAY['violation'::text, 'crisis'::text]))),
    CONSTRAINT evidence_cases_status_check CHECK ((status = ANY (ARRAY['open'::text, 'closed'::text, 'deletion_pending'::text]))),
    CONSTRAINT evidence_cases_version_check CHECK ((version > 0)),
    CONSTRAINT evidence_cases_kind_source_ref_key UNIQUE (kind, source_ref),
    CONSTRAINT evidence_cases_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE evidence.evidence_cases IS '仅限定违规认定或危机事件的证据范围，不自动保留所有举报';
COMMENT ON COLUMN evidence.evidence_cases.id IS '随机对象标识';
COMMENT ON COLUMN evidence.evidence_cases.kind IS '证据范围来源';
COMMENT ON COLUMN evidence.evidence_cases.source_ref IS '来源事件技术标识，不外键绑定普通数据';
COMMENT ON COLUMN evidence.evidence_cases.status IS '证据案件状态';
COMMENT ON COLUMN evidence.evidence_cases.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN evidence.evidence_cases.version IS '证据范围版本，删除命令绑定此版本';

CREATE TABLE evidence.evidence_items (
    id uuid NOT NULL,
    case_id uuid NOT NULL,
    subject_cipher jsonb NOT NULL,
    body_cipher jsonb NOT NULL,
    asset_ref uuid,
    key_ref text NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT evidence_items_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT evidence_items_key_ref_check CHECK ((length(btrim(key_ref)) > 0)),
    CONSTRAINT evidence_items_subject_cipher_check CHECK (((subject_cipher IS NULL) OR (jsonb_typeof(subject_cipher) = 'object'::text))),
    CONSTRAINT evidence_items_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE evidence.evidence_items IS '限定证据主体及正文密文，不保存普通账号外键';
COMMENT ON COLUMN evidence.evidence_items.id IS '随机对象标识';
COMMENT ON COLUMN evidence.evidence_items.case_id IS '限定证据范围';
COMMENT ON COLUMN evidence.evidence_items.subject_cipher IS '限定证据主体，保存信封密文而非明文';
COMMENT ON COLUMN evidence.evidence_items.body_cipher IS '限定证据正文，保存信封密文而非明文';
COMMENT ON COLUMN evidence.evidence_items.asset_ref IS '可选证据域对象标识';
COMMENT ON COLUMN evidence.evidence_items.key_ref IS '证据包裹密钥引用，不存密钥材料';
COMMENT ON COLUMN evidence.evidence_items.created_at IS '创建时间，以带时区时间保存';

CREATE INDEX crisis_deliveries_pending_idx ON evidence.crisis_deliveries USING btree (next_at, id) WHERE (status = ANY (ARRAY['pending'::text, 'leased'::text]));
