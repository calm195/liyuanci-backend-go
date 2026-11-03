-- 心愿、应愿、匹配、聊天和分享；版本 SQL 是结构定义的唯一来源。

CREATE TABLE public.applications (
    id uuid NOT NULL,
    wish_id uuid NOT NULL,
    wish_content_version bigint NOT NULL,
    applicant_id uuid NOT NULL,
    status text DEFAULT 'pending_scan'::text NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    message_cipher jsonb NOT NULL,
    content_version bigint DEFAULT 1 NOT NULL,
    cancel_reason text,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    review_task_id uuid,
    owner_epoch bigint,
    public_message_cipher jsonb,
    CONSTRAINT applications_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT applications_message_cipher_check CHECK (((message_cipher IS NULL) OR (jsonb_typeof(message_cipher) = 'object'::text))),
    CONSTRAINT applications_owner_epoch_check CHECK ((owner_epoch > 0)),
    CONSTRAINT applications_projection_status CHECK (((public_message_cipher IS NULL) OR (status = ANY (ARRAY['active'::text, 'accepted'::text])))),
    CONSTRAINT applications_public_message_cipher_check CHECK (((public_message_cipher IS NULL) OR (jsonb_typeof(public_message_cipher) = 'object'::text))),
    CONSTRAINT applications_status_check CHECK ((status = ANY (ARRAY['pending_scan'::text, 'pending_review'::text, 'active'::text, 'accepted'::text, 'withdrawn'::text, 'rejected'::text, 'invalidated'::text]))),
    CONSTRAINT applications_version_check CHECK ((version > 0)),
    CONSTRAINT applications_wish_content_version_check CHECK ((wish_content_version > 0)),
    CONSTRAINT applications_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.applications IS '应愿申请，待审申请不向许愿人公开正文';
COMMENT ON COLUMN public.applications.id IS '随机对象标识';
COMMENT ON COLUMN public.applications.wish_id IS '应愿目标心愿';
COMMENT ON COLUMN public.applications.wish_content_version IS '申请时的目标心愿版本';
COMMENT ON COLUMN public.applications.applicant_id IS '申请人';
COMMENT ON COLUMN public.applications.status IS '应愿状态';
COMMENT ON COLUMN public.applications.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.applications.message_cipher IS '应愿留言，保存信封密文而非明文';
COMMENT ON COLUMN public.applications.content_version IS '申请内容版本';
COMMENT ON COLUMN public.applications.cancel_reason IS '终结的稳定原因码，不存身份正文';
COMMENT ON COLUMN public.applications.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.applications.review_task_id IS '当前留言审核任务，旧审核不得恢复已撤销或失效申请';
COMMENT ON COLUMN public.applications.owner_epoch IS '申请时许愿人权限代次，历史空值不能作为生效授权';
COMMENT ON COLUMN public.applications.public_message_cipher IS '审核允许的脱敏留言密文，仅当前有效申请向许愿人解密';

CREATE TABLE public.conversations (
    id uuid NOT NULL,
    match_id uuid NOT NULL,
    write_epoch bigint DEFAULT 1 NOT NULL,
    next_delivery_seq bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT conversations_next_delivery_seq_check CHECK ((next_delivery_seq > 0)),
    CONSTRAINT conversations_version_check CHECK ((version > 0)),
    CONSTRAINT conversations_write_epoch_check CHECK ((write_epoch > 0)),
    CONSTRAINT conversations_match_id_key UNIQUE (match_id),
    CONSTRAINT conversations_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.conversations IS '匹配会话及事务内送达计数器，不使用不可回滚数据库序列';
COMMENT ON COLUMN public.conversations.id IS '随机对象标识';
COMMENT ON COLUMN public.conversations.match_id IS '唯一对应的匹配关系';
COMMENT ON COLUMN public.conversations.write_epoch IS '写权限代次，旧异步任务必须复核';
COMMENT ON COLUMN public.conversations.next_delivery_seq IS '下一送达序号，仅在送达事务中更新';
COMMENT ON COLUMN public.conversations.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.conversations.version IS '会话聚合版本，送达或写权限变化时递增';

CREATE TABLE public.matches (
    id uuid NOT NULL,
    wish_id uuid NOT NULL,
    requester_id uuid NOT NULL,
    helper_id uuid NOT NULL,
    status text DEFAULT 'current'::text NOT NULL,
    started_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    ended_at timestamp with time zone,
    application_id uuid,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT matches_check CHECK ((requester_id <> helper_id)),
    CONSTRAINT matches_check1 CHECK ((((status = 'current'::text) AND (ended_at IS NULL)) OR ((status = 'ended'::text) AND (ended_at IS NOT NULL)))),
    CONSTRAINT matches_status_check CHECK ((status = ANY (ARRAY['current'::text, 'ended'::text]))),
    CONSTRAINT matches_version_check CHECK ((version > 0)),
    CONSTRAINT matches_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.matches IS '当前及历史匹配，普通账号注销需先按矩阵处理关系';
COMMENT ON COLUMN public.matches.id IS '随机对象标识';
COMMENT ON COLUMN public.matches.wish_id IS '对应心愿';
COMMENT ON COLUMN public.matches.requester_id IS '许愿人，与目标心愿作者一致';
COMMENT ON COLUMN public.matches.helper_id IS '应愿人';
COMMENT ON COLUMN public.matches.status IS '匹配生命周期';
COMMENT ON COLUMN public.matches.started_at IS '匹配开始时间';
COMMENT ON COLUMN public.matches.ended_at IS '匹配结束时间';
COMMENT ON COLUMN public.matches.application_id IS '形成匹配的精确申请，历史空值不推断来源';
COMMENT ON COLUMN public.matches.version IS '匹配关系自身的乐观版本，不是心愿或申请版本';

CREATE TABLE public.messages (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    sender_id uuid NOT NULL,
    delivery_seq bigint,
    delivered_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    client_message_id text NOT NULL,
    body_cipher jsonb NOT NULL,
    content_version bigint DEFAULT 1 NOT NULL,
    status text DEFAULT 'pending_scan'::text NOT NULL,
    cancel_epoch bigint NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    review_task_id uuid,
    peer_epoch bigint,
    request_digest bytea,
    CONSTRAINT messages_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT messages_cancel_epoch_check CHECK ((cancel_epoch > 0)),
    CONSTRAINT messages_check CHECK ((((status = 'delivered'::text) AND (delivery_seq IS NOT NULL) AND (delivered_at IS NOT NULL)) OR ((status <> 'delivered'::text) AND (delivery_seq IS NULL) AND (delivered_at IS NULL)))),
    CONSTRAINT messages_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT messages_delivery_seq_check CHECK ((delivery_seq > 0)),
    CONSTRAINT messages_status_check CHECK ((status = ANY (ARRAY['pending_scan'::text, 'pending_review'::text, 'delivered'::text, 'rejected'::text, 'cancelled'::text]))),
    CONSTRAINT messages_version_check CHECK ((version > 0)),
    CONSTRAINT messages_conversation_id_sender_id_client_message_id_key UNIQUE (conversation_id, sender_id, client_message_id),
    CONSTRAINT messages_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.messages IS '聊天消息，待审无送达序号，最终送达才分配且重试保持原号';
COMMENT ON COLUMN public.messages.id IS '随机对象标识';
COMMENT ON COLUMN public.messages.conversation_id IS '所属会话';
COMMENT ON COLUMN public.messages.sender_id IS '发送者，是否为原参与者须命令复核';
COMMENT ON COLUMN public.messages.delivery_seq IS '送达顺序号，未送达时为空';
COMMENT ON COLUMN public.messages.delivered_at IS '最终送达时间，未送达时为空';
COMMENT ON COLUMN public.messages.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.messages.client_message_id IS '客户端不透明幂等标识，不限定为UUID';
COMMENT ON COLUMN public.messages.body_cipher IS '消息正文，保存信封密文而非明文';
COMMENT ON COLUMN public.messages.content_version IS '消息内容版本';
COMMENT ON COLUMN public.messages.status IS '审核与送达状态';
COMMENT ON COLUMN public.messages.cancel_epoch IS '提交时取消或写权限代次';
COMMENT ON COLUMN public.messages.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.messages.review_task_id IS '当前精确审核任务，历史空值不能授权送达';
COMMENT ON COLUMN public.messages.peer_epoch IS '提交时接收方权限代次，变化后旧消息不能复活';
COMMENT ON COLUMN public.messages.request_digest IS '客户端消息请求带密钥摘要，不保存明文请求';

CREATE TABLE public.share_links (
    id uuid NOT NULL,
    target_kind text NOT NULL,
    target_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT share_links_target_kind_check CHECK ((target_kind = ANY (ARRAY['wish'::text, 'story'::text]))),
    CONSTRAINT share_links_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.share_links IS '分享技术引用，不保存正文或身份快照，每次打开重验当前公开资格';
COMMENT ON COLUMN public.share_links.id IS '随机不透明分享引用';
COMMENT ON COLUMN public.share_links.target_kind IS '受控目标类型';
COMMENT ON COLUMN public.share_links.target_id IS '稳定内容标识，不持有身份外键';
COMMENT ON COLUMN public.share_links.created_at IS '技术引用创建时间';

CREATE TABLE public.story_versions (
    wish_id uuid NOT NULL,
    story_version bigint NOT NULL,
    author_id uuid NOT NULL,
    body_cipher jsonb NOT NULL,
    public_body text,
    moderation_status text DEFAULT 'pending'::text NOT NULL,
    school_related boolean,
    school_detection_version text NOT NULL,
    school_detection_state text DEFAULT 'pending'::text NOT NULL,
    withdrawn_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    review_task_id uuid,
    identity_tags jsonb DEFAULT '{}'::jsonb NOT NULL,
    wordlist_version uuid,
    source_match_id uuid,
    peer_epoch bigint,
    parent_hold_epoch bigint DEFAULT 0 NOT NULL,
    published_at timestamp with time zone,
    CONSTRAINT story_versions_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT story_versions_check CHECK ((((school_detection_state = 'pending'::text) AND (school_related IS NULL)) OR ((school_detection_state = 'completed'::text) AND (school_related IS NOT NULL)))),
    CONSTRAINT story_versions_check1 CHECK (((public_body IS NULL) OR ((moderation_status = 'approved'::text) AND (school_detection_state = 'completed'::text)))),
    CONSTRAINT story_versions_identity_tags_check CHECK ((jsonb_typeof(identity_tags) = 'object'::text)),
    CONSTRAINT story_versions_moderation_status_check CHECK ((moderation_status = ANY (ARRAY['pending'::text, 'machine_passed'::text, 'human_required'::text, 'approved'::text, 'rejected'::text, 'obsolete'::text, 'cancelled'::text]))),
    CONSTRAINT story_versions_parent_hold_epoch_check CHECK ((parent_hold_epoch >= 0)),
    CONSTRAINT story_versions_public_body_check CHECK (((public_body IS NULL) OR (length(btrim(public_body)) > 0))),
    CONSTRAINT story_versions_school_detection_state_check CHECK ((school_detection_state = ANY (ARRAY['pending'::text, 'completed'::text]))),
    CONSTRAINT story_versions_school_detection_version_check CHECK ((length(btrim(school_detection_version)) > 0)),
    CONSTRAINT story_versions_story_version_check CHECK ((story_version > 0)),
    CONSTRAINT story_versions_pkey PRIMARY KEY (wish_id, story_version)
);
COMMENT ON TABLE public.story_versions IS '还愿故事版本，故事撤回和审核不回退心愿已还愿状态';
COMMENT ON COLUMN public.story_versions.wish_id IS '所属心愿';
COMMENT ON COLUMN public.story_versions.story_version IS '故事内容版本';
COMMENT ON COLUMN public.story_versions.author_id IS '故事作者，须为许愿人';
COMMENT ON COLUMN public.story_versions.body_cipher IS '原始故事正文，保存信封密文而非明文';
COMMENT ON COLUMN public.story_versions.public_body IS '经审核脱敏的故事公开投影';
COMMENT ON COLUMN public.story_versions.moderation_status IS '故事审核状态';
COMMENT ON COLUMN public.story_versions.school_related IS '涉校判定结果，未完成时为空而非默认否';
COMMENT ON COLUMN public.story_versions.school_detection_version IS '涉校检测器版本';
COMMENT ON COLUMN public.story_versions.school_detection_state IS '涉校判定状态';
COMMENT ON COLUMN public.story_versions.withdrawn_at IS '作者撤回时间';
COMMENT ON COLUMN public.story_versions.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.story_versions.review_task_id IS '精确送审任务引用，旧版本审核不能覆盖新稿';
COMMENT ON COLUMN public.story_versions.identity_tags IS '经审核允许的最小公开身份标签，不含应愿人身份';
COMMENT ON COLUMN public.story_versions.wordlist_version IS '扫描采用的普通违规词库版本';
COMMENT ON COLUMN public.story_versions.source_match_id IS '提交时原匹配技术引用，隐私重审可清空，不作为永久身份存档';
COMMENT ON COLUMN public.story_versions.peer_epoch IS '提交时原应愿人权限代次，变化后旧审核不得生效';
COMMENT ON COLUMN public.story_versions.parent_hold_epoch IS '送审时父心愿隐私屏障代次，不能用故事重审释放父内容屏障';
COMMENT ON COLUMN public.story_versions.published_at IS '当前故事版本审核公开时间，由业务审核事务写入并作为公开排序位置';

CREATE TABLE public.wish_versions (
    wish_id uuid NOT NULL,
    content_version bigint NOT NULL,
    body_cipher jsonb NOT NULL,
    public_body text,
    identity_tags jsonb DEFAULT '{}'::jsonb NOT NULL,
    reward_method text DEFAULT 'sincere_thanks'::text NOT NULL,
    moderation_status text DEFAULT 'pending'::text NOT NULL,
    wordlist_version uuid,
    school_related boolean,
    school_detection_version text NOT NULL,
    school_detection_state text DEFAULT 'pending'::text NOT NULL,
    review_task_id uuid,
    CONSTRAINT wish_versions_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT wish_versions_check CHECK ((((school_detection_state = 'pending'::text) AND (school_related IS NULL)) OR ((school_detection_state = 'completed'::text) AND (school_related IS NOT NULL)))),
    CONSTRAINT wish_versions_check1 CHECK (((public_body IS NULL) OR ((moderation_status = 'approved'::text) AND (school_detection_state = 'completed'::text)))),
    CONSTRAINT wish_versions_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT wish_versions_identity_tags_check CHECK ((jsonb_typeof(identity_tags) = 'object'::text)),
    CONSTRAINT wish_versions_moderation_status_check CHECK ((moderation_status = ANY (ARRAY['pending'::text, 'machine_passed'::text, 'human_required'::text, 'approved'::text, 'rejected'::text, 'obsolete'::text, 'cancelled'::text]))),
    CONSTRAINT wish_versions_public_body_check CHECK (((public_body IS NULL) OR (((char_length(public_body) >= 1) AND (char_length(public_body) <= 500)) AND (length(btrim(public_body)) > 0)))),
    CONSTRAINT wish_versions_reward_method_check CHECK ((reward_method = ANY (ARRAY['milk_tea'::text, 'meal'::text, 'reciprocal_gift'::text, 'skill_exchange'::text, 'sincere_thanks'::text]))),
    CONSTRAINT wish_versions_school_detection_state_check CHECK ((school_detection_state = ANY (ARRAY['pending'::text, 'completed'::text]))),
    CONSTRAINT wish_versions_school_detection_version_check CHECK ((length(btrim(school_detection_version)) > 0)),
    CONSTRAINT wish_versions_pkey PRIMARY KEY (wish_id, content_version)
);
COMMENT ON TABLE public.wish_versions IS '精确心愿内容版本，公开正文只保存经审核脱敏投影';
COMMENT ON COLUMN public.wish_versions.wish_id IS '所属心愿';
COMMENT ON COLUMN public.wish_versions.content_version IS '内容版本';
COMMENT ON COLUMN public.wish_versions.body_cipher IS '原始心愿正文，保存信封密文而非明文';
COMMENT ON COLUMN public.wish_versions.public_body IS '审核脱敏正文，原始密文长度仍须服务层验证';
COMMENT ON COLUMN public.wish_versions.identity_tags IS '经审核允许的最小展示标签，不得包含完整身份';
COMMENT ON COLUMN public.wish_versions.reward_method IS '报答方式';
COMMENT ON COLUMN public.wish_versions.moderation_status IS '当前内容审核状态';
COMMENT ON COLUMN public.wish_versions.wordlist_version IS '采用的词库发布版本引用';
COMMENT ON COLUMN public.wish_versions.school_related IS '涉校判定结果，未完成时为空而非默认否';
COMMENT ON COLUMN public.wish_versions.school_detection_version IS '涉校检测器版本';
COMMENT ON COLUMN public.wish_versions.school_detection_state IS '涉校判定状态';
COMMENT ON COLUMN public.wish_versions.review_task_id IS '当前显式送审任务引用，旧任务不能覆盖本版本的新审核轮次';

CREATE TABLE public.wishes (
    id uuid NOT NULL,
    owner_id uuid NOT NULL,
    state text DEFAULT 'draft'::text NOT NULL,
    current_content_version bigint NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    category text NOT NULL,
    campus text NOT NULL,
    duration_days smallint DEFAULT 7 NOT NULL,
    expires_at timestamp with time zone,
    refresh_on_publish boolean DEFAULT false NOT NULL,
    published_at timestamp with time zone,
    restriction_epoch bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    draft_deleted_at timestamp with time zone,
    current_story_version bigint DEFAULT 0 NOT NULL,
    peer_deleted boolean DEFAULT false NOT NULL,
    CONSTRAINT wishes_campus_check CHECK ((campus = ANY (ARRAY['wushan'::text, 'university_town'::text, 'international'::text, 'any'::text]))),
    CONSTRAINT wishes_category_check CHECK ((category = ANY (ARRAY['help'::text, 'team'::text, 'items'::text, 'dating'::text, 'skill_exchange'::text, 'other'::text]))),
    CONSTRAINT wishes_check CHECK (((state <> ALL (ARRAY['published'::text, 'matching'::text])) OR ((published_at IS NOT NULL) AND (expires_at IS NOT NULL)))),
    CONSTRAINT wishes_current_content_version_check CHECK ((current_content_version > 0)),
    CONSTRAINT wishes_current_story_version_check CHECK ((current_story_version >= 0)),
    CONSTRAINT wishes_duration_days_check CHECK (((duration_days >= 1) AND (duration_days <= 30))),
    CONSTRAINT wishes_restriction_epoch_check CHECK ((restriction_epoch > 0)),
    CONSTRAINT wishes_state_check CHECK ((state = ANY (ARRAY['draft'::text, 'pending_review'::text, 'rejected'::text, 'ready'::text, 'published'::text, 'matching'::text, 'realized'::text, 'fulfilled'::text, 'withdrawn'::text, 'expired'::text]))),
    CONSTRAINT wishes_version_check CHECK ((version > 0)),
    CONSTRAINT wishes_id_owner_id_key UNIQUE (id, owner_id),
    CONSTRAINT wishes_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.wishes IS '心愿聚合，额度及当前可见性由持有账号锁的事务判定';
COMMENT ON COLUMN public.wishes.id IS '随机对象标识';
COMMENT ON COLUMN public.wishes.owner_id IS '许愿人';
COMMENT ON COLUMN public.wishes.state IS '心愿业务状态';
COMMENT ON COLUMN public.wishes.current_content_version IS '当前内容版本，与聚合版本分离';
COMMENT ON COLUMN public.wishes.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.wishes.category IS '批准契约中的心愿分类';
COMMENT ON COLUMN public.wishes.campus IS '批准契约中的校区范围';
COMMENT ON COLUMN public.wishes.duration_days IS '有效期整数天数，默认七天';
COMMENT ON COLUMN public.wishes.expires_at IS '当前权威到期时间，查询必须实时复核';
COMMENT ON COLUMN public.wishes.refresh_on_publish IS '下次上架是否按规则刷新到期时间';
COMMENT ON COLUMN public.wishes.published_at IS '最近上架时间，用于游标排序';
COMMENT ON COLUMN public.wishes.restriction_epoch IS '内容交互限制代次';
COMMENT ON COLUMN public.wishes.created_at IS '创建时间，以带时区时间保存';
COMMENT ON COLUMN public.wishes.draft_deleted_at IS '草稿删除生效时间，保留技术引用但不再允许读取或复活';
COMMENT ON COLUMN public.wishes.current_story_version IS '当前还愿故事内容版本，零表示尚未提交；故事命令使用心愿聚合版本';
COMMENT ON COLUMN public.wishes.peer_deleted IS '原应愿人注销后的通用提示，不保留原账号或身份';

CREATE INDEX applications_applicant_idx ON public.applications USING btree (applicant_id, created_at DESC, id DESC);

CREATE UNIQUE INDEX applications_live_unique ON public.applications USING btree (wish_id, applicant_id) WHERE (status = ANY (ARRAY['pending_scan'::text, 'pending_review'::text, 'active'::text]));

CREATE INDEX applications_review_task_idx ON public.applications USING btree (review_task_id) WHERE (review_task_id IS NOT NULL);

CREATE UNIQUE INDEX matches_application_unique ON public.matches USING btree (application_id) WHERE (application_id IS NOT NULL);

CREATE UNIQUE INDEX matches_current_unique ON public.matches USING btree (wish_id) WHERE (status = 'current'::text);

CREATE UNIQUE INDEX messages_delivery_unique ON public.messages USING btree (conversation_id, delivery_seq) WHERE (delivery_seq IS NOT NULL);

CREATE INDEX wishes_expiry_idx ON public.wishes USING btree (state, expires_at);

CREATE INDEX wishes_owner_state_idx ON public.wishes USING btree (owner_id, state, id);

CREATE INDEX wishes_public_idx ON public.wishes USING btree (category, campus, published_at DESC, id DESC);

CREATE INDEX wishes_visible_owner_idx ON public.wishes USING btree (owner_id, id) WHERE (draft_deleted_at IS NULL);
