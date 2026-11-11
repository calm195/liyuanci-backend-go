package matchmaking

import (
	"context"
	"errors"
	"math/rand"
	"sort"
)

// autoPairingEdge 是已通过资格与交互约束检查的候选关系。
type autoPairingEdge struct {
	Left  string
	Right string
	Score int
}

// solveAutoPairing 先最大化配对数，再最大化总分；种子只改变等价解的选择。
// 每个左侧节点有一个可用虚拟列，禁止边不会因人数不等被强行选中。
// 输入先规范排序再随机排列，使同一快照与种子不受数据库返回顺序影响。
func solveAutoPairing(ctx context.Context, left, right []string, edges []autoPairingEdge, seed int64) ([]autoPairingEdge, error) {
	if len(left) > 2000 || len(right) > 2000 {
		return nil, errors.New("自动分配人数超过计算上限")
	}
	l := append([]string(nil), left...)
	r := append([]string(nil), right...)
	sort.Strings(l)
	sort.Strings(r)
	li, err := autoPairingIndex(l)
	if err != nil {
		return nil, err
	}
	ri, err := autoPairingIndex(r)
	if err != nil {
		return nil, err
	}
	// 有界整数评分保证基数权重及 Hungarian 势函数不会溢出。
	maxScore := 0
	for _, e := range edges {
		if _, ok := li[e.Left]; !ok {
			return nil, errors.New("候选左侧不存在")
		}
		if _, ok := ri[e.Right]; !ok {
			return nil, errors.New("候选右侧不存在")
		}
		if e.Score < 0 || e.Score > 1000000 {
			return nil, errors.New("候选评分超出范围")
		}
		maxScore = max(maxScore, e.Score)
	}
	if len(l) == 0 || len(r) == 0 {
		return []autoPairingEdge{}, nil
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(l), func(i, j int) { l[i], l[j] = l[j], l[i] })
	rng.Shuffle(len(r), func(i, j int) { r[i], r[j] = r[j], r[i] })
	// 将较小分组置于行，减少不均衡活动的计算量。
	swapped := len(l) > len(r)
	if swapped {
		l, r = r, l
	}
	li, _ = autoPairingIndex(l)
	ri, _ = autoPairingIndex(r)
	n, m := len(l), len(r)
	scores := make([]int, (n * m))
	for i := range scores {
		scores[i] = -1
	}
	for _, e := range edges {
		a, b := e.Left, e.Right
		if swapped {
			a, b = b, a
		}
		idx := li[a]*m + ri[b]
		if scores[idx] >= 0 {
			return nil, errors.New("候选关系重复")
		}
		scores[idx] = e.Score
	}
	cardinality := int64(n)*int64(maxScore) + 1
	// 最小费用形式：合法边为负费用，虚拟边零费用，非法边正费用。
	cost := func(i, j int) int64 {
		if j >= m {
			return 0
		}
		score := scores[i*m+j]
		if score < 0 {
			return cardinality
		}
		return -cardinality - int64(score)
	}
	assignment, err := autoPairingHungarian(ctx, n, m+n, cost)
	if err != nil {
		return nil, err
	}
	out := make([]autoPairingEdge, 0, n)
	for i, j := range assignment {
		if j < 0 || j >= m || scores[i*m+j] < 0 {
			continue
		}
		e := autoPairingEdge{Left: l[i], Right: r[j], Score: scores[i*m+j]}
		if swapped {
			e.Left, e.Right = e.Right, e.Left
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Left < out[j].Left })
	return out, nil
}

func autoPairingIndex(ids []string) (map[string]int, error) {
	out := make(map[string]int, len(ids))
	for i, id := range ids {
		if id == "" {
			return nil, errors.New("报名标识不能为空")
		}
		if _, ok := out[id]; ok {
			return nil, errors.New("报名标识重复")
		}
		out[id] = i
	}
	return out, nil
}

// autoPairingHungarian 用势函数和增广路径求矩形最小费用分配。
// 内存为行列线性规模；取消检查避免后台任务停止后继续占用 CPU。
func autoPairingHungarian(ctx context.Context, n, m int, cost func(int, int) int64) ([]int, error) {
	const inf int64 = 1 << 60
	u, v := make([]int64, n+1), make([]int64, m+1)
	p, way := make([]int, m+1), make([]int, m+1)
	minv, used := make([]int64, m+1), make([]bool, m+1)
	for i := 1; i <= n; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p[0] = i
		for j := 0; j <= m; j++ {
			minv[j] = inf
			used[j] = false
		}
		j0 := 0
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			used[j0] = true
			i0, j1, delta := p[j0], 0, inf
			for j := 1; j <= m; j++ {
				if used[j] {
					continue
				}
				cur := cost(i0-1, j-1) - u[i0] - v[j]
				if cur < minv[j] {
					minv[j] = cur
					way[j] = j0
				}
				if minv[j] < delta {
					delta = minv[j]
					j1 = j
				}
			}
			for j := 0; j <= m; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
			if j0 == 0 {
				break
			}
		}
	}
	out := make([]int, n)
	for i := range out {
		out[i] = -1
	}
	for j := 1; j <= m; j++ {
		if p[j] > 0 {
			out[p[j]-1] = j - 1
		}
	}
	return out, nil
}
