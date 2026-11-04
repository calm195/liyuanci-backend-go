-- 仅由迁移器在锁定并确认全部业务表为空后执行。
DROP TABLE public.applications,
    public.conversations,
    public.matches,
    public.messages,
    public.share_links,
    public.story_versions,
    public.wish_versions,
    public.wishes;
