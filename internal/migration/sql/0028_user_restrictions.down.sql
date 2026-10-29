DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.penalties WHERE source_kind='direct' OR revoke_reason_cipher IS NOT NULL OR revoked_by IS NOT NULL) THEN
 RAISE EXCEPTION '存在直接处置记录，禁止回退丢失审计';
 END IF;
END $$;
ALTER TABLE public.penalties DROP CONSTRAINT direct_penalty_scope, DROP COLUMN revoked_by, DROP COLUMN revoke_reason_cipher, DROP COLUMN source_kind;
COMMENT ON COLUMN public.penalties.content_version IS '触发处置的精确内容版本';
