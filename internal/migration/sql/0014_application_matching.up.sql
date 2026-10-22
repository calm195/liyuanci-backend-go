ALTER TABLE public.applications
 ADD COLUMN review_task_id uuid REFERENCES public.moderation_tasks(id),
 ADD COLUMN owner_epoch bigint CHECK(owner_epoch>0),
 ADD COLUMN public_message_cipher jsonb CHECK(public_message_cipher IS NULL OR jsonb_typeof(public_message_cipher)='object');
COMMENT ON COLUMN public.applications.review_task_id IS '当前留言审核任务，旧审核不得恢复已撤销或失效申请';
COMMENT ON COLUMN public.applications.owner_epoch IS '申请时许愿人权限代次，历史空值不能作为生效授权';
COMMENT ON COLUMN public.applications.public_message_cipher IS '审核允许的脱敏留言密文，仅当前有效申请向许愿人解密';
ALTER TABLE public.applications ADD CONSTRAINT applications_projection_status CHECK(public_message_cipher IS NULL OR status IN ('active','accepted'));
ALTER TABLE public.matches
 ADD COLUMN application_id uuid REFERENCES public.applications(id),
 ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK(version>0);
COMMENT ON COLUMN public.matches.application_id IS '形成匹配的精确申请，历史空值不推断来源';
COMMENT ON COLUMN public.matches.version IS '匹配关系自身的乐观版本，不是心愿或申请版本';
CREATE UNIQUE INDEX matches_application_unique ON public.matches(application_id) WHERE application_id IS NOT NULL;
CREATE INDEX applications_review_task_idx ON public.applications(review_task_id) WHERE review_task_id IS NOT NULL;
