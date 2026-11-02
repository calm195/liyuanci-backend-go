package moderation

// CursorPage 是审核、举报和危机列表拥有的分页投影，不借用内容来源模块的页面类型。
// 有界扫描可返回空Items及非空NextCursor；nil与空切片按各读取用例的既有规则保留。
type CursorPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
