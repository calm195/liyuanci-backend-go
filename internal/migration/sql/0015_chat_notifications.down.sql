DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.messages WHERE review_task_id IS NOT NULL OR peer_epoch IS NOT NULL OR request_digest IS NOT NULL OR client_message_id !~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') OR EXISTS (SELECT 1 FROM public.notifications WHERE version<>1) OR EXISTS (SELECT 1 FROM public.conversations WHERE version<>1) THEN
 RAISE EXCEPTION '聊天或通知存在新版业务数据，拒绝有损回滚';
 END IF;
END $$;
ALTER TABLE public.messages DROP COLUMN review_task_id, DROP COLUMN peer_epoch, DROP COLUMN request_digest;
ALTER TABLE public.messages ALTER COLUMN client_message_id TYPE uuid USING client_message_id::uuid;
COMMENT ON COLUMN public.messages.client_message_id IS '客户端随机幂等标识';
ALTER TABLE public.notifications DROP COLUMN version;
ALTER TABLE public.conversations DROP COLUMN version;
