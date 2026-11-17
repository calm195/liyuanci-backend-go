package matchmaking

import (
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func TestResultStateUsesPublicationFact(t *testing.T) {
	now := time.Now()
	a := entity.MatchmakingActivity{Status: "published", ResultsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour)}
	for _, tc := range []struct {
		name       string
		enrollment *Enrollment
		pair       *Pair
		published  bool
		want       string
	}{
		{"未报名", nil, nil, true, "not_enrolled"},
		{"待审核", &Enrollment{Status: "pending"}, nil, true, "pending_review"},
		{"已过计划时间未实际公布", &Enrollment{Status: "approved"}, nil, false, "waiting"},
		{"已公布未配对", &Enrollment{Status: "approved"}, nil, true, "unmatched"},
		{"已配对", &Enrollment{Status: "approved"}, &Pair{Status: "published"}, true, "paired"},
		{"配对解除", &Enrollment{Status: "approved"}, &Pair{Status: "ended"}, true, "ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resultState(a, tc.enrollment, tc.pair, tc.published, now); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	if got := resultState(a, &Enrollment{Status: "approved"}, &Pair{Status: "published"}, true, a.EndsAt); got != "ended" {
		t.Fatal(got)
	}
}
