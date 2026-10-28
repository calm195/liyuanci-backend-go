-- 胶囊到期而审核未完成时持久挂起，不消耗错误重试次数，也不反复唤醒。
ALTER TABLE public.work_items DROP CONSTRAINT work_items_status_check;
ALTER TABLE public.work_items ADD CONSTRAINT work_items_status_check CHECK (status IN ('pending','leased','retry_wait','completed','cancelled','attention') OR (status='waiting' AND kind='whisper_capsule_unlock'));
COMMENT ON COLUMN public.work_items.status IS '工作状态；胶囊解锁waiting等待安全批准事件，异常attention仍需独立处理';
CREATE UNIQUE INDEX whisper_capsule_unlock_unique ON public.work_items(target_id) WHERE kind='whisper_capsule_unlock';
COMMENT ON INDEX public.whisper_capsule_unlock_unique IS '每个本人胶囊只有一个持久解锁工作，取消和终结后仍保留去重标识';
