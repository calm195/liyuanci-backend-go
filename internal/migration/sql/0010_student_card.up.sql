ALTER TABLE public.assets ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0);
ALTER TABLE public.assets ADD COLUMN scan_status text NOT NULL DEFAULT 'pending' CHECK (scan_status IN ('pending','approved','rejected'));
COMMENT ON COLUMN public.assets.version IS '学生卡处理版本，用于锁后状态复核';
COMMENT ON COLUMN public.assets.scan_status IS '图片安全状态，pending待处理、approved通过或rejected拒绝，不代表学生认证';
CREATE TABLE public.student_card_payloads (
 asset_id uuid PRIMARY KEY REFERENCES public.assets(id) ON DELETE CASCADE,
 envelope jsonb NOT NULL CHECK (jsonb_typeof(envelope)='object'),
 mime text NOT NULL CHECK (mime IN ('image/png','image/jpeg'))
);
COMMENT ON TABLE public.student_card_payloads IS '学生卡待外部持久化的短期信封密文，扫描终结后移除，不存图片明文';
COMMENT ON COLUMN public.student_card_payloads.asset_id IS '对应学生卡对象，物理清理时级联移除暂存密文';
COMMENT ON COLUMN public.student_card_payloads.envelope IS '学生卡图片的认证信封密文，不包含明文数据密钥';
COMMENT ON COLUMN public.student_card_payloads.mime IS '重新编码后的图片媒体类型';
