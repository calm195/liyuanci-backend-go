package privacy

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// EvidenceJobItem 仅用于超管列表的最小进度投影，任务所属来源用于选择详情入口。
type EvidenceJobItem struct {
	ID           string    `json:"id"`
	SourceModule string    `json:"source_module"`
	CreatedAt    time.Time `json:"created_at"`
	Receipt
}

// EvidenceJobPage 返回当前超管可见的有效证据待办，页码与总数使用同一快照。
type EvidenceJobPage struct {
	Items      []EvidenceJobItem `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	command.PageNumber
}

// ListEvidenceDeletionJobs 先复核超管权限，再在同一只读快照中计数与读取证据任务。
func (s *Service) ListEvidenceDeletionJobs(ctx context.Context, token, rid string, page, limit int) (out EvidenceJobPage, err error) {
	defer func() {
		if err != nil {
			out = EvidenceJobPage{}
		}
	}()
	defer s.done("evidence.list_jobs", rid, time.Now(), &err)
	if page < 1 || page > 1000000 || limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := authorizeEvidenceAdmin(ctx, db, token); e != nil {
			return e
		}
		// 过期时间使用事务时间，防止任务刚好过期时计数与结果的条件不同。
		base := db.Model(&entity.DeletionJob{}).Where("scope='evidence' AND expires_at>CURRENT_TIMESTAMP")
		query, meta, e := command.NumberedPage(base, page, limit)
		if e != nil {
			return e
		}
		var rows []entity.DeletionJob
		if e = query.Select("job_id,source_module,created_at,phase,error_code").
			Order("created_at DESC,job_id DESC").Scan(&rows).Error; e != nil {
			return e
		}
		out = EvidenceJobPage{Items: make([]EvidenceJobItem, 0, len(rows)), PageNumber: meta}
		for _, row := range rows {
			progress := Receipt{Status: "processing"}
			if row.Phase == "completed" {
				progress.Status = "completed"
			} else if row.ErrorCode != nil {
				switch *row.ErrorCode {
				case "database_cleanup_unavailable", "external_cleanup_unavailable":
					progress.RetryableError = *row.ErrorCode
				case "":
				default:
					progress.RetryableError = "cleanup_unavailable"
				}
			}
			out.Items = append(out.Items, EvidenceJobItem{
				ID: row.JobID, SourceModule: row.SourceModule, CreatedAt: row.CreatedAt.UTC(), Receipt: progress,
			})
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return
}
