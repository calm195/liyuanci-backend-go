package safety

import (
	"context"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

type userRow struct {
	ID                        string
	Nickname                  *string
	Verification, Restriction string
	MutedUntil                *time.Time
	CreatedAt                 time.Time
	Version                   int64
}

func (r userRow) summary(now time.Time) UserSummary {
	name := r.Nickname
	if name != nil && strings.TrimSpace(*name) == "" {
		name = nil
	}
	return UserSummary{r.ID, name, r.Verification, r.Restriction, utcTime(r.MutedUntil), r.CreatedAt.UTC(), now}
}

func utcTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	t := v.UTC()
	return &t
}

// usersRows 使用同一时间计算全局限制；状态和昵称过滤均在键集截断之前执行。
// translate 只折叠ASCII，strpos按字面匹配，避免locale、LIKE通配符及转义语义漂移。
func usersRows(ctx context.Context, db *gorm.DB, q UsersQuery, after usersCursor, now time.Time) (rows []userRow, err error) {
	limits := db.WithContext(ctx).Table("public.penalties AS p").
		Select("p.account_id,bool_or(p.action_kind='ban') AS banned,max(p.ends_at) FILTER (WHERE p.action_kind='mute') AS muted_until").
		Where("p.revoked_at IS NULL AND p.action_kind IN ('mute','ban') AND p.starts_at<=? AND (p.ends_at IS NULL OR p.ends_at>?)", now, now).Group("p.account_id")
	base := db.WithContext(ctx).Table("public.accounts AS a").
		Select("a.id,a.nickname,a.verification,a.created_at,a.version,CASE WHEN coalesce(p.banned,false) THEN 'banned' WHEN p.muted_until IS NOT NULL THEN 'muted' ELSE 'normal' END AS restriction,CASE WHEN coalesce(p.banned,false) THEN NULL ELSE p.muted_until END AS muted_until").
		Joins("LEFT JOIN (?) AS p ON p.account_id=a.id", limits).Where("a.lifecycle='active'")
	if q.UserID != "" {
		base = base.Where("a.id=?", q.UserID)
	}
	if q.Nickname != "" {
		base = base.Where("strpos(translate(coalesce(a.nickname,''),'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'),?)>0", q.Nickname)
	}
	if q.Verification != "" {
		base = base.Where("a.verification=?", q.Verification)
	}
	query := db.WithContext(ctx).Table("(?) AS u", base).Select("u.*")
	if q.Restriction != "" {
		query = query.Where("u.restriction=?", q.Restriction)
	}
	if after.ID != "" {
		query = query.Where("(u.created_at,u.id)<(?,?::uuid)", after.Position, after.ID)
	}
	err = query.Order("u.created_at DESC,u.id DESC").Limit(q.Limit + 1).Scan(&rows).Error
	return
}

// ordinaryUserContent 是普通案件的授权交集；匿名来源与危机来源绝不生成用户页关联入口。
const ordinaryUserContent = `c.kind IN ('wish','application','message','story')
 AND NOT EXISTS (SELECT 1 FROM public.content_restrictions x WHERE x.content_id=c.id AND x.kind='crisis' AND x.active)
 AND NOT EXISTS (SELECT 1 FROM public.moderation_tasks t JOIN public.moderation_scans s ON s.task_id=t.id WHERE t.content_id=c.id AND s.crisis_hit)`

// visibleUserPenalty 在分页前剔除普通运营不可见的危机记录。缺失内容关联的历史只提供安全摘要。
const visibleUserPenalty = `NOT EXISTS (SELECT 1 FROM public.content_restrictions x WHERE x.content_id=p.content_id AND x.kind='crisis' AND x.active)
 AND NOT EXISTS (SELECT 1 FROM public.moderation_tasks t JOIN public.moderation_scans s ON s.task_id=t.id WHERE t.content_id=p.content_id AND s.crisis_hit)`

func usersPenaltyQuery(ctx context.Context, db *gorm.DB, id string, roles []string, now time.Time) *gorm.DB {
	q := db.WithContext(ctx).Table("public.penalties AS p").Where("p.account_id=? AND p.starts_at<=?", id, now)
	if !slices.Contains(roles, "super_admin") && !slices.Contains(roles, "crisis_operator") {
		q = q.Where(visibleUserPenalty)
	}
	return q
}

