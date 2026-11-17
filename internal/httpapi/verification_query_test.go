package httpapi

import (
	"testing"
)

func TestIdentityReviewQuery(t *testing.T) {
	for _, test := range []struct {
		name, query string
		valid       bool
		include     bool
	}{
		{name: "旧队列", valid: true},
		{name: "旧工作区", query: "view=pending&limit=10", valid: true},
		{name: "待审首次时间", query: "view=pending&include=created_at", valid: true, include: true},
		{name: "历史首次时间", query: "view=history&include=created_at&limit=50", valid: true, include: true},
		{name: "缺工作区", query: "include=created_at"},
		{name: "空工作区", query: "view=&include=created_at"},
		{name: "非法工作区", query: "view=all"},
		{name: "未知增量", query: "view=pending&include=asset_id"},
		{name: "空增量", query: "view=pending&include="},
		{name: "重复增量", query: "view=pending&include=created_at&include=created_at"},
		{name: "重复工作区", query: "view=pending&view=history"},
		{name: "重复页上限", query: "limit=10&limit=20"},
		{name: "未知参数", query: "account_id=other"},
		{name: "过大页", query: "limit=51"},
		{name: "无效页", query: "limit=0"},
		{name: "非数字页", query: "limit=bad"},
		{name: "非法编码", query: "view=%FF%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, err := parseIdentityReviewQuery(test.query)
			if (err == nil) != test.valid {
				t.Fatalf("查询有效性不符: %v", err)
			}
			if err == nil && query.includeCreatedAt != test.include {
				t.Fatal("未按显式参数协商字段")
			}
		})
	}
}
