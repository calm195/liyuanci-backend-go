-- 仅由迁移器在锁定并确认全部业务表为空后执行。
DROP TABLE public.whisper_aliases,
    public.whisper_capsules,
    public.whisper_comment_versions,
    public.whisper_comments,
    public.whisper_hugs,
    public.whisper_nickname_versions,
    public.whisper_notification_refs,
    public.whisper_post_versions,
    public.whisper_posts,
    public.whisper_preferences;
