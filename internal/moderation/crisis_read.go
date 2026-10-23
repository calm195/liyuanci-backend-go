package moderation

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// CrisisView 的列表投影仅含案件状态，完整学号正文仅在具体案件读取并完成访问审计后提供。
type CrisisView struct {
	ID            string    `json:"id"`
	Version       int64     `json:"version"`
	Status        string    `json:"status"`
	StudentNumber *string   `json:"student_number,omitempty"`
	Body          *string   `json:"body,omitempty"`
	Timeline      []Summary `json:"timeline,omitempty"`
	details       []CrisisTimeline
}

// CrisisTimeline 保留处理人、动作和时刻，理由只在具体案件解密后提供。
type CrisisTimeline struct {
	ID     string    `json:"id"`
	Action string    `json:"action"`
	Actor  string    `json:"actor"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
}

// CrisisPage 不含身份或内容摘要。
type CrisisPage struct {
	Items      []CrisisView `json:"items"`
	NextCursor *string      `json:"next_cursor"`
}

// EvidenceView 只投影该案独立证据正文及主体学号，不返回存储键或学生卡引用。
type EvidenceView struct {
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	CaseID        string `json:"case_id"`
	Body          string `json:"body"`
	StudentNumber string `json:"student_number"`
	Status        string `json:"status"`
}

func crisisSummary(c entity.EvidenceCrisisCase) CrisisView {
	return CrisisView{ID: c.ID, Version: c.Version, Status: c.Status}
}

// CrisisCases 使用当前危机角色和主体绑定游标，夜间待办亦保留在队列供次日接手。
func (s *Service) CrisisCases(ctx context.Context, token, rid, cursor string, limit int) (out CrisisPage, err error) {
	defer func() {
		if err != nil {
			out = CrisisPage{}
		}
	}()
	defer s.done("crisis.list", rid, time.Now(), &err)
	if limit < 1 || limit > 100 {
		return out, command.ErrInvalid
	}
	out.Items = []CrisisView{}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		actor, e := s.authorizeRoles(db.Statement.Context, db, token, command.Discovery{}, []string{"crisis_operator", "super_admin"})
		if e != nil {
			return e
		}
		after, e := s.after(actor, "crisis", cursor)
		if e != nil {
			return e
		}
		q := db.Model(&entity.EvidenceCrisisCase{}).Table("evidence.crisis_cases AS c").Select("c.*").Joins("JOIN evidence.evidence_cases ec ON ec.id=c.evidence_case_id AND ec.status<>'deletion_pending'")
		if after != "" {
			q = q.Where("c.id>?", after)
		}
		var rows []entity.EvidenceCrisisCase
		if e = q.Order("c.id").Limit(limit + 1).Find(&rows).Error; e != nil {
			return e
		}
		if len(rows) > limit {
			rows = rows[:limit]
			next := s.cursor(actor, "crisis", rows[len(rows)-1].ID)
			out.NextCursor = &next
		}
		for _, r := range rows {
			out.Items = append(out.Items, crisisSummary(r))
		}
		return nil
	})
	return
}

// CrisisCase 对具体案件解密独立证据，普通数据注销后仍只允许案件角色读取限定证据。
func (s *Service) CrisisCase(ctx context.Context, token, rid, id string) (out CrisisView, err error) {
	defer func() {
		if err != nil {
			out = CrisisView{}
		}
	}()
	defer s.done("crisis.detail", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		ctx := db.Statement.Context
		actor, e := s.authorizeRoles(ctx, db, token, command.Discovery{}, []string{"crisis_operator", "super_admin"})
		if e != nil {
			return e
		}
		c, e := crisisRow(db, id)
		if e != nil {
			return e
		}
		var ec entity.EvidenceCase
		if e = db.Where("id=? AND kind='crisis' AND status<>'deletion_pending'", c.EvidenceCaseID).Take(&ec).Error; e != nil {
			return command.ErrNotFound
		}
		item, e := evidenceItem(db, ec.ID)
		if e != nil {
			return e
		}
		var subject crisisSubject
		var body string
		if e = decryptEvidence(ctx, s.codec, "crisis_subject", item.ID, item.SubjectCipher, &subject); e != nil {
			return e
		}
		if e = decryptEvidence(ctx, s.codec, "crisis_body", item.ID, item.BodyCipher, &body); e != nil {
			return e
		}
		out = crisisSummary(c)
		out.StudentNumber = &subject.StudentNumber
		out.Body = &body
		out.details = []CrisisTimeline{{ID: c.ID, Action: "isolated", Actor: "system", At: c.DetectedAt}}
		out.Timeline = []Summary{{ID: item.ID, Version: c.Version, Status: c.Status, Kind: "evidence"}}
		var rows []entity.EvidenceAction
		if e = db.Where("case_id=?", ec.ID).Order("at,id").Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			var reason string
			if len(r.ReasonCipher) > 0 {
				if e = decryptEvidence(ctx, s.codec, "crisis_reason", r.ID, r.ReasonCipher, &reason); e != nil {
					return e
				}
			}
			out.Timeline = append(out.Timeline, Summary{ID: r.ID, Version: 1, Status: r.Action, Kind: "action"})
			out.details = append(out.details, CrisisTimeline{ID: r.ID, Action: r.Action, Actor: r.Actor, At: r.At, Reason: reason})
		}
		// 必要访问动作保存在证据域，普通注销清理不能抹去案件访问轨迹。
		return crisisAction(ctx, db, s.codec, c, actor, "view", "案件授权查看", time.Now().UTC())
	})
	return
}

// CrisisEvidence 只接受证据范围标识或该范围唯一证据项标识，不提供普通数据全量检索。
func (s *Service) CrisisEvidence(ctx context.Context, token, rid, id string) (out EvidenceView, err error) {
	defer func() {
		if err != nil {
			out = EvidenceView{}
		}
	}()
	defer s.done("crisis.evidence", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		ctx := db.Statement.Context
		actor, e := s.authorizeRoles(ctx, db, token, command.Discovery{}, []string{"crisis_operator", "super_admin"})
		if e != nil {
			return e
		}
		var item entity.EvidenceItem
		if e = db.Where("id=? OR case_id=?", id, id).Take(&item).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrNotFound
			}
			return e
		}
		var c entity.EvidenceCrisisCase
		if e = db.Where("evidence_case_id=?", item.CaseID).Take(&c).Error; e != nil {
			return command.ErrNotFound
		}
		var ec entity.EvidenceCase
		if e = db.Where("id=? AND kind='crisis' AND status<>'deletion_pending'", item.CaseID).Take(&ec).Error; e != nil {
			return command.ErrNotFound
		}
		var subject crisisSubject
		var body string
		if e = decryptEvidence(ctx, s.codec, "crisis_subject", item.ID, item.SubjectCipher, &subject); e != nil {
			return e
		}
		if e = decryptEvidence(ctx, s.codec, "crisis_body", item.ID, item.BodyCipher, &body); e != nil {
			return e
		}
		out = EvidenceView{ID: item.ID, Version: ec.Version, CaseID: c.ID, Body: body, StudentNumber: subject.StudentNumber, Status: c.Status}
		return crisisAction(ctx, db, s.codec, c, actor, "evidence_view", "限定证据授权查看", time.Now().UTC())
	})
	return
}

// CrisisTimelineDetails 补充提供案件动作的处理人和时刻；固定契约的timeline仍使用CaseSummary投影。
func (s *Service) CrisisTimelineDetails(ctx context.Context, token, rid, id string) ([]CrisisTimeline, error) {
	out, e := s.CrisisCase(ctx, token, rid, id)
	if e != nil {
		return nil, e
	}
	if out.details == nil {
		return []CrisisTimeline{}, nil
	}
	return out.details, nil
}

// CrisisWorkView 暴露待办的安全元数据和人工接手窗口，不包含投递租约或身份正文。
type CrisisWorkView struct {
	HandoffAt *time.Time       `json:"handoff_at"`
	Items     []CrisisWorkItem `json:"items"`
}

// CrisisWorkItem 供危机工作台区分送达、失败重试及尚未接手。
type CrisisWorkItem struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Status   string    `json:"status"`
	Attempts int64     `json:"attempts"`
	NextAt   time.Time `json:"next_at"`
}

// CrisisWork 仅允许当前危机角色查看该案的持久投递状态。
func (s *Service) CrisisWork(ctx context.Context, token, rid, id string) (out CrisisWorkView, err error) {
	defer func() {
		if err != nil {
			out = CrisisWorkView{}
		}
	}()
	defer s.done("crisis.work", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.transaction(ctx, func(db *gorm.DB) error {
		_, e := s.authorizeRoles(db.Statement.Context, db, token, command.Discovery{}, []string{"crisis_operator", "super_admin"})
		if e != nil {
			return e
		}
		c, e := crisisRow(db, id)
		if e != nil {
			return e
		}
		out.HandoffAt = c.HandoffAt
		out.Items = []CrisisWorkItem{}
		var rows []entity.CrisisDelivery
		if e = db.Where("case_id=?", id).Order("kind").Find(&rows).Error; e != nil {
			return e
		}
		for _, r := range rows {
			out.Items = append(out.Items, CrisisWorkItem{r.ID, r.Kind, r.Status, r.Attempts, r.NextAt})
		}
		return nil
	})
	return
}