func usersActivePenalties(ctx context.Context, db *gorm.DB, id string, roles []string, now time.Time) ([]ActivePenalty, error) {
	rows := []ActivePenalty{}
	e := usersPenaltyQuery(ctx, db, id, roles, now).Select("p.id,p.action_kind AS kind,p.starts_at,p.ends_at").Where("p.action_kind IN ('mute','ban') AND p.revoked_at IS NULL AND (p.ends_at IS NULL OR p.ends_at>?)", now).Order("p.starts_at DESC,p.id DESC").Scan(&rows).Error
	for i := range rows {
		rows[i].StartsAt = rows[i].StartsAt.UTC()
		rows[i].EndsAt = utcTime(rows[i].EndsAt)
	}
	return rows, e
}

type userPenaltyRow struct {
	ID, Level, Kind            string
	StartsAt                   time.Time
	EndsAt, RevokedAt          *time.Time
	CaseID, CaseKind, AppealID *string
}

func (r userPenaltyRow) projection(now time.Time) UserPenalty {
	status := "active"
	if r.RevokedAt != nil {
		status = "revoked"
	} else if r.EndsAt != nil && !r.EndsAt.After(now) {
		status = "expired"
	}
	reason := map[string]string{"minor": "轻微违规处置", "general": "一般违规处置", "severe": "严重违规处置"}[r.Level]
	out := UserPenalty{ID: r.ID, Level: r.Level, Kind: r.Kind, Status: status, StartsAt: r.StartsAt.UTC(), EndsAt: utcTime(r.EndsAt), RevokedAt: utcTime(r.RevokedAt), Reason: &reason, AppealID: r.AppealID}
	if r.CaseID != nil && r.CaseKind != nil {
		out.CaseLink = &UserCaseLink{*r.CaseKind, *r.CaseID}
	}
	return out
}

func usersPenaltyRows(ctx context.Context, db *gorm.DB, id string, roles []string, limit int, after usersCursor, now time.Time) (rows []userPenaltyRow, err error) {
	// 两个标量子查询只投影本页所需链接；不读取reason_cipher或案件正文。
	ordinary := `EXISTS (SELECT 1 FROM public.content_registry c WHERE c.id=p.content_id AND c.author_id=p.account_id AND ` + ordinaryUserContent + `)`
	q := usersPenaltyQuery(ctx, db, id, roles, now).
		Select(`p.id,p.level,p.action_kind AS kind,p.starts_at,p.ends_at,p.revoked_at,
 link.id AS case_id,link.kind AS case_kind,
 CASE WHEN ` + ordinary + ` THEN (SELECT a.id FROM public.appeals a WHERE a.penalty_id=p.id AND a.account_id=p.account_id ORDER BY a.created_at DESC,a.id DESC LIMIT 1) END AS appeal_id`).
		Joins(`LEFT JOIN LATERAL (
 SELECT r.id,'ordinary_report'::text AS kind FROM public.reports r
 WHERE r.id=p.violation_ref AND r.content_id=p.content_id AND r.target_kind IN ('wish','application','message','story') AND ` + ordinary + `
 UNION ALL
 SELECT t.id,'ordinary_review'::text AS kind FROM public.moderation_tasks t
 WHERE t.id=p.violation_ref AND t.content_id=p.content_id AND ` + ordinary + `
 LIMIT 1) AS link ON true`)
	if after.ID != "" {
		q = q.Where("(p.starts_at,p.id)<(?,?::uuid)", after.Position, after.ID)
	}
	err = q.Order("p.starts_at DESC,p.id DESC").Limit(limit + 1).Scan(&rows).Error
	return
}

func usersNavigation(ctx context.Context, db *gorm.DB, id string) (cases, appeals bool, err error) {
	var row struct{ Cases, Appeals bool }
	// 普通处罚guard接受已处理举报，或精确版本普通命中的拒绝审核任务；导航使用同一资格。
	err = db.WithContext(ctx).Raw(`SELECT EXISTS (
 SELECT 1 FROM public.content_registry c WHERE c.author_id=? AND `+ordinaryUserContent+` AND (
 EXISTS (SELECT 1 FROM public.reports r WHERE r.content_id=c.id AND r.target_kind IN ('wish','application','message','story') AND r.status='resolved') OR
 EXISTS (SELECT 1 FROM public.moderation_tasks t JOIN public.moderation_scans s ON s.task_id=t.id WHERE t.content_id=c.id AND t.content_version=c.content_version AND t.status='rejected' AND t.decision='rejected' AND s.ordinary_hit AND NOT s.crisis_hit))) AS cases,
 EXISTS (SELECT 1 FROM public.appeals a JOIN public.penalties p ON p.id=a.penalty_id JOIN public.content_registry c ON c.id=p.content_id WHERE a.account_id=? AND p.account_id=a.account_id AND c.author_id=a.account_id AND a.status='pending' AND `+ordinaryUserContent+`) AS appeals`, id, id).Scan(&row).Error
	return row.Cases, row.Appeals, err
}
