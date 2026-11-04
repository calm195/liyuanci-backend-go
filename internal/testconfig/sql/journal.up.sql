-- 恢复日志只部署到独立数据库；实体标识与删除范围说明封装在信封密文中。
CREATE TABLE public.recovery_delete_journal (
    record_no bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    intent_id uuid NOT NULL UNIQUE,
    deletion_scope text NOT NULL CHECK (deletion_scope IN ('account', 'object', 'evidence')),
    entry_envelope jsonb NOT NULL CHECK (jsonb_typeof(entry_envelope) = 'object'),
    happened_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    confirmed_at timestamptz CHECK (confirmed_at >= happened_at)
);
COMMENT ON TABLE public.recovery_delete_journal IS '独立恢复重删意图日志，禁止同业务备份一起回滚，不替代普通审计';
COMMENT ON COLUMN public.recovery_delete_journal.record_no IS '独立日志单调技术序号，不用于聊天送达序号';
COMMENT ON COLUMN public.recovery_delete_journal.intent_id IS '随机删除意图标识，用于重复写入去重';
COMMENT ON COLUMN public.recovery_delete_journal.deletion_scope IS '技术删除范围，不包含身份或正文';
COMMENT ON COLUMN public.recovery_delete_journal.entry_envelope IS '随机实体及对象标识的信封密文，禁止明文身份、正文或浏览记录';
COMMENT ON COLUMN public.recovery_delete_journal.happened_at IS '删除意图持久化的技术时间';
COMMENT ON COLUMN public.recovery_delete_journal.confirmed_at IS '对应删除工作确认时间，非恢复开放授权';
