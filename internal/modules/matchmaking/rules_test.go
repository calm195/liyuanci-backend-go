package matchmaking

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

func ruleProfile() Profile {
	return Profile{Name: "合成参与者", Gender: "male", Grade: "undergraduate_3", Campus: "wushan", MBTI: "INFP", Preferences: Preferences{Age: 21, AgeMin: 18, AgeMax: 25, Height: 175, HeightMin: 150, HeightMax: 190, Weight: 130, WeightMin: 90, WeightMax: 160, Wechat: "synthetic", Introduction: "喜欢阅读和散步"}, Consent: true}
}

func ruleActivity() ActivityInput {
	start := time.Date(2026, 12, 31, 10, 0, 0, 0, beijing)
	return ActivityInput{Title: "跨年相遇", Description: "合成活动说明", Campuses: []string{"wushan"}, RegistrationOpensAt: start.Add(-72 * time.Hour), RegistrationClosesAt: start.Add(-48 * time.Hour), ResultsAt: start.Add(-24 * time.Hour), StartsAt: start, EndsAt: start.Add(72 * time.Hour), Days: 3}
}

func TestActivityValidation(t *testing.T) {
	if !validActivity(ruleActivity()) {
		t.Fatal("合法跨年活动被拒绝")
	}
	for _, tc := range []struct {
		name   string
		change func(*ActivityInput)
	}{
		{"标题空白", func(v *ActivityInput) { v.Title = " " }},
		{"标题超限", func(v *ActivityInput) { v.Title = strings.Repeat("字", 81) }},
		{"重复校区", func(v *ActivityInput) { v.Campuses = []string{"wushan", "wushan"} }},
		{"未知校区", func(v *ActivityInput) { v.Campuses = []string{"any"} }},
		{"无报名窗口", func(v *ActivityInput) { v.RegistrationClosesAt = v.RegistrationOpensAt }},
		{"先发布后截止", func(v *ActivityInput) { v.ResultsAt = v.RegistrationClosesAt.Add(-time.Second) }},
		{"任务未完整覆盖", func(v *ActivityInput) { v.EndsAt = unlockAt(v.StartsAt, v.Days) }},
		{"天数超限", func(v *ActivityInput) { v.Days = 31 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := ruleActivity()
			tc.change(&value)
			if validActivity(value) {
				t.Fatal("无效活动被接受")
			}
		})
	}
}

func TestProfileValidation(t *testing.T) {
	if !validProfile(ruleProfile()) {
		t.Fatal("合法资料被拒绝")
	}
	for _, tc := range []struct {
		name   string
		change func(*Profile)
	}{
		{"拒绝未成年", func(v *Profile) { v.Age = 17 }},
		{"拒绝年龄反向", func(v *Profile) { v.AgeMin, v.AgeMax = 26, 25 }},
		{"拒绝身高反向", func(v *Profile) { v.HeightMin, v.HeightMax = 180, 150 }},
		{"拒绝体重反向", func(v *Profile) { v.WeightMin, v.WeightMax = 180, 150 }},
		{"拒绝未授权", func(v *Profile) { v.Consent = false }},
		{"拒绝空微信号", func(v *Profile) { v.Wechat = " " }},
		{"拒绝未知MBTI", func(v *Profile) { v.MBTI = "unknown" }},
		{"拒绝无效编码", func(v *Profile) { v.Name = string([]byte{0xff}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := ruleProfile()
			tc.change(&value)
			if validProfile(value) {
				t.Fatal("无效资料被接受")
			}
		})
	}
}

func TestScoreUsesBothCompletePreferenceRanges(t *testing.T) {
	a, b := ruleProfile(), ruleProfile()
	a.MBTI, b.MBTI = "INFP", "ENFJ"
	n, details := Score(a, b)
	if n != 100 || details.Age != 20 || details.Height != 15 || details.Weight != 10 {
		t.Fatalf("相互满足区间应得满分：%d %+v", n, details)
	}
	// 只调整上限即可改变匹配结果，覆盖旧版忽略上限的错误。
	a.AgeMax, a.HeightMax, a.WeightMax = 20, 170, 120
	n, details = Score(a, b)
	if details.Age != 12 || details.Height != 7.5 || details.Weight != 5 || n != 79 {
		t.Fatalf("一方超出期望上限仍获得满分：%d %+v", n, details)
	}
	for _, first := range mbtis {
		for _, second := range mbtis {
			a.MBTI, b.MBTI = first, second
			forward, _ := Score(a, b)
			backward, _ := Score(b, a)
			if forward != backward || forward < 0 || forward > 100 {
				t.Fatalf("评分不对称或越界：%s/%s %d/%d", first, second, forward, backward)
			}
		}
	}
}

func TestTaskAssignmentUsesBeijingDatesAndNoReplacement(t *testing.T) {
	start := time.Date(2026, 12, 31, 23, 30, 0, 0, beijing)
	activity := entity.MatchmakingActivity{ID: "活动", StartsAt: start, Days: 3}
	items := []Template{{Kind: "online", Body: "固定首日", FixedDay: 1}, {Kind: "offline", Body: "散步"}, {Kind: "online", Body: "分享歌曲"}}
	tasks, err := assignTasks(activity, "配对", items)
	if err != nil || len(tasks) != 3 {
		t.Fatalf("生成失败：%v %+v", err, tasks)
	}
	seen := map[string]bool{}
	for i, task := range tasks {
		if seen[task.Body] || task.DayIndex != i+1 {
			t.Fatal("每日任务重复或序号缺失")
		}
		seen[task.Body] = true
	}
	if tasks[0].Body != "固定首日" || !tasks[0].UnlocksAt.Equal(start) ||
		!tasks[1].UnlocksAt.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, beijing)) ||
		!tasks[2].UnlocksAt.Equal(time.Date(2027, 1, 2, 0, 0, 0, 0, beijing)) {
		t.Fatal("跨年解锁没有使用完整北京时间")
	}
	if _, err := assignTasks(activity, "配对", items[:2]); !errors.Is(err, command.ErrConflict) {
		t.Fatal("任务不足没有拒绝")
	}
	items[1].FixedDay = 1
	if _, err := assignTasks(activity, "配对", items); !errors.Is(err, command.ErrConflict) {
		t.Fatal("重复固定日没有拒绝")
	}
	items[1].FixedDay = 4
	if _, err := assignTasks(activity, "配对", items); !errors.Is(err, command.ErrInvalid) {
		t.Fatal("超出活动天数没有拒绝")
	}
}

func TestActivityPhaseUsesFullTimestamp(t *testing.T) {
	v := ruleActivity()
	a := entity.MatchmakingActivity{Status: "published", RegistrationOpensAt: v.RegistrationOpensAt, RegistrationClosesAt: v.RegistrationClosesAt, StartsAt: v.StartsAt, EndsAt: v.EndsAt, Campuses: []byte(`["wushan"]`)}
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{v.RegistrationOpensAt.Add(-time.Second), "upcoming"},
		{v.RegistrationOpensAt, "registration"},
		{v.RegistrationClosesAt, "matching"},
		{v.StartsAt, "active"},
		{v.EndsAt, "ended"},
	} {
		if got := projectActivity(a, tc.at).Phase; got != tc.want {
			t.Fatalf("阶段=%s，期望%s", got, tc.want)
		}
	}
}
