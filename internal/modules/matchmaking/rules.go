package matchmaking

import (
	"crypto/rand"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)
var mbtis = []string{"INFP", "ENFP", "INFJ", "ENFJ", "INTJ", "ENTJ", "INTP", "ENTP", "ISFP", "ESFP", "ISTP", "ESTP", "ISFJ", "ESFJ", "ISTJ", "ESTJ"}

func validID(id string) bool {
	u, e := uuid.Parse(id)
	return e == nil && u != uuid.Nil && u.String() == id
}
func text(value string, max int) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= max
}

func validActivity(v ActivityInput) bool {
	if !text(v.Title, 80) || !text(v.Description, 2000) || v.Days < 1 || v.Days > 30 {
		return false
	}
	// 兼容旧管理端发来的字段，活动保存不再使用校区列表。
	if len(v.Campuses) > len(campusCodes) {
		return false
	}
	seen := map[string]bool{}
	for _, campus := range v.Campuses {
		if !slices.Contains(campusCodes, campus) || seen[campus] {
			return false
		}
		seen[campus] = true
	}
	return !v.RegistrationOpensAt.IsZero() && v.RegistrationOpensAt.Before(v.RegistrationClosesAt) && !v.RegistrationClosesAt.After(v.ResultsAt) && !v.ResultsAt.After(v.StartsAt) && v.StartsAt.Before(v.EndsAt) && unlockAt(v.StartsAt, v.Days).Before(v.EndsAt)
}

func validProfile(p Profile) bool {
	return text(p.Name, 40) && slices.Contains([]string{"male", "female"}, p.Gender) && text(p.Grade, 32) && (p.MBTI == "" || slices.Contains(mbtis, p.MBTI)) &&
		p.Age >= 18 && p.Age <= 80 && p.AgeMin >= 18 && p.AgeMax <= 80 && p.AgeMin <= p.AgeMax &&
		p.Height >= 100 && p.Height <= 250 && p.HeightMin >= 100 && p.HeightMax <= 250 && p.HeightMin <= p.HeightMax &&
		p.Weight >= 50 && p.Weight <= 500 && p.WeightMin >= 50 && p.WeightMax <= 500 && p.WeightMin <= p.WeightMax &&
		optionalText(p.Wechat, 64) && text(p.Introduction, 500) && p.Consent
}

func in(value, min, max int) bool { return value >= min && value <= max }

// Score 保留旧版权重，使用完整的双方区间而非旧版误读的下限。
func Score(a, b Profile) (int, ScoreDetails) {
	d := ScoreDetails{MBTI: 18, Age: 12, Campus: 12.5, Height: 7.5, Weight: 5}
	i, j := slices.Index(mbtis, a.MBTI), slices.Index(mbtis, b.MBTI)
	if i >= 0 && j >= 0 {
		d.MBTI = float64(mbtiScores[i][j]) * 3
	}
	if in(a.Age, b.AgeMin, b.AgeMax) && in(b.Age, a.AgeMin, a.AgeMax) {
		d.Age = 20
	}
	if a.Campus == b.Campus {
		d.Campus = 25
	}
	if in(a.Height, b.HeightMin, b.HeightMax) && in(b.Height, a.HeightMin, a.HeightMax) {
		d.Height = 15
	}
	if in(a.Weight, b.WeightMin, b.WeightMax) && in(b.Weight, a.WeightMin, a.WeightMax) {
		d.Weight = 10
	}
	d.PersonalToIdeal = traitIntersection(a.PersonalTraits, b.IdealTraits)
	d.IdealToPersonal = traitIntersection(a.IdealTraits, b.PersonalTraits)
	d.Traits = 5 * (len(d.PersonalToIdeal) + len(d.IdealToPersonal))
	return int(d.MBTI+d.Age+d.Campus+d.Height+d.Weight) + d.Traits, d
}

// unlockAt 首日从活动开始时刻解锁，后续日期使用北京时间自然日零点。
func unlockAt(start time.Time, day int) time.Time {
	if day == 1 {
		return start
	}
	v := start.In(beijing)
	return time.Date(v.Year(), v.Month(), v.Day()+day-1, 0, 0, 0, 0, beijing)
}

// firstMissingDay 计算任务池最早无法排满的一天。随机候选随天数增加，只增不减。
func firstMissingDay(days int, templates []Template) int {
	fixed := make(map[int]bool)
	released := make([]int, days+1)
	for _, v := range templates {
		if v.FixedDay > 0 {
			fixed[v.FixedDay] = true
		} else if v.MinDay <= days {
			released[max(1, v.MinDay)]++
		}
	}
	available := 0
	for day := 1; day <= days; day++ {
		available += released[day]
		if fixed[day] {
			continue
		}
		if available == 0 {
			return day
		}
		available--
	}
	return 0
}

func assignTasks(a entity.MatchmakingActivity, pair string, templates []Template) ([]entity.MatchmakingTask, error) {
	fixed := map[int]Template{}
	candidates := make([][]Template, a.Days+1)
	for _, v := range templates {
		if !text(v.Body, 1000) || !slices.Contains([]string{"online", "offline"}, v.Kind) ||
			v.FixedDay < 0 || v.FixedDay > a.Days || v.MinDay < 0 || v.MinDay > a.Days ||
			v.FixedDay > 0 && v.MinDay > v.FixedDay {
			return nil, command.ErrInvalid
		}
		if v.FixedDay == 0 {
			minDay := max(1, v.MinDay)
			candidates[minDay] = append(candidates[minDay], v)
		} else {
			if _, ok := fixed[v.FixedDay]; ok {
				return nil, command.ErrConflict
			}
			fixed[v.FixedDay] = v
		}
	}
	out := make([]entity.MatchmakingTask, 0, a.Days)
	var available []Template
	for day := 1; day <= a.Days; day++ {
		available = append(available, candidates[day]...)
		v, ok := fixed[day]
		if !ok {
			if len(available) == 0 {
				return nil, command.ErrConflict
			}
			n, e := rand.Int(rand.Reader, big.NewInt(int64(len(available))))
			if e != nil {
				return nil, e
			}
			i := int(n.Int64())
			v = available[i]
			available[i] = available[len(available)-1]
			available = available[:len(available)-1]
		}
		out = append(out, entity.MatchmakingTask{ID: uuid.NewString(), PairID: pair, DayIndex: day, Kind: v.Kind, Body: v.Body, UnlocksAt: unlockAt(a.StartsAt, day)})
	}
	return out, nil
}

// traitIntersection 以规范化后的报名快照去重计算命中，不依赖当前个人资料。
func traitIntersection(a, b []string) []string {
	out := []string{}
	for _, name := range a {
		if slices.Contains(b, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
