package whisper

import "time"

// CapsuleInput 是不可修改的本人信件及UTC整秒预约时间，安全告知必须明确接受。
type CapsuleInput struct {
	Body                 string    `json:"body"`
	UnlockAt             time.Time `json:"unlock_at"`
	SafetyNoticeAccepted bool      `json:"safety_notice_accepted"`
}

// Capsule 的正文仅供本人已解锁详情；创建、列表与终态命令始终返回空正文。
type Capsule struct {
	ID             string        `json:"id"`
	Version        int64         `json:"version"`
	Status         string        `json:"status"`
	UnlockAt       string        `json:"unlock_at"`
	ServerNow      string        `json:"server_now"`
	CreatedAt      string        `json:"created_at"`
	Body           *string       `json:"body"`
	ReviewResult   *ReviewResult `json:"review_result"`
	AllowedActions []string      `json:"allowed_actions"`
}

func validCapsuleTime(now, unlock time.Time) bool {
	_, offset := unlock.Zone()
	if unlock.IsZero() || unlock.Nanosecond() != 0 || offset != 0 {
		return false
	}
	delta := unlock.Sub(now.UTC().Truncate(time.Second))
	return delta >= 60*time.Second && delta <= 365*24*time.Hour
}
