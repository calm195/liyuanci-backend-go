package matchmaking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type pairingAccount struct {
	ID           string
	Version      int64
	Lifecycle    string
	Verification string
}

type pairingPerson struct {
	Row     entity.MatchmakingEnrollment
	Profile Profile
	Reason  string
}

type pairingSnapshot struct {
	Activity    entity.MatchmakingActivity
	People      []pairingPerson
	Blocks      map[string]bool
	Fingerprint string
}

func blockedKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + ":" + b
}

// pairingSnapshot 批量读取报名、账号、处罚、占用和拉黑；数据库往返数不随候选人数增长。
func (s *Service) pairingSnapshot(ctx context.Context, db *gorm.DB, id string, now time.Time) (out pairingSnapshot, err error) {
	out.Activity, err = activity(db, id)
	if err != nil {
		return
	}
	rows := []entity.MatchmakingEnrollment{}
	if err = db.Where("activity_id=?", id).Order("id").Find(&rows).Error; err != nil {
		return
	}
	if len(rows) > 2000 {
		return out, command.ErrInvalid
	}
	accountIDs := make([]string, 0, len(rows))
	enrollmentIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		accountIDs = append(accountIDs, row.AccountID)
		enrollmentIDs = append(enrollmentIDs, row.ID)
	}
	accounts := []pairingAccount{}
	if err = db.Model(&entity.Account{}).Select("id,version,lifecycle,verification").Where("id IN ?", accountIDs).Order("id").Scan(&accounts).Error; err != nil {
		return
	}
	punished := []string{}
	if err = db.Model(&entity.Penalty{}).Where("account_id IN ? AND action_kind IN ('ban','mute') AND revoked_at IS NULL AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", accountIDs, now, now).Distinct("account_id").Order("account_id").Pluck("account_id", &punished).Error; err != nil {
		return
	}
	occupied := []string{}
	if err = db.Model(&entity.MatchmakingMember{}).Where("enrollment_id IN ? AND active", enrollmentIDs).Order("enrollment_id").Pluck("enrollment_id", &occupied).Error; err != nil {
		return
	}
	type block struct{ OwnerID, TargetID string }
	blocks := []block{}
	if err = db.Model(&entity.Block{}).Select("owner_id,target_id").Where("owner_id IN ? AND target_id IN ?", accountIDs, accountIDs).Order("owner_id,target_id").Scan(&blocks).Error; err != nil {
		return
	}
	raw, e := json.Marshal(struct {
		Activity           entity.MatchmakingActivity
		Rows               []entity.MatchmakingEnrollment
		Accounts           []pairingAccount
		Punished, Occupied []string
		Blocks             []block
	}{out.Activity, rows, accounts, punished, occupied, blocks})
	if e != nil {
		return out, e
	}
	// 实体JSON刻意隐藏敏感字段，因此指纹另外显式纳入报名版本和密文摘要。
	h := sha256.New()
	_, _ = h.Write(raw)
	for _, row := range rows {
		data, _ := json.Marshal([]any{row.ID, row.AccountID, row.Status, row.Version, row.ProfileVersion, row.ProfileCipher})
		_, _ = h.Write(data)
	}
	activityData, _ := json.Marshal([]any{out.Activity.ID, out.Activity.Version, out.Activity.Status, out.Activity.RegistrationClosesAt, out.Activity.EndsAt})
	_, _ = h.Write(activityData)
	out.Fingerprint = hex.EncodeToString(h.Sum(nil))
	accountMap := map[string]pairingAccount{}
	for _, a := range accounts {
		accountMap[a.ID] = a
	}
	restricted := map[string]bool{}
	for _, a := range punished {
		restricted[a] = true
	}
	used := map[string]bool{}
	for _, a := range occupied {
		used[a] = true
	}
	out.Blocks = map[string]bool{}
	for _, b := range blocks {
		out.Blocks[blockedKey(b.OwnerID, b.TargetID)] = true
	}
	out.People = make([]pairingPerson, 0, len(rows))
	for _, row := range rows {
		a, exists := accountMap[row.AccountID]
		if !exists || a.Lifecycle != "active" {
			out.People = append(out.People, pairingPerson{Row: row, Profile: Profile{Name: "资料不可访问"}, Reason: "ineligible"})
			continue
		}
		p, e := s.decrypt(ctx, row)
		if e != nil {
			return out, e
		}
		person := pairingPerson{Row: row, Profile: p}
		switch {
		case row.Status != "approved":
			person.Reason = "not_approved"
		case !exists || a.Lifecycle != "active" || a.Verification != "verified" || restricted[a.ID] || (p.Gender != "male" && p.Gender != "female"):
			person.Reason = "ineligible"
		case used[row.ID]:
			person.Reason = "occupied"
		}
		out.People = append(out.People, person)
	}
	return
}

// candidatesFromSnapshot 全部计算在同一批量快照中完成，不逐人查询资格或拉黑。
func candidatesFromSnapshot(snapshot pairingSnapshot, enrollmentID string) ([]Candidate, error) {
	var first *pairingPerson
	for i := range snapshot.People {
		if snapshot.People[i].Row.ID == enrollmentID {
			first = &snapshot.People[i]
			break
		}
	}
	if first == nil {
		return nil, command.ErrNotFound
	}
	if first.Reason != "" {
		return nil, command.ErrConflict
	}
	out := []Candidate{}
	for _, other := range snapshot.People {
		if other.Row.ID == enrollmentID || other.Reason != "" || other.Profile.Gender == first.Profile.Gender || snapshot.Blocks[blockedKey(first.Row.AccountID, other.Row.AccountID)] {
			continue
		}
		p := other.Profile
		score, details := Score(first.Profile, p)
		out = append(out, Candidate{Enrollment: Enrollment{ID: other.Row.ID, Version: other.Row.Version, Status: other.Row.Status, Reason: other.Row.Reason, Profile: &p, Eligible: true}, Score: score, ScoreDetails: details})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Enrollment.ID < out[j].Enrollment.ID
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}
