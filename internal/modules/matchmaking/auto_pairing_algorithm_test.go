package matchmaking

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

// TestAutoPairingMatchesExhaustive 用小图穷举核对基数优先和总分最优，覆盖稀疏与不均衡情况。
func TestAutoPairingMatchesExhaustive(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	for trial := 0; trial < 150; trial++ {
		n, m := 1+rng.Intn(5), 1+rng.Intn(5)
		l, r := autoPairingTestIDs(n, "l"), autoPairingTestIDs(m, "r")
		edges := []autoPairingEdge{}
		weights := make([][]int, n)
		for i := range weights {
			weights[i] = make([]int, m)
			for j := range weights[i] {
				weights[i][j] = -1
				if rng.Intn(3) > 0 {
					weights[i][j] = rng.Intn(15)
					edges = append(edges, autoPairingEdge{l[i], r[j], weights[i][j]})
				}
			}
		}
		bestN, bestScore := 0, 0
		var visit func(int, int, int, int)
		visit = func(i, used, count, score int) {
			if i == n {
				if count > bestN || count == bestN && score > bestScore {
					bestN, bestScore = count, score
				}
				return
			}
			visit(i+1, used, count, score)
			for j, w := range weights[i] {
				if w >= 0 && used&(1<<j) == 0 {
					visit(i+1, used|1<<j, count+1, score+w)
				}
			}
		}
		visit(0, 0, 0, 0)
		out, err := solveAutoPairing(context.Background(), l, r, edges, int64(trial))
		if err != nil {
			t.Fatal(err)
		}
		score := 0
		ls, rs := map[string]bool{}, map[string]bool{}
		for _, e := range out {
			if ls[e.Left] || rs[e.Right] {
				t.Fatal("重复分配")
			}
			ls[e.Left], rs[e.Right] = true, true
			found := false
			for _, allowed := range edges {
				if e == allowed {
					found = true
					break
				}
			}
			if !found {
				t.Fatal("使用非法边")
			}
			score += e.Score
		}
		if len(out) != bestN || score != bestScore {
			t.Fatalf("trial=%d got=(%d,%d) want=(%d,%d)", trial, len(out), score, bestN, bestScore)
		}
	}
}

func TestAutoPairingSeedAndInputOrder(t *testing.T) {
	l, r := autoPairingTestIDs(5, "l"), autoPairingTestIDs(5, "r")
	edges := []autoPairingEdge{}
	for _, a := range l {
		for _, b := range r {
			edges = append(edges, autoPairingEdge{a, b, 80})
		}
	}
	first, err := solveAutoPairing(context.Background(), l, r, edges, 42)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(9))
	rng.Shuffle(len(l), func(i, j int) { l[i], l[j] = l[j], l[i] })
	rng.Shuffle(len(r), func(i, j int) { r[i], r[j] = r[j], r[i] })
	rng.Shuffle(len(edges), func(i, j int) { edges[i], edges[j] = edges[j], edges[i] })
	again, err := solveAutoPairing(context.Background(), l, r, edges, 42)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("相同种子/输入应可复现: %v", err)
	}
	changed := false
	for seed := int64(0); seed < 20; seed++ {
		out, err := solveAutoPairing(context.Background(), l, r, edges, seed)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, out) {
			changed = true
		}
	}
	if !changed {
		t.Fatal("等价选择未随种子变化")
	}
}

func TestAutoPairingRejectsInvalidAndCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := solveAutoPairing(ctx, []string{"a"}, []string{"b"}, []autoPairingEdge{{"a", "b", 0}}, 1); err == nil {
		t.Fatal("未响应取消")
	}
	cases := []struct {
		l, r  []string
		edges []autoPairingEdge
	}{
		{[]string{"a", "a"}, []string{"b"}, nil},
		{[]string{""}, []string{"b"}, nil},
		{[]string{"a"}, []string{"b"}, []autoPairingEdge{{"x", "b", 1}}},
		{[]string{"a"}, []string{"b"}, []autoPairingEdge{{"a", "b", -1}}},
		{[]string{"a"}, []string{"b"}, []autoPairingEdge{{"a", "b", 1}, {"a", "b", 1}}},
	}
	for i, tc := range cases {
		if _, err := solveAutoPairing(context.Background(), tc.l, tc.r, tc.edges, 1); err == nil {
			t.Fatalf("未拒绝无效输入%d", i)
		}
	}
}

func autoPairingTestIDs(n int, prefix string) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%04d", prefix, i)
	}
	return ids
}

// BenchmarkAutoPairing1000 测量千人满图、同分退化图及人数失衡图的算法耗时与分配内存。
func BenchmarkAutoPairing1000(b *testing.B) {
	for _, tc := range []struct {
		name  string
		n, m  int
		equal bool
	}{
		{"balanced", 500, 500, false}, {"equal", 500, 500, true}, {"imbalanced", 100, 900, false},
	} {
		b.Run(tc.name, func(b *testing.B) {
			l, r := autoPairingTestIDs(tc.n, "l"), autoPairingTestIDs(tc.m, "r")
			edges := make([]autoPairingEdge, 0, tc.n*tc.m)
			for i, a := range l {
				for j, c := range r {
					score := 50 + (i*31+j*17)%150
					if tc.equal {
						score = 100
					}
					edges = append(edges, autoPairingEdge{a, c, score})
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := solveAutoPairing(context.Background(), l, r, edges, int64(i))
				if err != nil || len(out) != tc.n {
					b.Fatalf("分配失败: %v", err)
				}
			}
		})
	}
}
