-- 树洞数据基础；仅由显式迁移入口执行，不启用业务路由。

CREATE TABLE public.whisper_preferences (
    account_id uuid PRIMARY KEY REFERENCES public.accounts(id),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    active_nickname_version bigint CHECK (active_nickname_version > 0),
    candidate_nickname_version bigint CHECK (candidate_nickname_version > 0),
    next_nickname_version bigint NOT NULL DEFAULT 1 CHECK (next_nickname_version >= 1)
);

COMMENT ON TABLE public.whisper_preferences IS '树洞昵称偏好，旧账号可按唯一键幂等初始化';

COMMENT ON COLUMN public.whisper_preferences.account_id IS '偏好所属账号';

COMMENT ON COLUMN public.whisper_preferences.version IS '资源乐观版本';

COMMENT ON COLUMN public.whisper_preferences.active_nickname_version IS '当前可选已审昵称版本';

COMMENT ON COLUMN public.whisper_preferences.candidate_nickname_version IS '当前候选昵称版本，不替换已批准昵称';

COMMENT ON COLUMN public.whisper_preferences.next_nickname_version IS '下一次分配的不可复用昵称版本';

CREATE TABLE public.whisper_nickname_versions (
    account_id uuid NOT NULL REFERENCES public.accounts(id),
    nickname_version bigint NOT NULL CHECK (nickname_version > 0),
    content_registry_id uuid NOT NULL UNIQUE REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED,
    label_cipher jsonb NOT NULL CHECK (label_cipher IS NULL OR jsonb_typeof(label_cipher) = 'object'),
    public_label text CHECK (public_label IS NULL OR (char_length(public_label) BETWEEN 2 AND 20 AND public_label ~ '[^[:space:]]')),
    review_state text NOT NULL DEFAULT 'scanning' CHECK (review_state IN ('draft','scanning','pending','approved','rejected','cancelled')),
    control_version bigint NOT NULL DEFAULT 1 CHECK (control_version >= 1),
    review_round bigint NOT NULL DEFAULT 0 CHECK (review_round >= 0),
    review_task_id uuid REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id,nickname_version),
    CHECK (public_label IS NULL OR review_state='approved')
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

