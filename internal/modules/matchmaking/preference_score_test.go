package matchmaking

import (
	"context"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func TestPreferenceScoresAndHalfPoints(t *testing.T) {
	a, b := ruleProfile(), ruleProfile()
	a.Campus, b.Campus = "wushan", "international"
	a.MBTI, b.MBTI = "INFP", "ENFJ"
	for _, tc := range []struct {
		name                               string
		campusesA, campusesB, mbtiA, mbtiB []string
		campus, mbti                       float64
	}{
		{"不限", nil, nil, nil, nil, 5, 10},
		{"双向命中", []string{"international"}, []string{"wushan"}, []string{"ENFJ"}, []string{"INFP"}, 5, 10},
		{"单向命中", []string{"wushan"}, []string{"wushan"}, []string{"INFP"}, []string{"INFP"}, 2.5, 5},
		{"双向未中", []string{"wushan"}, []string{"international"}, []string{"INFP"}, []string{"ENFJ"}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.PreferredCampuses, b.PreferredCampuses = tc.campusesA, tc.campusesB
			a.PreferredMBTI, b.PreferredMBTI = tc.mbtiA, tc.mbtiB
			score, d := Score(a, b)
			if d.PreferredCampus != tc.campus || d.PreferredMBTI != tc.mbti {
				t.Fatalf("错误明细 %+v", d)
			}
			base := float64(int(d.MBTI+d.Age+d.Campus+d.Height+d.Weight) + d.Traits)
			if score != base+tc.campus+tc.mbti {
				t.Fatalf("丢失半分 %v %+v", score, d)
			}
			reverse, _ := Score(b, a)
			if reverse != score {
				t.Fatal("评分不对称")
			}
		})
	}
	a.PreferredMBTI = []string{"INFP"}
	b.PreferredMBTI = nil
	b.MBTI = ""
	_, d := Score(a, b)
	if d.PreferredMBTI != 5 {
		t.Fatal("明确偏好与缺失MBTI不应命中")
	}
}

func TestPreferenceCodesAndDeduplication(t *testing.T) {
	if validPreferenceCodes([]string{"any"}, campusCodes) || validPreferenceCodes([]string{"BAD"}, mbtis) {
		t.Fatal("无效偏好被接受")
	}
	got := uniqueCodes([]string{"wushan", "wushan", "international"})
	if len(got) != 2 || got[0] != "international" {
		t.Fatalf("未规范化 %v", got)
	}
}

// TestAutoSolutionPrefersHalfPoint 验证仅相差半分的竞争不会被整数截断或随机决胜覆盖。
func TestAutoSolutionPrefersHalfPoint(t *testing.T) {
	lower, higher, peer := ruleProfile(), ruleProfile(), ruleProfile()
	lower.Gender, higher.Gender, peer.Gender = "male", "male", "female"
	lower.Campus, higher.Campus, peer.Campus = "international", "wushan", "wushan"
	lower.IdealTraits = []string{"真诚", "温柔"}
	peer.PersonalTraits = []string{"真诚", "温柔"}
	lower.PreferredCampuses = []string{"wushan"}
	higher.PreferredCampuses = []string{"international"}
	lowScore, _ := Score(lower, peer)
	highScore, _ := Score(higher, peer)
	if highScore-lowScore != 0.5 {
		t.Fatalf("合成候选应仅相差半分：低分=%v 高分=%v", lowScore, highScore)
	}
	snapshot := pairingSnapshot{
		People: []pairingPerson{
			{Row: entity.MatchmakingEnrollment{ID: "lower", AccountID: "account-lower"}, Profile: lower},
			{Row: entity.MatchmakingEnrollment{ID: "higher", AccountID: "account-higher"}, Profile: higher},
			{Row: entity.MatchmakingEnrollment{ID: "peer", AccountID: "account-peer"}, Profile: peer},
		},
		Blocks: map[string]bool{},
	}
	for seed := int64(0); seed < 32; seed++ {
		result, err := autoSolution(context.Background(), snapshot, seed)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Pairs) != 1 || result.Pairs[0].First.EnrollmentID != "higher" || result.Pairs[0].Score != highScore {
			t.Fatalf("种子%d不得牺牲半分：%+v", seed, result)
		}
	}
}
