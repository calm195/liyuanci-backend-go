#!/bin/sh
# 仅聚合检查，不输出凭据、任务标识或个人正文；通知由正式告警渠道接收失败状态。
set -eu
fail=0
for service in api card content whispers account evidence postgres redis; do
    if ! systemctl is-active --quiet "liyuanci-$service.service"; then
        printf '%s\n' "服务未运行：$service"
        fail=1
    fi
done
if ! curl --fail --silent --output /dev/null --max-time 15 http://127.0.0.1:18081/readyz; then
    printf '%s\n' 'API依赖未就绪'
    fail=1
fi
# 只监测专用数据所在文件系统；不扫描其他业务文件。
if ! df -P /var/lib/liyuanci | awk 'NR==2 {gsub(/%/,"",$5); exit ($5>=80)}'; then
    printf '%s\n' '数据磁盘使用率达到80%'
    fail=1
fi
if ! awk '/MemTotal:/{total=$2} /MemAvailable:/{available=$2} END{exit (available<total*0.15)}' /proc/meminfo; then
    printf '%s\n' '宿主可用内存低于15%'
    fail=1
fi
# PG*通过只读monitor.env/pgpass配置；角色仅SELECT两张任务表。
if ! pending=$(psql -X -w -At -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.work_items WHERE status='attention' OR (status IN ('pending','retry_wait') AND next_at<clock_timestamp()-interval '5 minutes') OR (status='leased' AND lease_until<clock_timestamp()-interval '5 minutes');" 2>/dev/null); then
    printf '%s\n' '数据库积压检查失败'
    fail=1
elif [ "$pending" -gt 0 ]; then
    printf '待处理异常任务数：%s\n' "$pending"
    fail=1
fi
if ! pending=$(psql -X -w -At -v ON_ERROR_STOP=1 -c "SELECT count(*) FROM public.deletion_jobs WHERE phase<>'completed' AND next_at<clock_timestamp()-interval '5 minutes';" 2>/dev/null); then
    printf '%s\n' '隐私待办检查失败'
    fail=1
elif [ "$pending" -gt 0 ]; then
    printf '超时隐私待办数：%s\n' "$pending"
    fail=1
fi
printf '轻量检查完成：result=%s\n' "$fail"
exit "$fail"
