CREATE TABLE public.resource_files (
 id uuid PRIMARY KEY,
 owner_id uuid NOT NULL REFERENCES public.admin_accounts(id),
 object_key text NOT NULL UNIQUE CHECK (object_key = 'resources/' || id::text),
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
 content_type text NOT NULL,
 byte_size bigint NOT NULL CHECK (byte_size BETWEEN 1 AND 20971520),
 sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
 ready boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,
 removable_after timestamptz NOT NULL,
 upload_until timestamptz,
 cleanup_started_at timestamptz,
 removed_at timestamptz
);
COMMENT ON TABLE public.resource_files IS '资料私有文件及上传清理状态';
COMMENT ON COLUMN public.resource_files.id IS '资料文件标识';
COMMENT ON COLUMN public.resource_files.owner_id IS '上传管理员';
COMMENT ON COLUMN public.resource_files.object_key IS '私有对象键';
COMMENT ON COLUMN public.resource_files.name IS '原始文件名称';
COMMENT ON COLUMN public.resource_files.content_type IS '文件媒体类型';
COMMENT ON COLUMN public.resource_files.byte_size IS '文件字节数';
COMMENT ON COLUMN public.resource_files.sha256 IS '文件内容摘要';
COMMENT ON COLUMN public.resource_files.ready IS '上传完整性已确认';
COMMENT ON COLUMN public.resource_files.created_at IS '上传预约时间';
COMMENT ON COLUMN public.resource_files.removable_after IS '允许清理未引用文件的时间';
COMMENT ON COLUMN public.resource_files.upload_until IS '上传预约到期时间';
COMMENT ON COLUMN public.resource_files.cleanup_started_at IS '清理开始时间，禁止再次绑定';
COMMENT ON COLUMN public.resource_files.removed_at IS '对象已确认删除时间';
CREATE INDEX resource_files_cleanup ON public.resource_files(removable_after,id) WHERE removed_at IS NULL;
ALTER TABLE public.resource_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.resource_materials ADD COLUMN file_id uuid REFERENCES public.resource_files(id);
COMMENT ON COLUMN public.resource_materials.file_id IS '当前资料文件引用，空时使用外部下载链接';

COMMENT ON TABLE public.resource_feedback IS '资料各内容版本学生反馈，列表仅统计当前版本';

-- 仅返回聚合计数，受限学生连接无需读取其他人的反馈明细。
CREATE FUNCTION public.resource_feedback_summary(ids uuid[])
RETURNS TABLE(resource_id uuid,kind text,count bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public
AS $$
 SELECT f.resource_id,f.kind,count(*)
 FROM public.resource_feedback f
 JOIN public.accounts a ON a.id=f.account_id AND a.lifecycle='active'
 JOIN public.resource_materials r ON r.id=f.resource_id AND r.content_version=f.content_version
 WHERE f.resource_id=ANY(ids)
 GROUP BY f.resource_id,f.kind
$$;
COMMENT ON FUNCTION public.resource_feedback_summary(uuid[]) IS '聚合当前资料版本活跃账号反馈，不返回反馈主体';
