package matchmaking

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

const autoAlgorithm = "max-cardinality-weight-traits-v1"

// AutoPerson 只包含预览必需的报名标识和昵称。
type AutoPerson struct {
	EnrollmentID string `json:"enrollment_id"`
	Name         string `json:"name"`
}

// AutoPair 是确认前的只读方案，不表示已占用名额。
type AutoPair struct {
	First        AutoPerson   `json:"first"`
	Second       AutoPerson   `json:"second"`
	Score        int          `json:"score"`
	ScoreDetails ScoreDetails `json:"score_details"`
}

// AutoUnmatched 解释合格落单或被排除的报名者。
type AutoUnmatched struct {
	AutoPerson
	Reason string `json:"reason"`
}

// AutoResult 保存完整方案，持久化时必须加密。
type AutoResult struct {
	Pairs     []AutoPair      `json:"pairs"`
	Unmatched []AutoUnmatched `json:"unmatched"`
	Excluded  []AutoUnmatched `json:"excluded"`
}

// AutoJob 是任务当前阶段及已授权的方案投影。
type AutoJob struct {
	AutoResult
	ID               string    `json:"id"`
	ActivityID       string    `json:"activity_id"`
	Version          int64     `json:"version"`
	ActivityVersion  int64     `json:"activity_version"`
	Status           string    `json:"status"`
	Seed             string    `json:"seed"`
	Algorithm        string    `json:"algorithm"`
	ParticipantCount int       `json:"participant_count"`
	EligibleCount    int       `json:"eligible_count"`
	PairedCount      int       `json:"paired_count"`
	UnmatchedCount   int       `json:"unmatched_count"`
	ExcludedCount    int       `json:"excluded_count"`
	TotalScore       int       `json:"total_score"`
	Error            string    `json:"error"`
	CreatedAt        time.Time `json:"created_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// AutoApplyInput 同时绑定任务和活动版本，未知结果重试沿用幂等键。
type AutoApplyInput struct {
	ExpectedVersion int64 `json:"expected_version"`
	ActivityVersion int64 `json:"activity_version"`
}

func autoRoute() durable.Route {
	return durable.Route{Kind: "matchmaking_auto_pairing", TargetKind: "matchmaking_auto_job", EventType: "matchmaking.auto_requested"}
}
func emptyAutoResult() AutoResult {
	return AutoResult{Pairs: []AutoPair{}, Unmatched: []AutoUnmatched{}, Excluded: []AutoUnmatched{}}
}
func autoBinding(id string) securedata.Binding {
	return securedata.Binding{Purpose: "matchmaking.auto_result.v1", ObjectID: id, Version: 1}
}
func autoWindow(a entity.MatchmakingActivity, now time.Time) bool {
	return a.Status == "published" && !now.Before(a.RegistrationClosesAt) && now.Before(a.EndsAt)
}

// CreateAutoJob 生成持久任务及输入指纹，不占用名额，不更改活动版本。
func (s *Service) CreateAutoJob(ctx context.Context, token, key, rid, id string, input VersionInput) (out Receipt, err error) {
	defer s.done("auto_create", rid, time.Now(), &err)
	if !validID(id) || input.ExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, id, "auto_create", "POST", "/api/v1/admin/matchmaking/activities/"+id+"/auto-pairing-jobs", input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		if a.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if !autoWindow(a, tx.Now()) {
			return "", command.ErrConflict
		}
		// 过期运行任务先作废，避免异常退出永久占用同活动任务位。
		if e := tx.DB().Model(&entity.MatchmakingAutoJob{}).Where("activity_id=? AND status IN ('pending','running') AND expires_at<=?", id, tx.Now()).Updates(map[string]any{"status": "stale", "error": "预览已过期", "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		var count int64
		if e := tx.DB().Model(&entity.MatchmakingAutoJob{}).Where("activity_id=? AND status IN ('pending','running')", id).Count(&count).Error; e != nil {
			return "", e
		}
		if count > 0 {
			return "", command.ErrConflict
		}
		snapshot, e := s.pairingSnapshot(ctx, tx.DB(), id, tx.Now())
		if e != nil {
			return "", e
		}
		var seedBytes [8]byte
		if _, e = rand.Read(seedBytes[:]); e != nil {
			return "", e
		}
		requestedBy := tx.Actor().ID
		row := entity.MatchmakingAutoJob{ID: uuid.NewString(), ActivityID: id, Version: 1, ActivityVersion: a.Version, Status: "pending", Seed: int64(binary.BigEndian.Uint64(seedBytes[:])), Algorithm: autoAlgorithm, InputHash: snapshot.Fingerprint, ParticipantCount: len(snapshot.People), CreatedAt: tx.Now(), ExpiresAt: tx.Now().Add(15 * time.Minute), RequestedBy: &requestedBy}
		for _, p := range snapshot.People {
			if p.Reason == "" {
				row.EligibleCount++
			} else {
				row.ExcludedCount++
			}
		}
		if e = tx.DB().Create(&row).Error; e != nil {
			return "", e
		}
		if e = durable.Append(ctx, tx.DB(), s.log, autoRoute(), durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: row.ID, AggregateVersion: 1}); e != nil {
			return "", e
		}
		return row.ID, audit(tx.DB(), id, requestedBy, row.ID, "auto_generate", "", tx.Now())
	}, nil)
}

func (s *Service) decodeAutoResult(ctx context.Context, row entity.MatchmakingAutoJob) (AutoResult, error) {
	out := emptyAutoResult()
	if len(row.ResultCipher) == 0 {
		return out, nil
	}
	var envelope securedata.Envelope
	if e := json.Unmarshal(row.ResultCipher, &envelope); e != nil {
		return out, e
	}
	raw, e := s.codec.Decrypt(ctx, autoBinding(row.ID), envelope)
	if e != nil {
		return out, e
	}
	defer clear(raw)
	e = json.Unmarshal(raw, &out)
	return out, e
}

// ReadAutoJob 读取指定或最新任务；过期和输入变更会即时隐藏旧方案。
func (s *Service) ReadAutoJob(ctx context.Context, token, rid, id, jobID string) (out AutoJob, err error) {
	defer s.done("auto_read", rid, time.Now(), &err)
	if jobID != "latest" && !validID(jobID) {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, id, true, func(db *gorm.DB, r reader) error {
		var row entity.MatchmakingAutoJob
		q := db.Where("activity_id=?", id)
		if jobID == "latest" {
			q = q.Order("created_at DESC,id DESC")
		} else {
			q = q.Where("id=?", jobID)
		}
		if e := q.Take(&row).Error; e != nil {
			return e
		}
		out = AutoJob{AutoResult: emptyAutoResult(), ID: row.ID, ActivityID: row.ActivityID, Version: row.Version, ActivityVersion: row.ActivityVersion, Status: row.Status, Seed: strconv.FormatInt(row.Seed, 10), Algorithm: row.Algorithm, ParticipantCount: row.ParticipantCount, EligibleCount: row.EligibleCount, PairedCount: row.PairedCount, UnmatchedCount: row.UnmatchedCount, ExcludedCount: row.ExcludedCount, TotalScore: row.TotalScore, Error: row.Error, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}
		if row.Status == "ready" || row.Status == "pending" || row.Status == "running" {
			snapshot, e := s.pairingSnapshot(ctx, db, id, r.Now)
			if e != nil {
				return e
			}
			if !r.Now.Before(row.ExpiresAt) || snapshot.Fingerprint != row.InputHash || !autoWindow(snapshot.Activity, r.Now) {
				// 行状态和密文同事务作废；轮询不会交付已失效的个人预览。
				if e = db.Model(&row).Updates(map[string]any{"status": "stale", "version": row.Version + 1, "error": "参与资料或资格已变化，请重新生成", "result_cipher": nil}).Error; e != nil {
					return e
				}
				out.Status, out.Version, out.Error = "stale", row.Version+1, "参与资料或资格已变化，请重新生成"
				return nil
			}
		}
		if out.Status == "ready" || out.Status == "applied" {
			var e error
			out.AutoResult, e = s.decodeAutoResult(ctx, row)
			if e != nil {
				return e
			}
			return recordAccess(db, r.ID, row.ID, "matchmaking.auto_preview", r.Now)
		}
		return nil
	})
	if err != nil {
		out = AutoJob{}
	}
	return
}

// ApplyAutoJob 在同一活动事务复核完整输入后批量创建草稿，保留所有既有占用。
func (s *Service) ApplyAutoJob(ctx context.Context, token, key, rid, id, jobID string, input AutoApplyInput) (out Receipt, err error) {
	defer s.done("auto_apply", rid, time.Now(), &err)
	if !validID(jobID) || input.ExpectedVersion < 1 || input.ActivityVersion < 1 {
		return out, command.ErrInvalid
	}
	return s.write(ctx, token, key, rid, id, "auto_apply", "POST", "/api/v1/admin/matchmaking/activities/"+id+"/auto-pairing-jobs/"+jobID+"/apply", input, func(tx *command.Tx, a entity.MatchmakingActivity) (string, error) {
		var job entity.MatchmakingAutoJob
		if e := tx.DB().Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND activity_id=?", jobID, id).Take(&job).Error; e != nil {
			return "", e
		}
		if job.Version != input.ExpectedVersion || a.Version != input.ActivityVersion || job.ActivityVersion != a.Version {
			return "", command.ErrVersionConflict
		}
		if job.Status != "ready" || !tx.Now().Before(job.ExpiresAt) || !autoWindow(a, tx.Now()) {
			return "", command.ErrConflict
		}
		snapshot, e := s.pairingSnapshot(ctx, tx.DB(), id, tx.Now())
		if e != nil {
			return "", e
		}
		if snapshot.Fingerprint != job.InputHash {
			return "", command.ErrVersionConflict
		}
		result, e := s.decodeAutoResult(ctx, job)
		if e != nil {
			return "", e
		}
		byID := map[string]pairingPerson{}
		for _, p := range snapshot.People {
			byID[p.Row.ID] = p
		}
		pairs := make([]entity.MatchmakingPair, 0, len(result.Pairs))
		members := make([]entity.MatchmakingMember, 0, 2*len(result.Pairs))
		used := map[string]bool{}
		for _, candidate := range result.Pairs {
			first, ok := byID[candidate.First.EnrollmentID]
			if !ok {
				return "", command.ErrConflict
			}
			second, ok := byID[candidate.Second.EnrollmentID]
			if !ok {
				return "", command.ErrConflict
			}
			if first.Reason != "" || second.Reason != "" || first.Profile.Gender == second.Profile.Gender || used[first.Row.ID] || used[second.Row.ID] || snapshot.Blocks[blockedKey(first.Row.AccountID, second.Row.AccountID)] {
				return "", command.ErrConflict
			}
			used[first.Row.ID], used[second.Row.ID] = true, true
			score, details := Score(first.Profile, second.Profile)
			raw, e := json.Marshal(details)
			if e != nil {
				return "", e
			}
			if score != candidate.Score {
				return "", command.ErrConflict
			}
			pair := entity.MatchmakingPair{ID: uuid.NewString(), ActivityID: id, Version: 1, Status: "draft", Score: score, ScoreDetails: raw, Algorithm: autoAlgorithm, CreatedAt: tx.Now()}
			pairs = append(pairs, pair)
			for _, p := range []pairingPerson{first, second} {
				members = append(members, entity.MatchmakingMember{PairID: pair.ID, EnrollmentID: p.Row.ID, ActivityID: id, AccountID: p.Row.AccountID, ProfileVersion: p.Row.ProfileVersion, Active: true})
			}
		}
		if len(pairs) > 0 {
			if e = tx.DB().CreateInBatches(&pairs, 100).Error; e != nil {
				return "", e
			}
			if e = tx.DB().CreateInBatches(&members, 200).Error; e != nil {
				return "", e
			}
		}
		if e = bump(tx.DB(), id); e != nil {
			return "", e
		}
		if e = tx.DB().Model(&job).Updates(map[string]any{"status": "applied", "version": job.Version + 1}).Error; e != nil {
			return "", e
		}
		return job.ID, audit(tx.DB(), id, tx.Actor().ID, job.ID, "auto_apply", "", tx.Now())
	}, nil)
}

func autoJob(db *gorm.DB, id string) (entity.MatchmakingAutoJob, error) {
	var row entity.MatchmakingAutoJob
	err := db.Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = command.ErrNotFound
	}
	return row, err
}
