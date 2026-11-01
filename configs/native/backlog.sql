-- 只输出聚合计数，不输出任务标识、主体、SQL参数或业务正文。
BEGIN READ ONLY;
SET LOCAL statement_timeout = '5s';
SELECT kind,
 count(*) FILTER (WHERE status IN ('pending','retry_wait') AND next_at<=clock_timestamp()
                 OR status='leased' AND lease_until<=clock_timestamp() AND next_at<=clock_timestamp()) AS due,
 count(*) FILTER (WHERE status='attention') AS attention,
 count(*) FILTER (WHERE status='waiting') AS waiting,
 count(*) FILTER (WHERE status IN ('pending','retry_wait') AND next_at>clock_timestamp()) AS scheduled,
 COALESCE(max(EXTRACT(EPOCH FROM clock_timestamp()-next_at)) FILTER (
   WHERE status IN ('pending','retry_wait') AND next_at<=clock_timestamp()
      OR status='leased' AND lease_until<=clock_timestamp() AND next_at<=clock_timestamp()),0)::bigint AS oldest_due_seconds
FROM public.work_items GROUP BY kind ORDER BY kind;
SELECT scope, phase, count(*) AS jobs FROM public.deletion_jobs
WHERE phase<>'completed' GROUP BY scope,phase ORDER BY scope,phase;
-- postgres任务模式不发送MQ，sent_at为空不是积压指标；不得批量伪造sent_at或清除历史回执。
COMMIT;
