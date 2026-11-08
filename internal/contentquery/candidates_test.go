package contentquery

import (
	"context"
	"slices"
	"testing"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

type memoryCandidates struct {
	values []Candidate
}

func (c *memoryCandidates) Get(context.Context, string) ([]Candidate, bool) {
	return c.values, true
}

func (c *memoryCandidates) Set(_ context.Context, _ string, values []Candidate) {
	c.values = values
}

func TestCollectRefreshesHiddenCachedCandidate(t *testing.T) {
	cache := &memoryCandidates{values: []Candidate{{ID: "visible"}, {ID: "withdrawn"}}}
	fresh := []Candidate{{ID: "new"}, {ID: "visible"}, {ID: "third"}}
	calls := 0
	items, next, err := Collect(context.Background(), cache, WishListKey, Candidate{}, 2,
		func(after Candidate, _ int) ([]Candidate, error) {
			calls++
			if after.ID != "" {
				t.Fatal("回源没有从头查询")
			}
			return fresh, nil
		},
		func(id string) (string, error) {
			if id == "withdrawn" {
				return "", command.ErrNotFound
			}
			return id, nil
		},
	)
	if err != nil || calls != 1 || !slices.Equal(items, []string{"new", "visible"}) || next.ID != "visible" ||
		!slices.Equal(cache.values, fresh) {
		t.Fatalf("缓存回源错误：%v %+v %d %v", items, next, calls, err)
	}
}
