ALTER TABLE public.wishes DROP CONSTRAINT wishes_duration_days_check;
ALTER TABLE public.wishes ADD CONSTRAINT wishes_duration_days_check CHECK (duration_days BETWEEN 1 AND 90);
ALTER TABLE public.wishes
 ADD COLUMN first_published_at timestamptz,
 ADD COLUMN publish_intent boolean NOT NULL DEFAULT false,
 ADD COLUMN identity_mode text NOT NULL DEFAULT 'named' CHECK (identity_mode IN ('named','semi')),
 ADD COLUMN comments_open boolean NOT NULL DEFAULT true,
 ADD COLUMN comments_epoch bigint NOT NULL DEFAULT 1 CHECK (comments_epoch > 0);
COMMENT ON COLUMN public.wishes.first_published_at IS '新规则首次公开时间，存量未知保持空';
COMMENT ON COLUMN public.wishes.publish_intent IS '当前版本审核后自动上架的明确意愿';
COMMENT ON COLUMN public.wishes.identity_mode IS '公开身份模式，首次公开后固定';
COMMENT ON COLUMN public.wishes.comments_open IS '是否允许新评论及审核公开';
COMMENT ON COLUMN public.wishes.comments_epoch IS '评论上下文代次，改稿或撤回后递增';
COMMENT ON COLUMN public.wishes.duration_days IS '有效期整数天数，默认七天，允许一至九十天';
CREATE TABLE public.wish_images (
 id uuid PRIMARY KEY,
 owner_id uuid REFERENCES public.accounts(id) ON DELETE SET NULL,
 object_key text NOT NULL UNIQUE CHECK (object_key = 'wish-images/' || id::text),
 object_version text NOT NULL DEFAULT '',
 byte_size bigint NOT NULL CHECK (byte_size BETWEEN 1 AND 4194304),
 width integer NOT NULL CHECK (width > 20),
 height integer NOT NULL CHECK (height > 20),
 scan_state text NOT NULL DEFAULT 'pending' CHECK (scan_state IN ('pending','approved','rejected')),
 upload_cipher jsonb,
 upload_until timestamptz,
 removable_after timestamptz NOT NULL,
 cleanup_started_at timestamptz,
 installed_at timestamptz,
 removed_at timestamptz
);
COMMENT ON TABLE public.wish_images IS '心愿图片私有加密对象及可恢复上传和清理状态';
COMMENT ON COLUMN public.wish_images.id IS '图片随机标识';
COMMENT ON COLUMN public.wish_images.owner_id IS '上传账号，注销后清空并保留对象清理引用';
COMMENT ON COLUMN public.wish_images.object_key IS '独立心愿图片命名空间对象键';
COMMENT ON COLUMN public.wish_images.object_version IS '私有密文对象完整性版本';
COMMENT ON COLUMN public.wish_images.byte_size IS '规范化PNG明文字节数';
COMMENT ON COLUMN public.wish_images.width IS '规范化图片像素宽度';
COMMENT ON COLUMN public.wish_images.height IS '规范化图片像素高度';
COMMENT ON COLUMN public.wish_images.scan_state IS '图片安全扫描状态';
COMMENT ON COLUMN public.wish_images.upload_cipher IS '待上传的稳定密文，完成后清空';
COMMENT ON COLUMN public.wish_images.upload_until IS '外部上传排他预约期限';
COMMENT ON COLUMN public.wish_images.removable_after IS '未引用图片允许清理的最早时间';
COMMENT ON COLUMN public.wish_images.cleanup_started_at IS '对象已被清理器认领，禁止重新引用';
COMMENT ON COLUMN public.wish_images.installed_at IS '审核及上传完成时间';
COMMENT ON COLUMN public.wish_images.removed_at IS '外部对象删除确认时间';
CREATE INDEX wish_images_cleanup ON public.wish_images(removable_after,id) WHERE removed_at IS NULL;
CREATE INDEX wish_images_owner ON public.wish_images(owner_id,id);
CREATE TABLE public.wish_version_images (
 wish_id uuid NOT NULL,
 content_version bigint NOT NULL,
 image_id uuid NOT NULL REFERENCES public.wish_images(id),
 position smallint NOT NULL CHECK (position BETWEEN 0 AND 2),
 PRIMARY KEY(wish_id,content_version,image_id),
 UNIQUE(wish_id,content_version,position),
 FOREIGN KEY(wish_id,content_version) REFERENCES public.wish_versions(wish_id,content_version) ON DELETE CASCADE
);
COMMENT ON TABLE public.wish_version_images IS '心愿精确正文版本的有序图片关联，最多三张';
COMMENT ON COLUMN public.wish_version_images.wish_id IS '所属心愿';
COMMENT ON COLUMN public.wish_version_images.content_version IS '所属精确正文版本';
COMMENT ON COLUMN public.wish_version_images.image_id IS '已审核的本人图片';
COMMENT ON COLUMN public.wish_version_images.position IS '展示顺序，零至二';
CREATE INDEX wish_version_images_image ON public.wish_version_images(image_id);
ALTER TABLE public.wish_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.wish_images FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.wish_images USING (true) WITH CHECK (true);
ALTER TABLE public.wish_version_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.wish_version_images FORCE ROW LEVEL SECURITY;
CREATE POLICY application_account ON public.wish_version_images USING (true) WITH CHECK (true);
REVOKE ALL ON public.wish_images, public.wish_version_images FROM PUBLIC;
