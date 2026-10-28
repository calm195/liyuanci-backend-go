package httpapi

import "testing"

// TestCapsuleInputBoundary 验证安全告知、时间规范和精确JSON字段。
func TestCapsuleInputBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"接受", `{"body":" 原文 ","unlock_at":"2026-10-29T00:00:00Z","safety_notice_accepted":true}`, true},
		{"未告知", `{"body":"正文","unlock_at":"2026-10-29T00:00:00Z","safety_notice_accepted":false}`, false},
		{"缺告知", `{"body":"正文","unlock_at":"2026-10-29T00:00:00Z"}`, false},
		{"小数时间", `{"body":"正文","unlock_at":"2026-10-29T00:00:00.000Z","safety_notice_accepted":true}`, false},
		{"时区偏移", `{"body":"正文","unlock_at":"2026-10-29T08:00:00+08:00","safety_notice_accepted":true}`, false},
		{"非法日期", `{"body":"正文","unlock_at":"2026-02-30T00:00:00Z","safety_notice_accepted":true}`, false},
		{"重复时间", `{"body":"正文","unlock_at":"2026-10-29T00:00:00Z","unlock_at":"2026-10-30T00:00:00Z","safety_notice_accepted":true}`, false},
		{"接收者注入", `{"body":"正文","unlock_at":"2026-10-29T00:00:00Z","safety_notice_accepted":true,"recipient_id":"他人"}`, false},
		{"显式空", `{"body":null,"unlock_at":"2026-10-29T00:00:00Z","safety_notice_accepted":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, e := decodeWhisperCapsule([]byte(tc.body))
			if (e == nil) != tc.valid {
				t.Fatal(v, e)
			}
			if tc.valid && v.Body != " 原文 " {
				t.Fatal("改变原文")
			}
		})
	}
}
