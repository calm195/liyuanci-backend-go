-- 仅由迁移器在锁定并确认全部业务表为空后执行。
DROP TABLE public.command_receipts,
    public.consumer_receipts,
    public.delivery_attempts,
    public.notifications,
    public.outbox_events,
    public.work_items;
