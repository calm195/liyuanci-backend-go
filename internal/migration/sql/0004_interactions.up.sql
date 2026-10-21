CREATE TABLE public.wishes (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES public.accounts(id),
    state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'pending_review', 'rejected', 'ready', 'published', 'matching', 'realized', 'fulfilled', 'withdrawn', 'expired')),
    current_content_version bigint NOT NULL CHECK (current_content_version > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    category text NOT NULL CHECK (category IN ('help', 'team', 'items', 'dating', 'skill_exchange', 'other')),
    campus text NOT NULL CHECK (campus IN ('wushan', 'university_town', 'international', 'any')),
    duration_days smallint NOT NULL DEFAULT 7 CHECK (duration_days BETWEEN 1 AND 30),
    expires_at timestamptz,
    refresh_on_publish boolean NOT NULL DEFAULT false,
    published_at timestamptz,
    restriction_epoch bigint NOT NULL DEFAULT 1 CHECK (restriction_epoch > 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (id, owner_id),
    CHECK (state NOT IN ('published','matching') OR (published_at IS NOT NULL AND expires_at IS NOT NULL))
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

CREATE INDEX wishes_owner_state_idx ON public.wishes(owner_id, state, id);
CREATE INDEX wishes_public_idx ON public.wishes(category, campus, published_at DESC, id DESC);
CREATE INDEX wishes_expiry_idx ON public.wishes(state, expires_at);
CREATE TABLE public.wish_versions (
    wish_id uuid NOT NULL REFERENCES public.wishes(id),
    content_version bigint NOT NULL CHECK (content_version > 0),
    body_cipher jsonb NOT NULL CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    public_body text CHECK (public_body IS NULL OR (char_length(public_body) BETWEEN 1 AND 500 AND length(btrim(public_body)) > 0)),
    identity_tags jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(identity_tags) = 'object'),
    reward_method text NOT NULL DEFAULT 'sincere_thanks' CHECK (reward_method IN ('milk_tea', 'meal', 'reciprocal_gift', 'skill_exchange', 'sincere_thanks')),
    moderation_status text NOT NULL DEFAULT 'pending' CHECK (moderation_status IN ('pending', 'machine_passed', 'human_required', 'approved', 'rejected', 'obsolete', 'cancelled')),
    wordlist_version uuid,
    school_related boolean,
    school_detection_version text NOT NULL CHECK (length(btrim(school_detection_version)) > 0),
    school_detection_state text NOT NULL DEFAULT 'pending' CHECK (school_detection_state IN ('pending', 'completed')),
    PRIMARY KEY (wish_id, content_version),
    CHECK ((school_detection_state = 'pending' AND school_related IS NULL) OR (school_detection_state = 'completed' AND school_related IS NOT NULL)),
    CHECK (public_body IS NULL OR (moderation_status = 'approved' AND school_detection_state = 'completed'))
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

ALTER TABLE public.wishes ADD CONSTRAINT wishes_current_content_fk FOREIGN KEY (id, current_content_version) REFERENCES public.wish_versions(wish_id, content_version) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE public.applications (
    id uuid PRIMARY KEY,
    wish_id uuid NOT NULL,
    wish_content_version bigint NOT NULL CHECK (wish_content_version > 0),
    applicant_id uuid NOT NULL REFERENCES public.accounts(id),
    status text NOT NULL DEFAULT 'pending_scan' CHECK (status IN ('pending_scan', 'pending_review', 'active', 'accepted', 'withdrawn', 'rejected', 'invalidated')),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    message_cipher jsonb NOT NULL CHECK (message_cipher IS NULL OR jsonb_typeof(message_cipher) = 'object'),
    content_version bigint NOT NULL DEFAULT 1 CHECK (content_version > 0),
    cancel_reason text,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (wish_id, wish_content_version) REFERENCES public.wish_versions(wish_id, content_version)
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

CREATE UNIQUE INDEX applications_live_unique ON public.applications(wish_id, applicant_id) WHERE status IN ('pending_scan','pending_review','active');
CREATE INDEX applications_applicant_idx ON public.applications(applicant_id, created_at DESC, id DESC);
CREATE TABLE public.matches (
    id uuid PRIMARY KEY,
    wish_id uuid NOT NULL,
    requester_id uuid NOT NULL,
    helper_id uuid NOT NULL REFERENCES public.accounts(id),
    status text NOT NULL DEFAULT 'current' CHECK (status IN ('current', 'ended')),
    started_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at timestamptz,
    FOREIGN KEY (wish_id, requester_id) REFERENCES public.wishes(id, owner_id),
    CHECK (requester_id <> helper_id),
    CHECK ((status = 'current' AND ended_at IS NULL) OR (status = 'ended' AND ended_at IS NOT NULL))
);
COMMENT ON TABLE public.matches IS '当前及历史匹配，普通账号注销需先按矩阵处理关系';
COMMENT ON COLUMN public.matches.id IS '随机对象标识';
COMMENT ON COLUMN public.matches.wish_id IS '对应心愿';
COMMENT ON COLUMN public.matches.requester_id IS '许愿人，与目标心愿作者一致';
COMMENT ON COLUMN public.matches.helper_id IS '应愿人';
COMMENT ON COLUMN public.matches.status IS '匹配生命周期';
COMMENT ON COLUMN public.matches.started_at IS '匹配开始时间';
COMMENT ON COLUMN public.matches.ended_at IS '匹配结束时间';

CREATE UNIQUE INDEX matches_current_unique ON public.matches(wish_id) WHERE status = 'current';
CREATE TABLE public.conversations (
    id uuid PRIMARY KEY,
    match_id uuid NOT NULL UNIQUE REFERENCES public.matches(id),
    write_epoch bigint NOT NULL DEFAULT 1 CHECK (write_epoch > 0),
    next_delivery_seq bigint NOT NULL DEFAULT 1 CHECK (next_delivery_seq > 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE public.conversations IS '匹配会话及事务内送达计数器，不使用不可回滚数据库序列';
COMMENT ON COLUMN public.conversations.id IS '随机对象标识';
COMMENT ON COLUMN public.conversations.match_id IS '唯一对应的匹配关系';
COMMENT ON COLUMN public.conversations.write_epoch IS '写权限代次，旧异步任务必须复核';
COMMENT ON COLUMN public.conversations.next_delivery_seq IS '下一送达序号，仅在送达事务中更新';
COMMENT ON COLUMN public.conversations.created_at IS '创建时间，以带时区时间保存';

CREATE TABLE public.messages (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES public.conversations(id),
    sender_id uuid NOT NULL REFERENCES public.accounts(id),
    delivery_seq bigint CHECK (delivery_seq > 0),
    delivered_at timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    client_message_id uuid NOT NULL,
    body_cipher jsonb NOT NULL CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    content_version bigint NOT NULL DEFAULT 1 CHECK (content_version > 0),
    status text NOT NULL DEFAULT 'pending_scan' CHECK (status IN ('pending_scan', 'pending_review', 'delivered', 'rejected', 'cancelled')),
    cancel_epoch bigint NOT NULL CHECK (cancel_epoch > 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (conversation_id, sender_id, client_message_id),
    CHECK ((status = 'delivered' AND delivery_seq IS NOT NULL AND delivered_at IS NOT NULL) OR (status <> 'delivered' AND delivery_seq IS NULL AND delivered_at IS NULL))
);
COMMENT ON TABLE public.messages IS '聊天消息，待审无送达序号，最终送达才分配且重试保持原号';
COMMENT ON COLUMN public.messages.id IS '随机对象标识';
COMMENT ON COLUMN public.messages.conversation_id IS '所属会话';
COMMENT ON COLUMN public.messages.sender_id IS '发送者，是否为原参与者须命令复核';
COMMENT ON COLUMN public.messages.delivery_seq IS '送达顺序号，未送达时为空';
COMMENT ON COLUMN public.messages.delivered_at IS '最终送达时间，未送达时为空';
COMMENT ON COLUMN public.messages.version IS '聚合版本，用于期望版本校验';
COMMENT ON COLUMN public.messages.client_message_id IS '客户端随机幂等标识';
COMMENT ON COLUMN public.messages.body_cipher IS '消息正文，保存信封密文而非明文';
COMMENT ON COLUMN public.messages.content_version IS '消息内容版本';
COMMENT ON COLUMN public.messages.status IS '审核与送达状态';
COMMENT ON COLUMN public.messages.cancel_epoch IS '提交时取消或写权限代次';
COMMENT ON COLUMN public.messages.created_at IS '创建时间，以带时区时间保存';

CREATE UNIQUE INDEX messages_delivery_unique ON public.messages(conversation_id, delivery_seq) WHERE delivery_seq IS NOT NULL;
CREATE TABLE public.story_versions (
    wish_id uuid NOT NULL,
    story_version bigint NOT NULL CHECK (story_version > 0),
    author_id uuid NOT NULL,
    body_cipher jsonb NOT NULL CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    public_body text CHECK (public_body IS NULL OR length(btrim(public_body)) > 0),
    moderation_status text NOT NULL DEFAULT 'pending' CHECK (moderation_status IN ('pending', 'machine_passed', 'human_required', 'approved', 'rejected', 'obsolete', 'cancelled')),
    school_related boolean,
    school_detection_version text NOT NULL CHECK (length(btrim(school_detection_version)) > 0),
    school_detection_state text NOT NULL DEFAULT 'pending' CHECK (school_detection_state IN ('pending', 'completed')),
    withdrawn_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (wish_id, story_version),
    FOREIGN KEY (wish_id, author_id) REFERENCES public.wishes(id, owner_id),
    CHECK ((school_detection_state = 'pending' AND school_related IS NULL) OR (school_detection_state = 'completed' AND school_related IS NOT NULL)),
    CHECK (public_body IS NULL OR (moderation_status = 'approved' AND school_detection_state = 'completed'))
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

CREATE TABLE public.content_registry (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('wish', 'application', 'message', 'story')),
    entity_id uuid NOT NULL,
    content_version bigint NOT NULL CHECK (content_version > 0),
    author_id uuid NOT NULL REFERENCES public.accounts(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'machine_passed', 'human_required', 'approved', 'rejected', 'obsolete', 'cancelled')),
    school_related boolean,
    school_detection_version text NOT NULL CHECK (length(btrim(school_detection_version)) > 0),
    school_detection_state text NOT NULL DEFAULT 'pending' CHECK (school_detection_state IN ('pending', 'completed')),
    UNIQUE (kind, entity_id, content_version),
    UNIQUE (id, content_version),
    CHECK ((school_detection_state = 'pending' AND school_related IS NULL) OR (school_detection_state = 'completed' AND school_related IS NOT NULL))
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
