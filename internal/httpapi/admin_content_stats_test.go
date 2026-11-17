package httpapi

import "testing"

func TestAdminContentStatisticsNegotiation(t *testing.T) {
	for _, tc := range []struct {
		query        string
		valid, stats bool
	}{
		{"", true, false}, {"include=stats", true, true}, {"include=stats&state=published", true, true},
		{"include=unknown", false, false}, {"include=stats&include=stats", false, false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			q, err := parseAdminContentQuery(tc.query)
			if (err == nil) != tc.valid || err == nil && q.IncludeStats != tc.stats {
				t.Fatalf("query=%s q=%+v err=%v", tc.query, q, err)
			}
		})
	}
}
