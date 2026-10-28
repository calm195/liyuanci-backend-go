-- 不销毁持久挂起状态来回退旧消费者；须先按业务生命周期处理待办。
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.work_items WHERE status='waiting') THEN
  RAISE EXCEPTION '存在挂起的胶囊解锁工作，拒绝破坏性回退';
 END IF;
END $$;
DROP INDEX public.whisper_capsule_unlock_unique;
ALTER TABLE public.work_items DROP CONSTRAINT work_items_status_check;
ALTER TABLE public.work_items ADD CONSTRAINT work_items_status_check CHECK (status IN ('pending','leased','retry_wait','completed','cancelled','attention'));
COMMENT ON COLUMN public.work_items.status IS '工作状态，异常待办不能绕过取消';
