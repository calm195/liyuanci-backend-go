package command

import "gorm.io/gorm"

// PageNumber 仅由显式页码请求返回，旧游标响应继续使用原字段。
type PageNumber struct {
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
	Total    *int64 `json:"total,omitempty"`
}

// NumberedPage 在调用方的授权快照内，基于同一筛选查询计数并定位有效页。
func NumberedPage(query *gorm.DB, page, size int) (*gorm.DB, PageNumber, error) {
	if page == 0 {
		return query.Limit(size + 1), PageNumber{}, nil
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Select("count(*)").Count(&total).Error; err != nil {
		return nil, PageNumber{}, err
	}
	meta := PageAt(total, page, size)
	return query.Offset((meta.Page - 1) * size).Limit(size), meta, nil
}

// PageAt 将超出最新范围的请求收敛到末页，空集合仍使用一基页码。
func PageAt(total int64, page, size int) PageNumber {
	last := max(1, int((total+int64(size)-1)/int64(size)))
	return PageNumber{Page: min(page, last), PageSize: size, Total: &total}
}
