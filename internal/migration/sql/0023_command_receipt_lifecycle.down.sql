LOCK TABLE public.command_receipts IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.command_receipts WHERE lifecycle_bound) THEN
  RAISE EXCEPTION '存在生命周期回执，拒绝回退';
 END IF;
END $$;
ALTER TABLE public.command_receipts DROP COLUMN lifecycle_bound;
