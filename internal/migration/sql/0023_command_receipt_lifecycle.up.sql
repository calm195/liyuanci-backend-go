-- 与主体／资源生命周期绑定的最小回执，不能按技术期限覆盖为新命令。
ALTER TABLE public.command_receipts ADD COLUMN lifecycle_bound boolean NOT NULL DEFAULT false;
COMMENT ON COLUMN public.command_receipts.lifecycle_bound IS '回执绑定业务幂等生命周期，技术到期后仍重验当前访问，不覆盖或按期清理';
