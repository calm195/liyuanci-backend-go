package education

import (
	"testing"
	"time"
)

func TestGradeAcademicBoundaryAndOverride(t *testing.T) {
	for _, tc := range []struct {
		date, level, override, legacy, want string
		year                                int
	}{
		{"2026-08-31T15:59:59Z", "undergraduate", "", "", "undergraduate_1", 2025},
		{"2026-08-31T16:00:00Z", "undergraduate", "", "", "undergraduate_2", 2025},
		{"2026-11-11T00:00:00Z", "master", "", "", "master_3", 2024},
		{"2026-11-11T00:00:00Z", "doctor", "", "", "doctoral_2", 2025},
		{"2026-11-11T00:00:00Z", "undergraduate", "undergraduate_1", "", "undergraduate_1", 2025},
		{"2026-11-11T00:00:00Z", "undergraduate", "", "", "undergraduate_8", 2019},
		{"2026-11-11T00:00:00Z", "", "", "undergraduate_3", "undergraduate_3", 0},
	} {
		at, _ := time.Parse(time.RFC3339, tc.date)
		if got := GradeCode(tc.year, tc.level, tc.override, tc.legacy, at); got != tc.want {
			t.Errorf("%+v: %s", tc, got)
		}
	}
}

func TestEducationValidation(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, beijing)
	for _, tc := range []struct {
		year            int
		level, override string
		want            bool
	}{
		{0, "", "", true}, {2025, "undergraduate", "", true}, {2026, "undergraduate", "", false},
		{2025, "", "", false}, {0, "master", "", false}, {2024, "doctor", "doctoral_2", true},
		{2024, "doctor", "master_2", false}, {2024, "doctor", "doctoral_02", false},
	} {
		if got := Valid(tc.year, tc.level, tc.override, at); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
	if Label("doctoral_2") != "博二" || Label("undergraduate_11") != "大11" {
		t.Fatal("年级标签错误")
	}
}
