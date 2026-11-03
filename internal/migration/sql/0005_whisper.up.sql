-- 树洞、昵称、评论、抱抱和胶囊；版本 SQL 是结构定义的唯一来源。

CREATE TABLE public.whisper_aliases (
    post_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    label text NOT NULL,
    CONSTRAINT whisper_aliases_label_check CHECK ((((char_length(label) >= 1) AND (char_length(label) <= 64)) AND (label ~ '[^[:space:]]'::text))),
    CONSTRAINT whisper_aliases_pkey PRIMARY KEY (post_id, actor_id)
);
COMMENT ON TABLE public.whisper_aliases IS '帖子上下文随机别名，跨帖子独立生成';
COMMENT ON COLUMN public.whisper_aliases.post_id IS '别名上下文帖子';
COMMENT ON COLUMN public.whisper_aliases.actor_id IS '内部参与账号，不输出别名行或账号标识';
COMMENT ON COLUMN public.whisper_aliases.label IS '独立随机展示别名，允许重名';

CREATE TABLE public.whisper_capsules (
    id uuid NOT NULL,
    owner_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    content_registry_id uuid NOT NULL,
    body_cipher jsonb,
    unlock_at timestamp with time zone NOT NULL,
    review_state text DEFAULT 'scanning'::text NOT NULL,
    lifecycle text DEFAULT 'active'::text NOT NULL,
    privacy_epoch bigint DEFAULT 0 NOT NULL,
    restriction_epoch bigint DEFAULT 0 NOT NULL,
    review_round bigint DEFAULT 0 NOT NULL,
    review_task_id uuid,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT whisper_capsules_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT whisper_capsules_check CHECK ((((lifecycle = 'active'::text) AND (body_cipher IS NOT NULL)) OR ((lifecycle = ANY (ARRAY['cancelled'::text, 'deleted'::text])) AND (body_cipher IS NULL)))),
    CONSTRAINT whisper_capsules_lifecycle_check CHECK ((lifecycle = ANY (ARRAY['active'::text, 'cancelled'::text, 'deleted'::text]))),
    CONSTRAINT whisper_capsules_privacy_epoch_check CHECK ((privacy_epoch >= 0)),
    CONSTRAINT whisper_capsules_restriction_epoch_check CHECK ((restriction_epoch >= 0)),
    CONSTRAINT whisper_capsules_review_round_check CHECK ((review_round >= 0)),
    CONSTRAINT whisper_capsules_review_state_check CHECK ((review_state = ANY (ARRAY['draft'::text, 'scanning'::text, 'pending'::text, 'approved'::text, 'rejected'::text, 'cancelled'::text]))),
    CONSTRAINT whisper_capsules_unlock_at_check CHECK (((unlock_at IS NOT NULL) AND (unlock_at = date_trunc('second'::text, unlock_at)))),
    CONSTRAINT whisper_capsules_version_check CHECK ((version >= 1)),
    CONSTRAINT whisper_capsules_content_registry_id_key UNIQUE (content_registry_id),
    CONSTRAINT whisper_capsules_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.whisper_capsules IS '只给本人的加密胶囊，无接收者或公开正文';
COMMENT ON COLUMN public.whisper_capsules.id IS '随机胶囊标识';
COMMENT ON COLUMN public.whisper_capsules.owner_id IS '仅本人日常读取的所有者';
COMMENT ON COLUMN public.whisper_capsules.version IS '资源乐观版本';
COMMENT ON COLUMN public.whisper_capsules.content_registry_id IS '精确内容注册标识，昵称来源使用本随机标识';
COMMENT ON COLUMN public.whisper_capsules.body_cipher IS '封存后不可改文，取消或删除清空密文';
COMMENT ON COLUMN public.whisper_capsules.unlock_at IS '服务端按秒保存的UTC预约时间';
COMMENT ON COLUMN public.whisper_capsules.review_state IS '当前审核状态，不等于实际公开或解锁结果';
COMMENT ON COLUMN public.whisper_capsules.lifecycle IS '业务生命周期，治理和隐私屏障独立判断';
COMMENT ON COLUMN public.whisper_capsules.privacy_epoch IS '隐私屏障代次';
COMMENT ON COLUMN public.whisper_capsules.restriction_epoch IS '治理限制代次';
COMMENT ON COLUMN public.whisper_capsules.review_round IS '独立审核轮次';
COMMENT ON COLUMN public.whisper_capsules.review_task_id IS '当前审核轮次任务引用，迟到任务不得覆盖';
COMMENT ON COLUMN public.whisper_capsules.created_at IS '封存时间';

CREATE TABLE public.whisper_comment_versions (
    comment_id uuid NOT NULL,
    content_version bigint NOT NULL,
    content_registry_id uuid NOT NULL,
    body_cipher jsonb NOT NULL,
    candidate_cipher jsonb,
    identity_mode text NOT NULL,
    identity_cipher jsonb NOT NULL,
    nickname_version bigint,
    dictionary_version bigint,
    review_state text DEFAULT 'scanning'::text NOT NULL,
    review_task_id uuid,
    public_identity jsonb,
    public_body text,
    school_related boolean,
    notice_codes jsonb,
    review_result_cipher jsonb,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT whisper_comment_versions_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT whisper_comment_versions_candidate_cipher_check CHECK (((candidate_cipher IS NULL) OR (jsonb_typeof(candidate_cipher) = 'object'::text))),
    CONSTRAINT whisper_comment_versions_check CHECK (((identity_mode = 'named'::text) = (nickname_version IS NOT NULL))),
    CONSTRAINT whisper_comment_versions_check1 CHECK (((identity_mode = 'semi'::text) = (dictionary_version IS NOT NULL))),
    CONSTRAINT whisper_comment_versions_check2 CHECK ((((public_body IS NULL) AND (public_identity IS NULL)) OR ((public_body IS NOT NULL) AND (public_identity IS NOT NULL) AND (school_related IS NOT NULL) AND (review_state = 'approved'::text)))),
    CONSTRAINT whisper_comment_versions_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT whisper_comment_versions_dictionary_version_check CHECK ((dictionary_version > 0)),
    CONSTRAINT whisper_comment_versions_identity_cipher_check CHECK (((identity_cipher IS NULL) OR (jsonb_typeof(identity_cipher) = 'object'::text))),
    CONSTRAINT whisper_comment_versions_identity_mode_check CHECK ((identity_mode = ANY (ARRAY['anonymous'::text, 'semi'::text, 'named'::text]))),
    CONSTRAINT whisper_comment_versions_nickname_version_check CHECK ((nickname_version > 0)),
    CONSTRAINT whisper_comment_versions_notice_codes_check CHECK (((notice_codes IS NULL) OR (jsonb_typeof(notice_codes) = 'array'::text))),
    CONSTRAINT whisper_comment_versions_public_body_check CHECK (((public_body IS NULL) OR (((char_length(public_body) >= 1) AND (char_length(public_body) <= 500)) AND (public_body ~ '[^[:space:]]'::text)))),
    CONSTRAINT whisper_comment_versions_public_identity_check CHECK (((public_identity IS NULL) OR (jsonb_typeof(public_identity) = 'object'::text))),
    CONSTRAINT whisper_comment_versions_review_result_cipher_check CHECK (((review_result_cipher IS NULL) OR (jsonb_typeof(review_result_cipher) = 'object'::text))),
    CONSTRAINT whisper_comment_versions_review_state_check CHECK ((review_state = ANY (ARRAY['draft'::text, 'scanning'::text, 'pending'::text, 'approved'::text, 'rejected'::text, 'cancelled'::text]))),
    CONSTRAINT whisper_comment_versions_content_registry_id_key UNIQUE (content_registry_id),
    CONSTRAINT whisper_comment_versions_pkey PRIMARY KEY (comment_id, content_version)
);
COMMENT ON TABLE public.whisper_comment_versions IS '暖评不可变输入与可撤销公开投影';
COMMENT ON COLUMN public.whisper_comment_versions.comment_id IS '所属暖评';
COMMENT ON COLUMN public.whisper_comment_versions.content_version IS '评论正文版本';
COMMENT ON COLUMN public.whisper_comment_versions.content_registry_id IS '精确内容注册标识，昵称来源使用本随机标识';
COMMENT ON COLUMN public.whisper_comment_versions.body_cipher IS '用途绑定原始暖评密文';
COMMENT ON COLUMN public.whisper_comment_versions.candidate_cipher IS '待审脱敏候选密文';
COMMENT ON COLUMN public.whisper_comment_versions.identity_mode IS '本版本选择的匿名档位';
COMMENT ON COLUMN public.whisper_comment_versions.identity_cipher IS '自选标签或昵称输入的用途绑定密文';
COMMENT ON COLUMN public.whisper_comment_versions.nickname_version IS '固定昵称快照所引用的版本';
COMMENT ON COLUMN public.whisper_comment_versions.dictionary_version IS '半匿名受控字典版本';
COMMENT ON COLUMN public.whisper_comment_versions.review_state IS '当前审核状态，不等于实际公开或解锁结果';
COMMENT ON COLUMN public.whisper_comment_versions.review_task_id IS '当前审核轮次任务引用，迟到任务不得覆盖';
COMMENT ON COLUMN public.whisper_comment_versions.public_identity IS '当前已审最小身份快照，可撤销';
COMMENT ON COLUMN public.whisper_comment_versions.public_body IS '当前已审脱敏正文，非私有原文';
COMMENT ON COLUMN public.whisper_comment_versions.school_related IS '已完成涉校判断结果，未完成为空';
COMMENT ON COLUMN public.whisper_comment_versions.notice_codes IS '结构化审核展示提示';
COMMENT ON COLUMN public.whisper_comment_versions.review_result_cipher IS '精确版本驳回原因和建议密文';
COMMENT ON COLUMN public.whisper_comment_versions.created_at IS '版本创建时间';

CREATE TABLE public.whisper_comments (
    id uuid NOT NULL,
    post_id uuid,
    author_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    content_version bigint DEFAULT 1 NOT NULL,
    parent_content_version bigint DEFAULT 1 NOT NULL,
    comments_epoch bigint DEFAULT 1 NOT NULL,
    review_state text DEFAULT 'scanning'::text NOT NULL,
    lifecycle text DEFAULT 'active'::text NOT NULL,
    privacy_epoch bigint DEFAULT 0 NOT NULL,
    restriction_epoch bigint DEFAULT 0 NOT NULL,
    review_round bigint DEFAULT 0 NOT NULL,
    published_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    recovery_cipher jsonb,
    CONSTRAINT whisper_comments_check CHECK (((post_id IS NOT NULL) OR (privacy_epoch > 0))),
    CONSTRAINT whisper_comments_comments_epoch_check CHECK ((comments_epoch >= 1)),
    CONSTRAINT whisper_comments_content_version_check CHECK ((content_version >= 1)),
    CONSTRAINT whisper_comments_lifecycle_check CHECK ((lifecycle = ANY (ARRAY['active'::text, 'withdrawn'::text]))),
    CONSTRAINT whisper_comments_parent_content_version_check CHECK ((parent_content_version >= 1)),
    CONSTRAINT whisper_comments_privacy_epoch_check CHECK ((privacy_epoch >= 0)),
    CONSTRAINT whisper_comments_recovery_cipher_check CHECK (((recovery_cipher IS NULL) OR (jsonb_typeof(recovery_cipher) = 'object'::text))),
    CONSTRAINT whisper_comments_restriction_epoch_check CHECK ((restriction_epoch >= 0)),
    CONSTRAINT whisper_comments_review_round_check CHECK ((review_round >= 0)),
    CONSTRAINT whisper_comments_review_state_check CHECK ((review_state = ANY (ARRAY['draft'::text, 'scanning'::text, 'pending'::text, 'approved'::text, 'rejected'::text, 'cancelled'::text]))),
    CONSTRAINT whisper_comments_version_check CHECK ((version >= 1)),
    CONSTRAINT whisper_comments_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.whisper_comments IS '单层暖评，父帖删除后保留受屏障本人记录';
COMMENT ON COLUMN public.whisper_comments.id IS '随机暖评标识';
COMMENT ON COLUMN public.whisper_comments.post_id IS '所属帖子，删除图显式解除后为空';
COMMENT ON COLUMN public.whisper_comments.author_id IS '内部暖评作者';
COMMENT ON COLUMN public.whisper_comments.version IS '资源乐观版本';
COMMENT ON COLUMN public.whisper_comments.content_version IS '当前评论正文版本';
COMMENT ON COLUMN public.whisper_comments.parent_content_version IS '创建或显式重提绑定的父正文版本';
COMMENT ON COLUMN public.whisper_comments.comments_epoch IS '提交绑定的父帖评论代次';
COMMENT ON COLUMN public.whisper_comments.review_state IS '当前审核状态，不等于实际公开或解锁结果';
COMMENT ON COLUMN public.whisper_comments.lifecycle IS '业务生命周期，治理和隐私屏障独立判断';
COMMENT ON COLUMN public.whisper_comments.privacy_epoch IS '隐私屏障代次';
COMMENT ON COLUMN public.whisper_comments.restriction_epoch IS '治理限制代次';
COMMENT ON COLUMN public.whisper_comments.review_round IS '独立审核轮次';
COMMENT ON COLUMN public.whisper_comments.published_at IS '首次或当前最终公开时间';
COMMENT ON COLUMN public.whisper_comments.created_at IS '创建时间';
COMMENT ON COLUMN public.whisper_comments.updated_at IS '最近持久修改时间';
COMMENT ON COLUMN public.whisper_comments.recovery_cipher IS '完成受控脱敏后的幸存评论候选密文';

CREATE TABLE public.whisper_hugs (
    post_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    active boolean DEFAULT true NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT whisper_hugs_version_check CHECK ((version >= 1)),
    CONSTRAINT whisper_hugs_pkey PRIMARY KEY (post_id, actor_id)
);
COMMENT ON TABLE public.whisper_hugs IS '本人抱抱目标态及取消墓碑，防止旧请求重新点亮';
COMMENT ON COLUMN public.whisper_hugs.post_id IS '目标帖子';
COMMENT ON COLUMN public.whisper_hugs.actor_id IS '内部互动账号';
COMMENT ON COLUMN public.whisper_hugs.active IS '当前有效目标态，取消保留记录';
COMMENT ON COLUMN public.whisper_hugs.version IS '独立抱抱版本，不使用帖子版本或计数';

CREATE TABLE public.whisper_nickname_versions (
    account_id uuid NOT NULL,
    nickname_version bigint NOT NULL,
    content_registry_id uuid NOT NULL,
    label_cipher jsonb NOT NULL,
    public_label text,
    review_state text DEFAULT 'scanning'::text NOT NULL,
    control_version bigint DEFAULT 1 NOT NULL,
    review_round bigint DEFAULT 0 NOT NULL,
    review_task_id uuid,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT whisper_nickname_versions_check CHECK (((public_label IS NULL) OR (review_state = 'approved'::text))),
    CONSTRAINT whisper_nickname_versions_control_version_check CHECK ((control_version >= 1)),
    CONSTRAINT whisper_nickname_versions_label_cipher_check CHECK (((label_cipher IS NULL) OR (jsonb_typeof(label_cipher) = 'object'::text))),
    CONSTRAINT whisper_nickname_versions_nickname_version_check CHECK ((nickname_version > 0)),
    CONSTRAINT whisper_nickname_versions_public_label_check CHECK (((public_label IS NULL) OR (((char_length(public_label) >= 2) AND (char_length(public_label) <= 20)) AND (public_label ~ '[^[:space:]]'::text)))),
    CONSTRAINT whisper_nickname_versions_review_round_check CHECK ((review_round >= 0)),
    CONSTRAINT whisper_nickname_versions_review_state_check CHECK ((review_state = ANY (ARRAY['draft'::text, 'scanning'::text, 'pending'::text, 'approved'::text, 'rejected'::text, 'cancelled'::text]))),
    CONSTRAINT whisper_nickname_versions_content_registry_id_key UNIQUE (content_registry_id),
    CONSTRAINT whisper_nickname_versions_pkey PRIMARY KEY (account_id, nickname_version)
);
COMMENT ON TABLE public.whisper_nickname_versions IS '独立树洞昵称不可变版本及可撤销已审展示';
COMMENT ON COLUMN public.whisper_nickname_versions.account_id IS '昵称所属账号';
COMMENT ON COLUMN public.whisper_nickname_versions.nickname_version IS '昵称内容版本';
COMMENT ON COLUMN public.whisper_nickname_versions.content_registry_id IS '精确内容注册标识，昵称来源使用本随机标识';
COMMENT ON COLUMN public.whisper_nickname_versions.label_cipher IS '用途绑定的昵称原稿信封密文';
COMMENT ON COLUMN public.whisper_nickname_versions.public_label IS '审核允许的昵称快照';
COMMENT ON COLUMN public.whisper_nickname_versions.review_state IS '当前审核状态，不等于实际公开或解锁结果';
COMMENT ON COLUMN public.whisper_nickname_versions.control_version IS '昵称版本的治理乐观版本，与偏好版本分离';
COMMENT ON COLUMN public.whisper_nickname_versions.review_round IS '独立审核轮次，重提不复用旧工作';
COMMENT ON COLUMN public.whisper_nickname_versions.review_task_id IS '当前审核轮次任务引用，迟到任务不得覆盖';
COMMENT ON COLUMN public.whisper_nickname_versions.revoked_at IS '昵称违规撤销时间，引用内容同步隐藏';
COMMENT ON COLUMN public.whisper_nickname_versions.created_at IS '昵称版本创建时间';

CREATE TABLE public.whisper_notification_refs (
    notification_id uuid NOT NULL,
    kind text NOT NULL,
    source_id uuid NOT NULL,
    target_kind text NOT NULL,
    target_id uuid NOT NULL,
    comment_id uuid,
    suppressed boolean DEFAULT false NOT NULL,
    CONSTRAINT whisper_notification_refs_check CHECK ((((kind = 'whisper_comment'::text) AND (target_kind = 'post'::text) AND (comment_id IS NOT NULL) AND (source_id = comment_id)) OR ((kind = 'whisper_capsule_unlocked'::text) AND (target_kind = 'capsule'::text) AND (comment_id IS NULL) AND (source_id = target_id)))),
    CONSTRAINT whisper_notification_refs_kind_check CHECK ((kind = ANY (ARRAY['whisper_comment'::text, 'whisper_capsule_unlocked'::text]))),
    CONSTRAINT whisper_notification_refs_target_kind_check CHECK ((target_kind = ANY (ARRAY['post'::text, 'capsule'::text]))),
    CONSTRAINT whisper_notification_refs_kind_source_id_key UNIQUE (kind, source_id),
    CONSTRAINT whisper_notification_refs_pkey PRIMARY KEY (notification_id)
);
COMMENT ON TABLE public.whisper_notification_refs IS '树洞站内通知技术引用及唯一事件键，不复制正文或身份';
COMMENT ON COLUMN public.whisper_notification_refs.notification_id IS '复用站内通知标识';
COMMENT ON COLUMN public.whisper_notification_refs.kind IS '通知事件类型';
COMMENT ON COLUMN public.whisper_notification_refs.source_id IS '首次公开评论或已解锁胶囊事件标识';
COMMENT ON COLUMN public.whisper_notification_refs.target_kind IS '受控跳转目标类型';
COMMENT ON COLUMN public.whisper_notification_refs.target_id IS '目标实体随机标识，回流必须重验';
COMMENT ON COLUMN public.whisper_notification_refs.comment_id IS '评论通知精确引用，胶囊通知为空';
COMMENT ON COLUMN public.whisper_notification_refs.suppressed IS '隐私受理后抑制读取与投递，技术定位保留至外部清理确认';

CREATE TABLE public.whisper_post_versions (
    post_id uuid NOT NULL,
    content_version bigint NOT NULL,
    content_registry_id uuid NOT NULL,
    body_cipher jsonb NOT NULL,
    candidate_cipher jsonb,
    identity_mode text NOT NULL,
    identity_cipher jsonb NOT NULL,
    nickname_version bigint,
    dictionary_version bigint,
    review_state text DEFAULT 'draft'::text NOT NULL,
    review_task_id uuid,
    public_identity jsonb,
    public_body text,
    school_related boolean,
    notice_codes jsonb,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    CONSTRAINT whisper_post_versions_body_cipher_check CHECK (((body_cipher IS NULL) OR (jsonb_typeof(body_cipher) = 'object'::text))),
    CONSTRAINT whisper_post_versions_candidate_cipher_check CHECK (((candidate_cipher IS NULL) OR (jsonb_typeof(candidate_cipher) = 'object'::text))),
    CONSTRAINT whisper_post_versions_check CHECK (((identity_mode = 'named'::text) = (nickname_version IS NOT NULL))),
    CONSTRAINT whisper_post_versions_check1 CHECK (((identity_mode = 'semi'::text) = (dictionary_version IS NOT NULL))),
    CONSTRAINT whisper_post_versions_check2 CHECK ((((public_body IS NULL) AND (public_identity IS NULL)) OR ((public_body IS NOT NULL) AND (public_identity IS NOT NULL) AND (school_related IS NOT NULL) AND (review_state = 'approved'::text)))),
    CONSTRAINT whisper_post_versions_content_version_check CHECK ((content_version > 0)),
    CONSTRAINT whisper_post_versions_dictionary_version_check CHECK ((dictionary_version > 0)),
    CONSTRAINT whisper_post_versions_identity_cipher_check CHECK (((identity_cipher IS NULL) OR (jsonb_typeof(identity_cipher) = 'object'::text))),
    CONSTRAINT whisper_post_versions_identity_mode_check CHECK ((identity_mode = ANY (ARRAY['anonymous'::text, 'semi'::text, 'named'::text]))),
    CONSTRAINT whisper_post_versions_nickname_version_check CHECK ((nickname_version > 0)),
    CONSTRAINT whisper_post_versions_notice_codes_check CHECK (((notice_codes IS NULL) OR (jsonb_typeof(notice_codes) = 'array'::text))),
    CONSTRAINT whisper_post_versions_public_body_check CHECK (((public_body IS NULL) OR (((char_length(public_body) >= 1) AND (char_length(public_body) <= 500)) AND (public_body ~ '[^[:space:]]'::text)))),
    CONSTRAINT whisper_post_versions_public_identity_check CHECK (((public_identity IS NULL) OR (jsonb_typeof(public_identity) = 'object'::text))),
    CONSTRAINT whisper_post_versions_review_state_check CHECK ((review_state = ANY (ARRAY['draft'::text, 'scanning'::text, 'pending'::text, 'approved'::text, 'rejected'::text, 'cancelled'::text]))),
    CONSTRAINT whisper_post_versions_content_registry_id_key UNIQUE (content_registry_id),
    CONSTRAINT whisper_post_versions_pkey PRIMARY KEY (post_id, content_version)
);
COMMENT ON TABLE public.whisper_post_versions IS '树洞帖子不可变正文输入与独立已审投影';
COMMENT ON COLUMN public.whisper_post_versions.post_id IS '所属帖子';
COMMENT ON COLUMN public.whisper_post_versions.content_version IS '正文版本';
COMMENT ON COLUMN public.whisper_post_versions.content_registry_id IS '精确内容注册标识，昵称来源使用本随机标识';
COMMENT ON COLUMN public.whisper_post_versions.body_cipher IS '用途绑定的正文信封密文';
COMMENT ON COLUMN public.whisper_post_versions.candidate_cipher IS '待审脱敏候选密文';
COMMENT ON COLUMN public.whisper_post_versions.identity_mode IS '本版本选择的匿名档位';
COMMENT ON COLUMN public.whisper_post_versions.identity_cipher IS '自选标签或昵称输入的用途绑定密文';
COMMENT ON COLUMN public.whisper_post_versions.nickname_version IS '固定昵称快照所引用的版本';
COMMENT ON COLUMN public.whisper_post_versions.dictionary_version IS '半匿名受控字典版本';
COMMENT ON COLUMN public.whisper_post_versions.review_state IS '当前审核状态，不等于实际公开或解锁结果';
COMMENT ON COLUMN public.whisper_post_versions.review_task_id IS '当前审核轮次任务引用，迟到任务不得覆盖';
COMMENT ON COLUMN public.whisper_post_versions.public_identity IS '当前已审最小身份快照，可撤销';
COMMENT ON COLUMN public.whisper_post_versions.public_body IS '当前已审脱敏正文，非私有原文';
COMMENT ON COLUMN public.whisper_post_versions.school_related IS '已完成涉校判断结果，未完成为空';
COMMENT ON COLUMN public.whisper_post_versions.notice_codes IS '结构化审核展示提示';
COMMENT ON COLUMN public.whisper_post_versions.created_at IS '正文版本创建时间';

CREATE TABLE public.whisper_posts (
    id uuid NOT NULL,
    owner_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    current_content_version bigint DEFAULT 1 NOT NULL,
    published_content_version bigint,
    lifecycle text DEFAULT 'active'::text NOT NULL,
    review_state text DEFAULT 'draft'::text NOT NULL,
    publish_intent boolean DEFAULT false NOT NULL,
    comments_open boolean DEFAULT true NOT NULL,
    comments_epoch bigint DEFAULT 1 NOT NULL,
    privacy_epoch bigint DEFAULT 0 NOT NULL,
    restriction_epoch bigint DEFAULT 0 NOT NULL,
    review_round bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    updated_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP NOT NULL,
    published_at timestamp with time zone,
    CONSTRAINT whisper_posts_check CHECK (((published_content_version IS NULL) OR ((published_content_version = current_content_version) AND (published_at IS NOT NULL) AND (review_state = 'approved'::text) AND (lifecycle = 'active'::text) AND publish_intent))),
    CONSTRAINT whisper_posts_comments_epoch_check CHECK ((comments_epoch >= 1)),
    CONSTRAINT whisper_posts_current_content_version_check CHECK ((current_content_version >= 1)),
    CONSTRAINT whisper_posts_lifecycle_check CHECK ((lifecycle = ANY (ARRAY['active'::text, 'withdrawn'::text]))),
    CONSTRAINT whisper_posts_privacy_epoch_check CHECK ((privacy_epoch >= 0)),
    CONSTRAINT whisper_posts_published_content_version_check CHECK ((published_content_version > 0)),
    CONSTRAINT whisper_posts_restriction_epoch_check CHECK ((restriction_epoch >= 0)),
    CONSTRAINT whisper_posts_review_round_check CHECK ((review_round >= 0)),
    CONSTRAINT whisper_posts_review_state_check CHECK ((review_state = ANY (ARRAY['draft'::text, 'scanning'::text, 'pending'::text, 'approved'::text, 'rejected'::text, 'cancelled'::text]))),
    CONSTRAINT whisper_posts_version_check CHECK ((version >= 1)),
    CONSTRAINT whisper_posts_pkey PRIMARY KEY (id)
);
COMMENT ON TABLE public.whisper_posts IS '独立文字树洞帖子聚合，不占心愿额度';
COMMENT ON COLUMN public.whisper_posts.id IS '随机帖子标识';
COMMENT ON COLUMN public.whisper_posts.owner_id IS '内部作者账号，不向公共投影输出';
COMMENT ON COLUMN public.whisper_posts.version IS '资源乐观版本';
COMMENT ON COLUMN public.whisper_posts.current_content_version IS '当前正文版本';
COMMENT ON COLUMN public.whisper_posts.published_content_version IS '当前公开正文版本，未公开为空';
COMMENT ON COLUMN public.whisper_posts.lifecycle IS '业务生命周期，治理和隐私屏障独立判断';
COMMENT ON COLUMN public.whisper_posts.review_state IS '当前审核状态，不等于实际公开或解锁结果';
COMMENT ON COLUMN public.whisper_posts.publish_intent IS '显式提交授予当前轮审核后自动公开意愿';
COMMENT ON COLUMN public.whisper_posts.comments_open IS '评论写入开关，关闭不删除已公开评论';
COMMENT ON COLUMN public.whisper_posts.comments_epoch IS '评论上下文代次';
COMMENT ON COLUMN public.whisper_posts.privacy_epoch IS '隐私屏障代次';
COMMENT ON COLUMN public.whisper_posts.restriction_epoch IS '治理限制代次';
COMMENT ON COLUMN public.whisper_posts.review_round IS '独立送审轮次';
COMMENT ON COLUMN public.whisper_posts.created_at IS '创建时间，本人列表排序依据';
COMMENT ON COLUMN public.whisper_posts.updated_at IS '最近持久状态修改时间';
COMMENT ON COLUMN public.whisper_posts.published_at IS '当前版本最终公开时间';

CREATE TABLE public.whisper_preferences (
    account_id uuid NOT NULL,
    version bigint DEFAULT 1 NOT NULL,
    active_nickname_version bigint,
    candidate_nickname_version bigint,
    next_nickname_version bigint DEFAULT 1 NOT NULL,
    CONSTRAINT whisper_preferences_active_nickname_version_check CHECK ((active_nickname_version > 0)),
    CONSTRAINT whisper_preferences_candidate_nickname_version_check CHECK ((candidate_nickname_version > 0)),
    CONSTRAINT whisper_preferences_next_nickname_version_check CHECK ((next_nickname_version >= 1)),
    CONSTRAINT whisper_preferences_version_check CHECK ((version >= 1)),
    CONSTRAINT whisper_preferences_pkey PRIMARY KEY (account_id)
);
COMMENT ON TABLE public.whisper_preferences IS '树洞昵称偏好，旧账号可按唯一键幂等初始化';
COMMENT ON COLUMN public.whisper_preferences.account_id IS '偏好所属账号';
COMMENT ON COLUMN public.whisper_preferences.version IS '资源乐观版本';
COMMENT ON COLUMN public.whisper_preferences.active_nickname_version IS '当前可选已审昵称版本';
COMMENT ON COLUMN public.whisper_preferences.candidate_nickname_version IS '当前候选昵称版本，不替换已批准昵称';
COMMENT ON COLUMN public.whisper_preferences.next_nickname_version IS '下一次分配的不可复用昵称版本';

CREATE INDEX whisper_capsules_due_idx ON public.whisper_capsules USING btree (unlock_at, id) WHERE (lifecycle = 'active'::text);

CREATE INDEX whisper_capsules_owner_idx ON public.whisper_capsules USING btree (owner_id, created_at DESC, id DESC);

CREATE INDEX whisper_comments_owner_idx ON public.whisper_comments USING btree (author_id, created_at DESC, id DESC);

CREATE INDEX whisper_comments_public_idx ON public.whisper_comments USING btree (post_id, published_at, id) WHERE (published_at IS NOT NULL);

CREATE INDEX whisper_nicknames_owner_idx ON public.whisper_nickname_versions USING btree (account_id, nickname_version DESC);

CREATE INDEX whisper_posts_owner_idx ON public.whisper_posts USING btree (owner_id, created_at DESC, id DESC);

CREATE INDEX whisper_posts_public_idx ON public.whisper_posts USING btree (published_at DESC, id DESC) WHERE (published_content_version IS NOT NULL);