CREATE TABLE public.whisper_posts (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES public.accounts(id),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    current_content_version bigint NOT NULL DEFAULT 1 CHECK (current_content_version >= 1),
    published_content_version bigint CHECK (published_content_version > 0),
    lifecycle text NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','withdrawn')),
    review_state text NOT NULL DEFAULT 'draft' CHECK (review_state IN ('draft','scanning','pending','approved','rejected','cancelled')),
    publish_intent boolean NOT NULL DEFAULT false,
    comments_open boolean NOT NULL DEFAULT true,
    comments_epoch bigint NOT NULL DEFAULT 1 CHECK (comments_epoch >= 1),
    privacy_epoch bigint NOT NULL DEFAULT 0 CHECK (privacy_epoch >= 0),
    restriction_epoch bigint NOT NULL DEFAULT 0 CHECK (restriction_epoch >= 0),
    review_round bigint NOT NULL DEFAULT 0 CHECK (review_round >= 0),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at timestamptz ,
    CHECK (published_content_version IS NULL OR (published_content_version=current_content_version AND published_at IS NOT NULL AND review_state='approved' AND lifecycle='active' AND publish_intent))
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

CREATE TABLE public.whisper_post_versions (
    post_id uuid NOT NULL REFERENCES public.whisper_posts(id),
    content_version bigint NOT NULL CHECK (content_version > 0),
    content_registry_id uuid NOT NULL UNIQUE REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED,
    body_cipher jsonb NOT NULL CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    candidate_cipher jsonb CHECK (candidate_cipher IS NULL OR jsonb_typeof(candidate_cipher) = 'object'),
    identity_mode text NOT NULL CHECK (identity_mode IN ('anonymous','semi','named')),
    identity_cipher jsonb NOT NULL CHECK (identity_cipher IS NULL OR jsonb_typeof(identity_cipher) = 'object'),
    nickname_version bigint CHECK (nickname_version > 0),
    dictionary_version bigint CHECK (dictionary_version > 0),
    review_state text NOT NULL DEFAULT 'draft' CHECK (review_state IN ('draft','scanning','pending','approved','rejected','cancelled')),
    review_task_id uuid REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED,
    public_identity jsonb CHECK (public_identity IS NULL OR jsonb_typeof(public_identity) = 'object'),
    public_body text CHECK (public_body IS NULL OR (char_length(public_body) BETWEEN 1 AND 500 AND public_body ~ '[^[:space:]]')),
    school_related boolean,
    notice_codes jsonb CHECK (notice_codes IS NULL OR jsonb_typeof(notice_codes) = 'array'),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (post_id,content_version),
    CHECK ((identity_mode='named') = (nickname_version IS NOT NULL)),
    CHECK ((identity_mode='semi') = (dictionary_version IS NOT NULL)),
    CHECK ((public_body IS NULL AND public_identity IS NULL) OR (public_body IS NOT NULL AND public_identity IS NOT NULL AND school_related IS NOT NULL AND review_state='approved'))
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

CREATE TABLE public.whisper_aliases (
    post_id uuid NOT NULL REFERENCES public.whisper_posts(id),
    actor_id uuid NOT NULL REFERENCES public.accounts(id),
    label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 64 AND label ~ '[^[:space:]]'),
    PRIMARY KEY (post_id,actor_id)
);

COMMENT ON TABLE public.whisper_aliases IS '帖子上下文随机别名，跨帖子独立生成';

COMMENT ON COLUMN public.whisper_aliases.post_id IS '别名上下文帖子';

COMMENT ON COLUMN public.whisper_aliases.actor_id IS '内部参与账号，不输出别名行或账号标识';

COMMENT ON COLUMN public.whisper_aliases.label IS '独立随机展示别名，允许重名';

CREATE TABLE public.whisper_comments (
    id uuid PRIMARY KEY,
    post_id uuid REFERENCES public.whisper_posts(id),
    author_id uuid NOT NULL REFERENCES public.accounts(id),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    content_version bigint NOT NULL DEFAULT 1 CHECK (content_version >= 1),
    parent_content_version bigint NOT NULL DEFAULT 1 CHECK (parent_content_version >= 1),
    comments_epoch bigint NOT NULL DEFAULT 1 CHECK (comments_epoch >= 1),
    review_state text NOT NULL DEFAULT 'scanning' CHECK (review_state IN ('draft','scanning','pending','approved','rejected','cancelled')),
    lifecycle text NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','withdrawn')),
    privacy_epoch bigint NOT NULL DEFAULT 0 CHECK (privacy_epoch >= 0),
    restriction_epoch bigint NOT NULL DEFAULT 0 CHECK (restriction_epoch >= 0),
    review_round bigint NOT NULL DEFAULT 0 CHECK (review_round >= 0),
    published_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    recovery_cipher jsonb CHECK (recovery_cipher IS NULL OR jsonb_typeof(recovery_cipher) = 'object'),
    CHECK (post_id IS NOT NULL OR privacy_epoch > 0)
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

CREATE TABLE public.whisper_comment_versions (
    comment_id uuid NOT NULL REFERENCES public.whisper_comments(id),
    content_version bigint NOT NULL CHECK (content_version > 0),
    content_registry_id uuid NOT NULL UNIQUE REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED,
    body_cipher jsonb NOT NULL CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    candidate_cipher jsonb CHECK (candidate_cipher IS NULL OR jsonb_typeof(candidate_cipher) = 'object'),
    identity_mode text NOT NULL CHECK (identity_mode IN ('anonymous','semi','named')),
    identity_cipher jsonb NOT NULL CHECK (identity_cipher IS NULL OR jsonb_typeof(identity_cipher) = 'object'),
    nickname_version bigint CHECK (nickname_version > 0),
    dictionary_version bigint CHECK (dictionary_version > 0),
    review_state text NOT NULL DEFAULT 'scanning' CHECK (review_state IN ('draft','scanning','pending','approved','rejected','cancelled')),
    review_task_id uuid REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED,
    public_identity jsonb CHECK (public_identity IS NULL OR jsonb_typeof(public_identity) = 'object'),
    public_body text CHECK (public_body IS NULL OR (char_length(public_body) BETWEEN 1 AND 500 AND public_body ~ '[^[:space:]]')),
    school_related boolean,
    notice_codes jsonb CHECK (notice_codes IS NULL OR jsonb_typeof(notice_codes) = 'array'),
    review_result_cipher jsonb CHECK (review_result_cipher IS NULL OR jsonb_typeof(review_result_cipher) = 'object'),
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (comment_id,content_version),
    CHECK ((identity_mode='named') = (nickname_version IS NOT NULL)),
    CHECK ((identity_mode='semi') = (dictionary_version IS NOT NULL)),
    CHECK ((public_body IS NULL AND public_identity IS NULL) OR (public_body IS NOT NULL AND public_identity IS NOT NULL AND school_related IS NOT NULL AND review_state='approved'))
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

CREATE TABLE public.whisper_hugs (
    post_id uuid NOT NULL REFERENCES public.whisper_posts(id),
    actor_id uuid NOT NULL REFERENCES public.accounts(id),
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    PRIMARY KEY (post_id,actor_id)
);

COMMENT ON TABLE public.whisper_hugs IS '本人抱抱目标态及取消墓碑，防止旧请求重新点亮';

COMMENT ON COLUMN public.whisper_hugs.post_id IS '目标帖子';

COMMENT ON COLUMN public.whisper_hugs.actor_id IS '内部互动账号';

COMMENT ON COLUMN public.whisper_hugs.active IS '当前有效目标态，取消保留记录';

COMMENT ON COLUMN public.whisper_hugs.version IS '独立抱抱版本，不使用帖子版本或计数';

CREATE TABLE public.whisper_capsules (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES public.accounts(id),
    version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
    content_registry_id uuid NOT NULL UNIQUE REFERENCES public.content_registry(id) DEFERRABLE INITIALLY DEFERRED,
    body_cipher jsonb CHECK (body_cipher IS NULL OR jsonb_typeof(body_cipher) = 'object'),
    unlock_at timestamptz NOT NULL,
    review_state text NOT NULL DEFAULT 'scanning' CHECK (review_state IN ('draft','scanning','pending','approved','rejected','cancelled')),
    lifecycle text NOT NULL DEFAULT 'active' CHECK (lifecycle IN ('active','cancelled','deleted')),
    privacy_epoch bigint NOT NULL DEFAULT 0 CHECK (privacy_epoch >= 0),
    restriction_epoch bigint NOT NULL DEFAULT 0 CHECK (restriction_epoch >= 0),
    review_round bigint NOT NULL DEFAULT 0 CHECK (review_round >= 0),
    review_task_id uuid REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (unlock_at IS NOT NULL AND unlock_at = date_trunc('second',unlock_at)),
    CHECK ((lifecycle='active' AND body_cipher IS NOT NULL) OR (lifecycle IN ('cancelled','deleted') AND body_cipher IS NULL))
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

CREATE TABLE public.whisper_notification_refs (
    notification_id uuid PRIMARY KEY REFERENCES public.notifications(id),
    kind text NOT NULL CHECK (kind IN ('whisper_comment','whisper_capsule_unlocked')),
    source_id uuid NOT NULL,
    target_kind text NOT NULL CHECK (target_kind IN ('post','capsule')),
    target_id uuid NOT NULL,
    comment_id uuid ,
    UNIQUE (kind,source_id),
    CHECK ((kind='whisper_comment' AND target_kind='post' AND comment_id IS NOT NULL AND source_id=comment_id) OR (kind='whisper_capsule_unlocked' AND target_kind='capsule' AND comment_id IS NULL AND source_id=target_id))
);

COMMENT ON TABLE public.whisper_notification_refs IS '树洞站内通知技术引用及唯一事件键，不复制正文或身份';

COMMENT ON COLUMN public.whisper_notification_refs.notification_id IS '复用站内通知标识';

COMMENT ON COLUMN public.whisper_notification_refs.kind IS '通知事件类型';

COMMENT ON COLUMN public.whisper_notification_refs.source_id IS '首次公开评论或已解锁胶囊事件标识';

COMMENT ON COLUMN public.whisper_notification_refs.target_kind IS '受控跳转目标类型';

COMMENT ON COLUMN public.whisper_notification_refs.target_id IS '目标实体随机标识，回流必须重验';

COMMENT ON COLUMN public.whisper_notification_refs.comment_id IS '评论通知精确引用，胶囊通知为空';

ALTER TABLE public.whisper_post_versions ADD CONSTRAINT whisper_post_registry_version_fk FOREIGN KEY (content_registry_id,content_version) REFERENCES public.content_registry(id,content_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.whisper_comment_versions ADD CONSTRAINT whisper_comment_registry_version_fk FOREIGN KEY (content_registry_id,content_version) REFERENCES public.content_registry(id,content_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.whisper_nickname_versions ADD CONSTRAINT whisper_nickname_registry_version_fk FOREIGN KEY (content_registry_id,nickname_version) REFERENCES public.content_registry(id,content_version) DEFERRABLE INITIALLY DEFERRED;
-- 聚合与当前内容版本相互引用，允许同事务创建后于提交时复核。
ALTER TABLE public.whisper_posts ADD CONSTRAINT whisper_posts_current_fk FOREIGN KEY (id,current_content_version) REFERENCES public.whisper_post_versions(post_id,content_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.whisper_posts ADD CONSTRAINT whisper_posts_published_fk FOREIGN KEY (id,published_content_version) REFERENCES public.whisper_post_versions(post_id,content_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.whisper_comments ADD CONSTRAINT whisper_comments_current_fk FOREIGN KEY (id,content_version) REFERENCES public.whisper_comment_versions(comment_id,content_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.whisper_preferences ADD CONSTRAINT whisper_preferences_active_fk FOREIGN KEY (account_id,active_nickname_version) REFERENCES public.whisper_nickname_versions(account_id,nickname_version) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.whisper_preferences ADD CONSTRAINT whisper_preferences_candidate_fk FOREIGN KEY (account_id,candidate_nickname_version) REFERENCES public.whisper_nickname_versions(account_id,nickname_version) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX whisper_posts_public_idx ON public.whisper_posts(published_at DESC,id DESC) WHERE published_content_version IS NOT NULL;
CREATE INDEX whisper_posts_owner_idx ON public.whisper_posts(owner_id,created_at DESC,id DESC);
CREATE INDEX whisper_comments_public_idx ON public.whisper_comments(post_id,published_at,id) WHERE published_at IS NOT NULL;
CREATE INDEX whisper_comments_owner_idx ON public.whisper_comments(author_id,created_at DESC,id DESC);
CREATE INDEX whisper_nicknames_owner_idx ON public.whisper_nickname_versions(account_id,nickname_version DESC);
CREATE INDEX whisper_capsules_owner_idx ON public.whisper_capsules(owner_id,created_at DESC,id DESC);
CREATE INDEX whisper_capsules_due_idx ON public.whisper_capsules(unlock_at,id) WHERE lifecycle='active';
-- 采用现有账号级拉黑权威；默认值同时覆盖升级前仍在写入的旧客户端。
ALTER TABLE public.blocks ADD COLUMN block_id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE public.blocks ADD CONSTRAINT blocks_opaque_id_unique UNIQUE (block_id);
COMMENT ON COLUMN public.blocks.block_id IS '不透明随机黑名单条目标识，历史关系原位回填且不返回目标账号';
ALTER TABLE public.content_registry DROP CONSTRAINT content_registry_kind_check;
ALTER TABLE public.content_registry ADD CONSTRAINT content_registry_kind_check CHECK (kind IN ('wish','application','message','story','whisper_post','whisper_comment','whisper_nickname','whisper_capsule'));
ALTER TABLE public.reports DROP CONSTRAINT reports_target_kind_check;
ALTER TABLE public.reports ADD CONSTRAINT reports_target_kind_check CHECK (target_kind IN ('wish','application','message','story','account','whisper_post','whisper_comment'));
ALTER TABLE public.privacy_holds DROP CONSTRAINT privacy_holds_target_kind_check;
ALTER TABLE public.privacy_holds ADD CONSTRAINT privacy_holds_target_kind_check CHECK (target_kind IN ('wish','story','whisper_post','whisper_comment','whisper_nickname','whisper_capsule'));
ALTER TABLE evidence.crisis_cases DROP CONSTRAINT crisis_cases_content_type_check;
ALTER TABLE evidence.crisis_cases ADD CONSTRAINT crisis_cases_content_type_check CHECK (content_type IN ('wish','application','message','story','whisper_post','whisper_comment','whisper_nickname','whisper_capsule'));
ALTER TABLE evidence.crisis_cases ADD COLUMN identity_status text CHECK (identity_status IN ('verified','not_verified'));
COMMENT ON COLUMN evidence.crisis_cases.identity_status IS '检测时认证身份状态，未认证不伪造学号，历史未记录保持空';
