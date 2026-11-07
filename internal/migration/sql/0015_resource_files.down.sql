DROP FUNCTION public.resource_feedback_summary(uuid[]);
ALTER TABLE public.resource_materials DROP COLUMN file_id;
DROP TABLE public.resource_files;
COMMENT ON TABLE public.resource_feedback IS '资料当前内容版本学生反馈';
