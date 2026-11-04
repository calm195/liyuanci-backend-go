-- 迁移器技术账本必须先于版本目录创建；不属于业务表模型推导。
CREATE TABLE IF NOT EXISTS public.schema_migrations (version bigint PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP);
COMMENT ON TABLE public.schema_migrations IS '显式数据库迁移版本及文件完整性账本';
COMMENT ON COLUMN public.schema_migrations.version IS '已成功应用的迁移版本';
COMMENT ON COLUMN public.schema_migrations.checksum IS '对应上下行迁移文件的 SHA-256 校验和';
COMMENT ON COLUMN public.schema_migrations.applied_at IS '迁移事务应用时间';
