package adminauth

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// DashboardMetrics 是当前留存业务记录的运营汇总，不包含后台账号。
type DashboardMetrics struct {
	RegisteredUsers int64 `json:"registered_users"`
	VerifiedUsers   int64 `json:"verified_users"`
	NewUsers        int64 `json:"new_users"`
	Resources       int64 `json:"resources"`
	Wishes          int64 `json:"wishes"`
	FulfilledWishes int64 `json:"fulfilled_wishes"`
}

// DashboardPoint 按北京时间日期展示累计量，新增用户为该日或该月新增量。
type DashboardPoint struct {
	Date string `json:"date"`
	DashboardMetrics
}

// Dashboard 返回同一数据库快照的指标和历史曲线，明确历史重建边界。
type Dashboard struct {
	Range       string           `json:"range"`
	Timezone    string           `json:"timezone"`
	GeneratedAt time.Time        `json:"generated_at"`
	Summary     DashboardMetrics `json:"summary"`
	Series      []DashboardPoint `json:"series"`
	HistoryNote string           `json:"history_note"`
}

// ReadDashboard 只向当前有效后台身份返回聚合值，不返回个人资料。
func (s *Service) ReadDashboard(ctx context.Context, token, requestID, period string) (out Dashboard, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		if err != nil {
			out = Dashboard{}
		}
		s.complete("dashboard", requestID, start, err)
	}()
	if period != "month" && period != "year" {
		return out, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := s.lockTimeout(ctx, db); e != nil {
			return e
		}
		_, now, e := s.authorized(ctx, db, token)
		if e != nil {
			return e
		}
		out = Dashboard{Range: period, Timezone: "Asia/Shanghai", GeneratedAt: now, Series: []DashboardPoint{},
			HistoryNote: "统计基于当前留存记录：注册与认证仅含正常账号；资料含草稿、上架和下架；心愿不含已删除草稿。历史按创建、审核通过和首次还愿时间回溯，不含已注销或物理删除记录，不能视为当时完整快照。认证与达成曲线只包含有事件时间的记录，缺少时间的存量记录不纳入，末点可能小于当前总数。"}
		// 汇总与所有分桶合并在一条SQL，保证各曲线读取同一语句快照。
		var rows []struct {
			Date string
			DashboardMetrics
		}
		e = db.Raw(dashboardSQL, now, period).Scan(&rows).Error
		if e != nil {
			return e
		}
		for _, row := range rows {
			if row.Date == "summary" {
				out.Summary = row.DashboardMetrics
			} else {
				out.Series = append(out.Series, DashboardPoint{Date: row.Date, DashboardMetrics: row.DashboardMetrics})
			}
		}
		return nil
	})
	return out, err
}

// 时间上界使用半开区间；当日/当月截止请求时刻，不统计未来记录。
const dashboardSQL = `
WITH params AS (SELECT ?::timestamptz AS now, ?::text AS period),
bounds AS (
 SELECT now, period, date_trunc('day',now AT TIME ZONE 'Asia/Shanghai') AS today,
 date_trunc('month',now AT TIME ZONE 'Asia/Shanghai') AS month_start FROM params
), buckets AS (
 SELECT to_char(t,'YYYY-MM-DD') AS date,
 t AT TIME ZONE 'Asia/Shanghai' AS start_at,
 least((t + CASE WHEN period='month' THEN interval '1 day' ELSE interval '1 month' END) AT TIME ZONE 'Asia/Shanghai',now) AS end_at
 FROM bounds CROSS JOIN LATERAL generate_series(
 CASE WHEN period='month' THEN today-interval '29 days' ELSE month_start-interval '11 months' END,
 CASE WHEN period='month' THEN today ELSE month_start END,
 CASE WHEN period='month' THEN interval '1 day' ELSE interval '1 month' END) AS t
 UNION ALL SELECT 'summary',now-interval '30 days',now FROM params
), registered AS (
 SELECT id,created_at FROM public.accounts WHERE lifecycle='active'
), verified AS (
 SELECT a.id,min(r.reviewed_at) AS happened_at FROM public.accounts a
 JOIN public.identity_reviews r ON r.account_id=a.id AND r.status='approved'
 WHERE a.lifecycle='active' AND a.verification='verified' AND r.reviewed_at IS NOT NULL GROUP BY a.id
), wishes AS (
 SELECT id,created_at,state FROM public.wishes WHERE draft_deleted_at IS NULL
), fulfilled AS (
 SELECT w.id,min(s.created_at) AS happened_at FROM wishes w JOIN public.story_versions s ON s.wish_id=w.id
 WHERE w.state='fulfilled' GROUP BY w.id
)
SELECT b.date,
 (SELECT count(*) FROM registered a WHERE a.created_at<b.end_at) AS registered_users,
 CASE WHEN b.date='summary' THEN (SELECT count(*) FROM public.accounts WHERE lifecycle='active' AND verification='verified')
 ELSE (SELECT count(*) FROM verified v WHERE v.happened_at<b.end_at) END AS verified_users,
 (SELECT count(*) FROM registered a WHERE a.created_at>=b.start_at AND a.created_at<b.end_at) AS new_users,
 (SELECT count(*) FROM public.resource_materials r WHERE r.created_at<b.end_at) AS resources,
 (SELECT count(*) FROM wishes w WHERE w.created_at<b.end_at) AS wishes,
 CASE WHEN b.date='summary' THEN (SELECT count(*) FROM wishes WHERE state='fulfilled')
 ELSE (SELECT count(*) FROM fulfilled f WHERE f.happened_at<b.end_at) END AS fulfilled_wishes
FROM buckets b ORDER BY b.date`
