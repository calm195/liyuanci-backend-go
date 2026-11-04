-- 仅由迁移器在锁定并确认全部业务表为空后执行。
DROP TABLE public.resource_audits,
    public.resource_categories,
    public.resource_feedback,
    public.resource_materials;
