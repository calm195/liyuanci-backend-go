ALTER TABLE public.story_versions ADD COLUMN published_at timestamptz;
COMMENT ON COLUMN public.story_versions.published_at IS '当前故事版本审核公开时间；历史已审版本无法还原原公开时间，使用迁移切换时间作为排序起点';
-- 历史审批没有持久化完成时间，明确使用切换时间，不把提交时间冒充公开时间。
UPDATE public.story_versions SET published_at=CURRENT_TIMESTAMP WHERE moderation_status='approved' AND public_body IS NOT NULL;
