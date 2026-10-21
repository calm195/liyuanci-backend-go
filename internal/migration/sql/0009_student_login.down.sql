LOCK TABLE public.student_login_exchanges IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM public.student_login_exchanges) THEN
  RAISE EXCEPTION '学生登录回执非空，拒绝破坏性回退';
 END IF;
END $$;
DROP TABLE public.student_login_exchanges;
