package httpapi

import (
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
)

func moderationCasesQuery(c *gin.Context) (moderation.CaseQuery, bool) {
	q := moderation.CaseQuery{Limit: 20}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		adminError(c, command.ErrInvalid)
		return q, false
	}
	for key, list := range values {
		if len(list) != 1 {
			adminError(c, command.ErrInvalid)
			return q, false
		}
		value := list[0]
		switch key {
		case "q":
			q.Q = value
		case "status":
			q.Status = value
		case "kind":
			q.Kind = value
		case "cursor":
			if value == "" {
				adminError(c, command.ErrInvalid)
				return q, false
			}
			q.Cursor = value
		case "limit":
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || n > 100 {
				adminError(c, command.ErrInvalid)
				return q, false
			}
			q.Limit = n
		default:
			adminError(c, command.ErrInvalid)
			return q, false
		}
	}
	return q, true
}
