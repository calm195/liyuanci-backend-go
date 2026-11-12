CREATE TABLE public.content_favorites (
 account_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
 target_kind text NOT NULL CHECK(target_kind IN ('wish','whisper_post','resource')),
 target_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(account_id,target_kind,target_id)
);
CREATE INDEX content_favorites_timeline ON public.content_favorites(account_id,created_at DESC,target_kind DESC,target_id DESC);
CREATE TABLE public.content_statistics (
 target_kind text NOT NULL CHECK(target_kind IN ('wish','whisper_post','resource')),
 target_id uuid NOT NULL,
 view_count bigint NOT NULL DEFAULT 0 CHECK(view_count>=0),
 PRIMARY KEY(target_kind,target_id)
);
INSERT INTO public.content_statistics(target_kind,target_id,view_count) SELECT 'wish',wish_id,count(*) FROM public.wish_detail_views GROUP BY wish_id;
CREATE TABLE public.content_view_events (
 event_id uuid PRIMARY KEY,
 target_kind text NOT NULL CHECK(target_kind IN ('wish','whisper_post','resource')),
 target_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX content_view_events_expiry ON public.content_view_events(created_at);
CREATE TABLE public.wish_comments (
 id uuid PRIMARY KEY,
 wish_id uuid NOT NULL REFERENCES public.wishes(id) ON DELETE CASCADE,
 author_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
 kind text NOT NULL DEFAULT 'comment' CHECK(kind IN ('comment','author_supplement')),
 root_comment_id uuid REFERENCES public.wish_comments(id) ON DELETE SET NULL,
 reply_to_comment_id uuid REFERENCES public.wish_comments(id) ON DELETE SET NULL,
 version bigint NOT NULL DEFAULT 1 CHECK(version>0),
 content_version bigint NOT NULL DEFAULT 1 CHECK(content_version>0),
 parent_content_version bigint NOT NULL CHECK(parent_content_version>0),
 comments_epoch bigint NOT NULL CHECK(comments_epoch>0),
 status text NOT NULL DEFAULT 'pending_scan' CHECK(status IN ('pending_scan','pending_review','published','rejected','withdrawn','cancelled')),
 published_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(root_comment_id IS NULL OR (root_comment_id<>id AND kind='comment')),
 CHECK(reply_to_comment_id IS NULL OR reply_to_comment_id<>id)
);
CREATE INDEX wish_comments_thread ON public.wish_comments(wish_id,parent_content_version,root_comment_id,published_at,id);
CREATE INDEX wish_comments_owner ON public.wish_comments(author_id,created_at DESC,id DESC);
CREATE TABLE public.wish_comment_versions (
 comment_id uuid NOT NULL REFERENCES public.wish_comments(id) ON DELETE CASCADE,
 content_version bigint NOT NULL CHECK(content_version>0),
 content_registry_id uuid NOT NULL UNIQUE REFERENCES public.content_registry(id),
 body_cipher jsonb NOT NULL,
 public_body text,
 review_task_id uuid NOT NULL REFERENCES public.moderation_tasks(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(comment_id,content_version)
);
CREATE TABLE public.content_comment_notices (
 comment_id uuid NOT NULL REFERENCES public.wish_comments(id) ON DELETE CASCADE,
 recipient_id uuid NOT NULL REFERENCES public.accounts(id) ON DELETE CASCADE,
 notification_id uuid NOT NULL REFERENCES public.notifications(id) ON DELETE CASCADE,
 PRIMARY KEY(comment_id,recipient_id)
);
ALTER TABLE public.whisper_comments ADD COLUMN root_comment_id uuid REFERENCES public.whisper_comments(id) ON DELETE SET NULL;
ALTER TABLE public.whisper_comments ADD COLUMN reply_to_comment_id uuid REFERENCES public.whisper_comments(id) ON DELETE SET NULL;
ALTER TABLE public.whisper_comments ADD CONSTRAINT whisper_comments_thread_shape CHECK((root_comment_id IS NULL OR root_comment_id<>id) AND (reply_to_comment_id IS NULL OR reply_to_comment_id<>id));
CREATE INDEX whisper_comments_thread ON public.whisper_comments(post_id,parent_content_version,root_comment_id,published_at,id);
ALTER TABLE public.whisper_notification_refs ADD COLUMN recipient_id uuid;
UPDATE public.whisper_notification_refs r SET recipient_id=n.recipient_id FROM public.notifications n WHERE n.id=r.notification_id;
ALTER TABLE public.whisper_notification_refs ALTER COLUMN recipient_id SET NOT NULL;
ALTER TABLE public.whisper_notification_refs ADD CONSTRAINT whisper_notification_refs_recipient_fk FOREIGN KEY(recipient_id) REFERENCES public.accounts(id) ON DELETE CASCADE;
ALTER TABLE public.whisper_notification_refs DROP CONSTRAINT whisper_notification_refs_kind_source_id_key;
ALTER TABLE public.whisper_notification_refs ADD CONSTRAINT whisper_notification_refs_event_recipient_key UNIQUE(kind,source_id,recipient_id);
ALTER TABLE public.wordlist_entries ADD COLUMN category text NOT NULL DEFAULT '';
ALTER TABLE public.wordlist_entries ADD COLUMN aliases jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(aliases)='array');
ALTER TABLE public.wordlist_entries ADD COLUMN match_mode text NOT NULL DEFAULT 'contains' CHECK(match_mode IN ('contains','word'));
ALTER TABLE public.content_registry DROP CONSTRAINT content_registry_kind_check;
ALTER TABLE public.content_registry ADD CONSTRAINT content_registry_kind_check CHECK(kind IN ('wish','wish_comment','application','message','story','whisper_post','whisper_comment','whisper_nickname','whisper_capsule'));
ALTER TABLE public.privacy_holds DROP CONSTRAINT privacy_holds_target_kind_check;
ALTER TABLE public.privacy_holds ADD CONSTRAINT privacy_holds_target_kind_check CHECK(target_kind IN ('wish','wish_comment','story','whisper_post','whisper_comment','whisper_nickname','whisper_capsule'));
COMMENT ON TABLE public.content_favorites IS '本人内容收藏引用';
COMMENT ON COLUMN public.content_favorites.account_id IS '收藏所属账号';
COMMENT ON COLUMN public.content_favorites.target_kind IS '内容固定种类';
COMMENT ON COLUMN public.content_favorites.target_id IS '目标内容引用';
COMMENT ON COLUMN public.content_favorites.created_at IS '首次设置收藏时间';
COMMENT ON TABLE public.content_statistics IS '无读者身份的累计浏览统计';
COMMENT ON COLUMN public.content_statistics.target_kind IS '内容固定种类';
COMMENT ON COLUMN public.content_statistics.target_id IS '内容引用';
COMMENT ON COLUMN public.content_statistics.view_count IS '累计有效详情打开次数';
COMMENT ON TABLE public.content_view_events IS '短期页面事件去重键';
COMMENT ON COLUMN public.content_view_events.event_id IS '页面打开的防重随机键';
COMMENT ON COLUMN public.content_view_events.target_kind IS '内容固定种类';
COMMENT ON COLUMN public.content_view_events.target_id IS '内容引用';
COMMENT ON COLUMN public.content_view_events.created_at IS '短期去重键创建时间';
COMMENT ON TABLE public.wish_comments IS '心愿评论和作者补充';
COMMENT ON COLUMN public.wish_comments.id IS '随机评论标识';
COMMENT ON COLUMN public.wish_comments.wish_id IS '所属心愿';
COMMENT ON COLUMN public.wish_comments.author_id IS '内部评论作者';
COMMENT ON COLUMN public.wish_comments.kind IS '普通评论或作者补充';
COMMENT ON COLUMN public.wish_comments.root_comment_id IS '楼内回复所属根评论';
COMMENT ON COLUMN public.wish_comments.reply_to_comment_id IS '被回复评论引用，不保存身份副本';
COMMENT ON COLUMN public.wish_comments.version IS '评论资源乐观版本';
COMMENT ON COLUMN public.wish_comments.content_version IS '当前正文版本';
COMMENT ON COLUMN public.wish_comments.parent_content_version IS '绑定心愿正文版本';
COMMENT ON COLUMN public.wish_comments.comments_epoch IS '绑定心愿评论代次';
COMMENT ON COLUMN public.wish_comments.status IS '评论审核和撤回状态';
COMMENT ON COLUMN public.wish_comments.published_at IS '当前正文公开时间';
COMMENT ON COLUMN public.wish_comments.created_at IS '首次创建时间';
COMMENT ON COLUMN public.wish_comments.updated_at IS '最近修改时间';
COMMENT ON TABLE public.wish_comment_versions IS '心愿评论的不可变原稿版本';
COMMENT ON COLUMN public.wish_comment_versions.comment_id IS '所属评论';
COMMENT ON COLUMN public.wish_comment_versions.content_version IS '不可变正文版本';
COMMENT ON COLUMN public.wish_comment_versions.content_registry_id IS '精确内容注册';
COMMENT ON COLUMN public.wish_comment_versions.body_cipher IS '原稿信封密文';
COMMENT ON COLUMN public.wish_comment_versions.public_body IS '当前经审核的公开正文';
COMMENT ON COLUMN public.wish_comment_versions.review_task_id IS '本版审核任务';
COMMENT ON COLUMN public.wish_comment_versions.created_at IS '版本创建时间';
COMMENT ON TABLE public.content_comment_notices IS '按接收人去重的评论通知事件';
COMMENT ON COLUMN public.content_comment_notices.comment_id IS '首次公开的心愿评论';
COMMENT ON COLUMN public.content_comment_notices.recipient_id IS '通知接收者';
COMMENT ON COLUMN public.content_comment_notices.notification_id IS '全站通知引用';
COMMENT ON COLUMN public.whisper_comments.root_comment_id IS '所属一级评论，一级为空';
COMMENT ON COLUMN public.whisper_comments.reply_to_comment_id IS '被回复评论，不保存昵称快照';
COMMENT ON COLUMN public.whisper_notification_refs.recipient_id IS '事件接收人，用于多人通知去重';
COMMENT ON COLUMN public.wordlist_entries.category IS '运营分类，不改变审核路由';
COMMENT ON COLUMN public.wordlist_entries.aliases IS '明确配置的拼音缩写和词条变体';
COMMENT ON COLUMN public.wordlist_entries.match_mode IS '包含匹配或词边界匹配';

ALTER TABLE evidence.crisis_cases DROP CONSTRAINT crisis_cases_content_type_check;
ALTER TABLE evidence.crisis_cases ADD CONSTRAINT crisis_cases_content_type_check CHECK(content_type IN ('wish','wish_comment','application','message','story','whisper_post','whisper_comment','whisper_nickname','whisper_capsule'));
CREATE FUNCTION public.fill_whisper_notification_recipient() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.recipient_id IS NULL OR NEW.recipient_id='00000000-0000-0000-0000-000000000000'::uuid THEN
  SELECT recipient_id INTO NEW.recipient_id FROM public.notifications WHERE id=NEW.notification_id;
 END IF;
 RETURN NEW;
END;
$$;
COMMENT ON FUNCTION public.fill_whisper_notification_recipient() IS '为兼容旧通知写入在同事务补齐当前通知接收者';
CREATE TRIGGER whisper_notification_recipient BEFORE INSERT ON public.whisper_notification_refs FOR EACH ROW EXECUTE FUNCTION public.fill_whisper_notification_recipient();
ALTER TABLE public.reports DROP CONSTRAINT reports_target_kind_check;
ALTER TABLE public.reports ADD CONSTRAINT reports_target_kind_check CHECK(target_kind IN ('wish','wish_comment','application','message','story','account','whisper_post','whisper_comment'));
