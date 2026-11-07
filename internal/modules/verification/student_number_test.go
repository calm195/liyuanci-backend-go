package verification

import "testing"

// TestStudentNumberFormat 覆盖长度、非ASCII数字、前导零及年份边界。
func TestStudentNumberFormat(t *testing.T) {
	for _, value := range []string{"202600001234", "100000000000", "999999999999"} {
		if !validStudentNumber(value) {
			t.Errorf("有效学号被拒绝：%q", value)
		}
	}
	for _, value := range []string{"", "20260000123", "2026000012345", "000000001234", "2026A0001234", "２０２６００００１２３４", " 202600001234", "202600001234 "} {
		if validStudentNumber(value) {
			t.Errorf("无效学号被接受：%q", value)
		}
	}
}
