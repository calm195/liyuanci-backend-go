package httpapi

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// adminPageQuery 严格区分页码和游标，状态筛选只在已明确支持的举报列表开放。
func adminPageQuery(raw string, maxSize int, reportStatus bool) (cursor string, limit, page int, resolved bool, err error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", 0, 0, false, command.ErrInvalid
	}
	limit = 20
	for key, list := range values {
		if len(list) != 1 || list[0] == "" {
			return "", 0, 0, false, command.ErrInvalid
		}
		value := list[0]
		switch key {
		case "cursor":
			if len(value) > 4096 {
				return "", 0, 0, false, command.ErrInvalid
			}
			cursor = value
		case "limit", "page":
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || strings.Trim(value, "0123456789") != "" || strconv.Itoa(n) != value {
				return "", 0, 0, false, command.ErrInvalid
			}
			if key == "limit" {
				limit = n
			} else {
				page = n
			}
		case "status":
			if !reportStatus || value != "resolved" {
				return "", 0, 0, false, command.ErrInvalid
			}
			resolved = true
		default:
			return "", 0, 0, false, command.ErrInvalid
		}
	}
	if limit > maxSize || page > 1000000 || cursor != "" && page != 0 {
		return "", 0, 0, false, command.ErrInvalid
	}
	return
}
