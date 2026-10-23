package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// CareResources 由宿主注入已核验资料；没有内置热线或预约地址，来源与版本随关怀保存。
type CareResources struct {
	Version        string `json:"version"`
	Source         string `json:"source"`
	VerifiedAt     string `json:"verified_at"`
	Hotline24Hours string `json:"hotline_24_hours"`
	Appointment    string `json:"appointment"`
	Notice         string `json:"notice"`
}

// CrisisNotifier 仅接收稳定投递和案件标识，不传正文或身份；接收方必须以投递标识去重并鉴权打开案件。
// 通知不等于人工接手；夜间照常通知，待办的人工窗口由HandoffAt描述。
type CrisisNotifier interface {
	NotifyCrisis(context.Context, string, string) error
}

func validateCare(c *CareResources) bool {
	if c == nil {
		return true
	}
	for _, v := range []string{c.Version, c.Source, c.VerifiedAt, c.Hotline24Hours, c.Appointment, c.Notice} {
		if strings.TrimSpace(v) == "" || len(v) > 4096 {
			return false
		}
	}
	_, e := time.Parse("2006-01-02", c.VerifiedAt)
	return e == nil
}

// ProcessCrisisDelivery 先持久取得有界租约，再在事务外通知，晚到结果只确认相同租约。
// 未配置正式资料/通知方或发送失败保留pending和下一重试时刻，进程崩溃由到期租约恢复。
func (w *Worker) ProcessCrisisDelivery(ctx context.Context) (processed bool, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		result := "success"
		if err != nil {
			result = "failure"
		}
		w.logger.Info("危机待办投递完成", zap.String("result", result), zap.Duration("duration", time.Since(start)), zap.Bool("processed", processed))
	}()
	bounded, cancel := context.WithTimeout(ctx, w.cfg.Consumer.Timeout)
	defer cancel()
	var job entity.CrisisDelivery
	lease := uuid.NewString()
	err = w.db.WithContext(bounded).Transaction(func(db *gorm.DB) error {
		var now time.Time
		if e := db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		e := db.Where("(status='pending' OR (status='leased' AND lease_until<=?)) AND next_at<=?", now, now).Order("next_at,id").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&job).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		if job.Attempts == math.MaxInt64 {
			return command.ErrConflict
		}
		return db.Model(&entity.CrisisDelivery{}).Where("id=?", job.ID).Updates(map[string]any{"status": "leased", "lease_id": lease, "lease_until": now.Add(w.cfg.AnalysisTimeout + 2*w.cfg.Consumer.Timeout), "attempts": gorm.Expr("attempts+1")}).Error
	})
	if err != nil || job.ID == "" {
		return false, safe(err)
	}
	processed = true
	var sendErr error
	if job.Kind == "operator" {
		call, stop := context.WithTimeout(ctx, w.cfg.AnalysisTimeout)
		sendErr = func() (e error) {
			defer func() {
				if recover() != nil {
					e = command.ErrUnavailable
				}
			}()
			if w.cfg.CrisisNotifier == nil {
				return command.ErrUnavailable
			}
			return w.cfg.CrisisNotifier.NotifyCrisis(call, job.ID, job.CaseID)
		}()
		if sendErr == nil {
			sendErr = call.Err()
		}
		stop()
	}
	finish, stop := context.WithTimeout(ctx, w.cfg.Consumer.Timeout)
	defer stop()
	err = w.db.WithContext(finish).Transaction(func(db *gorm.DB) error {
		// 与人工处置保持案件先于投递行的锁序；误报已取消的租约不会创建关怀。
		c, e := crisisRow(db, job.CaseID)
		if e != nil {
			return e
		}
		plan := command.Discovery{Resources: []command.Resource{{Kind: command.CrisisCase, ID: c.ID}}}
		var subject crisisSubject
		if job.Kind == "care" {
			item, e := evidenceItem(db, c.EvidenceCaseID)
			if e != nil {
				return e
			}
			if e = decryptEvidence(finish, w.codec, "crisis_subject", item.ID, item.SubjectCipher, &subject); e != nil {
				return e
			}
			var n int64
			if e = db.Model(&entity.Account{}).Where("id=?", subject.AccountID).Count(&n).Error; e != nil {
				return e
			}
			if n == 1 {
				plan.Accounts = []string{subject.AccountID}
			}
		}
		if _, e = command.LockPlan(finish, db, plan, w.cfg.Consumer.LockTimeout); e != nil {
			return e
		}
		c, e = crisisRow(db, job.CaseID)
		if e != nil {
			return e
		}
		var current entity.CrisisDelivery
		if e = db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", job.ID).Take(&current).Error; e != nil {
			return e
		}
		if current.Status != "leased" || current.LeaseID == nil || *current.LeaseID != lease {
			return nil
		}
		status := "sent"
		if c.Status == "false_positive" {
			status = "cancelled"
		} else if job.Kind == "care" {
			if w.cfg.Care == nil {
				sendErr = command.ErrUnavailable
			} else {
				var n int64
				if e = db.Model(&entity.Account{}).Where("id=? AND lifecycle='active'", subject.AccountID).Count(&n).Error; e != nil {
					return e
				}
				if n != 1 {
					status = "cancelled"
				} else {
					resources, _ := json.Marshal(w.cfg.Care)
					if e = db.Create(&entity.CareCard{ID: job.ID, RecipientID: subject.AccountID, Resources: resources}).Error; e != nil {
						return e
					}
					if e = db.Create(&entity.Notification{ID: uuid.NewString(), RecipientID: subject.AccountID, Type: "crisis_care", TargetRef: job.ID}).Error; e != nil {
						return e
					}
				}
			}
		}
		var now time.Time
		if e = db.Raw("SELECT clock_timestamp()").Scan(&now).Error; e != nil {
			return e
		}
		if sendErr != nil && status != "cancelled" {
			status = "pending"
		}
		return db.Model(&entity.CrisisDelivery{}).Where("id=? AND lease_id=?", job.ID, lease).Updates(map[string]any{"status": status, "next_at": now.Add(w.cfg.Consumer.RetryDelay), "lease_id": nil, "lease_until": nil}).Error
	})
	if err != nil {
		return processed, safe(err)
	}
	// 缺依赖亦有失败完成日志；调用方继续独立扫描，不将待办重试错误吞成整体成功。
	return processed, safe(sendErr)
}

// Maintain 由宿主调度独立补偿危机投递，不挤占正文扫描的外部分析预算。
func (w *Worker) Maintain(ctx context.Context) error { _, e := w.ProcessCrisisDelivery(ctx); return e }
