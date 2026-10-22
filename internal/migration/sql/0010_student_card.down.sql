LOCK TABLE public.assets,public.student_card_payloads IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM public.student_card_payloads) OR EXISTS(SELECT 1 FROM public.work_items WHERE kind='student_card.scan') OR EXISTS(SELECT 1 FROM public.assets WHERE version<>1 OR scan_status<>'pending') THEN
  RAISE EXCEPTION '学生卡处理记录非空，拒绝破坏性回退';
 END IF;
END $$;
DROP TABLE public.student_card_payloads;
ALTER TABLE public.assets DROP COLUMN scan_status;
ALTER TABLE public.assets DROP COLUMN version;
