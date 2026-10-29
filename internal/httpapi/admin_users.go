package httpapi

import (
	"context"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
)

// AdminUsersAPI 提供后台普通资料查询，与既有用户处罚POST共享路径但不共享写权限。
type AdminUsersAPI interface {
	UsersList(context.Context, string, string, safety.UsersQuery) (safety.UserPage, error)
	UserDetailRead(context.Context, string, string, string) (safety.UserDetail, error)
	UserPenalties(context.Context, string, string, string, safety.UsersQuery) (safety.UserPenaltyPage, error)
}

// RegisterAdminUsers 显式注册脱敏只读入口；基础健康模式不装配。
func RegisterAdminUsers(r *gin.Engine, service AdminUsersAPI) error {
	if r == nil || service == nil {
		return command.ErrInvalid
	}
	g := r.Group("/api/v1/admin/users")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	g.GET("", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, e := adminUsersQuery(c, true)
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := service.UsersList(c.Request.Context(), token, c.GetString("request_id"), q)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.GET("/:id", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, e := url.ParseQuery(c.Request.URL.RawQuery)
		if e != nil || len(q) != 0 {
			adminError(c, command.ErrInvalid)
			return
		}
		id, e := safety.NormalizeUserID(c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := service.UserDetailRead(c.Request.Context(), token, c.GetString("request_id"), id)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	g.GET("/:id/penalties", func(c *gin.Context) {
		token, ok := bearer(c)
		if !ok {
			return
		}
		q, e := adminUsersQuery(c, false)
		if e != nil {
			adminError(c, e)
			return
		}
		id, e := safety.NormalizeUserID(c.Param("id"))
		if e != nil {
			adminError(c, e)
			return
		}
		out, e := service.UserPenalties(c.Request.Context(), token, c.GetString("request_id"), id, q)
		if e != nil {
			adminError(c, e)
			return
		}
		adminResult(c, 200, out)
	})
	return nil
}

func adminUsersQuery(c *gin.Context, list bool) (safety.UsersQuery, error) {
	q := safety.UsersQuery{Limit: 20}
	values, e := url.ParseQuery(c.Request.URL.RawQuery)
	if e != nil {
		return q, command.ErrInvalid
	}
	for k, v := range values {
		if len(v) != 1 || (v[0] == "" && k != "nickname") {
			return q, command.ErrInvalid
		}
		switch k {
		case "cursor":
			q.Cursor = v[0]
		case "limit":
			// 仅接受十进制数字，避免符号、空白及非规范输入。
			for _, c := range v[0] {
				if c < '0' || c > '9' {
					return q, command.ErrInvalid
				}
			}
			q.Limit, e = strconv.Atoi(v[0])
			if e != nil {
				return q, command.ErrInvalid
			}
		case "user_id":
			if !list {
				return q, command.ErrInvalid
			}
			q.UserID = v[0]
		case "nickname":
			if !list {
				return q, command.ErrInvalid
			}
			q.Nickname = v[0]
		case "verification":
			if !list {
				return q, command.ErrInvalid
			}
			q.Verification = v[0]
		case "restriction":
			if !list {
				return q, command.ErrInvalid
			}
			q.Restriction = v[0]
		default:
			return q, command.ErrInvalid
		}
	}
	return safety.NormalizeUsersQuery(q)
}
