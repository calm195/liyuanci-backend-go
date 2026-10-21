-- 仍有意图记录时拒绝回退，不能用迁移跳过备份与消息窗口确认。
LOCK TABLE public.recovery_delete_journal IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM public.recovery_delete_journal) THEN
        RAISE EXCEPTION '恢复删除日志非空，拒绝回退';
    END IF;
END $$;
DROP TABLE public.recovery_delete_journal;
