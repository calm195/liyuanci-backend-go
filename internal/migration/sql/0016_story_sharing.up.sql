ALTER TABLE public.wishes ADD COLUMN current_story_version bigint NOT NULL DEFAULT 0 CHECK (current_story_version>=0);
COMMENT ON COLUMN public.wishes.current_story_version IS '当前还愿故事内容版本，零表示尚未提交；故事命令使用心愿聚合版本';
ALTER TABLE public.story_versions ADD COLUMN review_task_id uuid REFERENCES public.moderation_tasks(id), ADD COLUMN identity_tags jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(identity_tags)='object'), ADD COLUMN wordlist_version uuid REFERENCES public.wordlist_versions(id), ADD COLUMN source_match_id uuid, ADD COLUMN peer_epoch bigint, ADD COLUMN parent_hold_epoch bigint NOT NULL DEFAULT 0 CHECK (parent_hold_epoch>=0);
COMMENT ON COLUMN public.story_versions.review_task_id IS '精确送审任务引用，旧版本审核不能覆盖新稿';
COMMENT ON COLUMN public.story_versions.identity_tags IS '经审核允许的最小公开身份标签，不含应愿人身份';
COMMENT ON COLUMN public.story_versions.wordlist_version IS '扫描采用的普通违规词库版本';
COMMENT ON COLUMN public.story_versions.source_match_id IS '提交时原匹配技术引用，隐私重审可清空，不作为永久身份存档';
COMMENT ON COLUMN public.story_versions.peer_epoch IS '提交时原应愿人权限代次，变化后旧审核不得生效';
COMMENT ON COLUMN public.story_versions.parent_hold_epoch IS '送审时父心愿隐私屏障代次，不能用故事重审释放父内容屏障';
CREATE TABLE public.share_links (
 id uuid PRIMARY KEY,
 target_kind text NOT NULL CHECK (target_kind IN ('wish','story')),
 target_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
COMMENT ON TABLE public.share_links IS '分享技术引用，不保存正文或身份快照，每次打开重验当前公开资格';
COMMENT ON COLUMN public.share_links.id IS '随机不透明分享引用';
COMMENT ON COLUMN public.share_links.target_kind IS '受控目标类型';
COMMENT ON COLUMN public.share_links.target_id IS '稳定内容标识，不持有身份外键';
COMMENT ON COLUMN public.share_links.created_at IS '技术引用创建时间';
