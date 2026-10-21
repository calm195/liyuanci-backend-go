-- 重驱动只替代唤醒事件，保留工作和操作标识，避免重置租约代次导致旧持有者误确认。
ALTER TABLE public.outbox_events ADD COLUMN superseded_at timestamptz;
COMMENT ON COLUMN public.outbox_events.superseded_at IS '被已授权重驱动替代的技术时间，非空后禁止领取、确认和消费旧唤醒';
CREATE INDEX outbox_events_work_active_idx ON public.outbox_events(work_id, event_type, created_at, event_id) WHERE superseded_at IS NULL;
COMMENT ON INDEX public.outbox_events_work_active_idx IS '数据库补偿及重驱动读取当前工作唤醒关联';
