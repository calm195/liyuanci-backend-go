ALTER TABLE public.penalties ADD COLUMN source_kind text NOT NULL DEFAULT 'case' CHECK (source_kind IN ('case','direct')),
 ADD COLUMN revoke_reason_cipher jsonb, ADD COLUMN revoked_by uuid;
COMMENT ON COLUMN public.penalties.source_kind IS '处罚来源：case案件或direct管理员直接处置；直接处置不关联内容';
COMMENT ON COLUMN public.penalties.revoke_reason_cipher IS '管理员直接解除理由信封密文，绑定处罚标识，随处罚隐私清理';
COMMENT ON COLUMN public.penalties.revoked_by IS '直接解除的后台操作者技术标识，不存身份正文';
COMMENT ON COLUMN public.penalties.content_version IS '案件精确内容版本；direct来源固定为1且content_id为空，不代表存在内容';
ALTER TABLE public.penalties ADD CONSTRAINT direct_penalty_scope CHECK (source_kind<>'direct' OR (content_id IS NULL AND content_version=1 AND action_kind IN ('mute','ban')));
