package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
)

const adminSessionCookie = "liyuanci_admin_session"

type adminSessionRestorer interface {
	Restore(context.Context, string, string) (adminauth.Session, error)
}

// sameOriginSession 拒绝跨站浏览器请求；恢复操作额外要求不可由HTML表单发送的头。
// sessionHTTPS 与Cookie的Secure策略保持一致，不信任客户端转发头。
func sessionHTTPS(c *gin.Context) bool {
	host := c.Request.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return c.Request.TLS != nil || !(host == "localhost" || host == "127.0.0.1" || host == "::1")
}

func sameOriginSession(c *gin.Context) bool {
	if len(c.Request.Header.Values("Origin")) > 1 || len(c.Request.Header.Values("Sec-Fetch-Site")) > 1 {
		return false
	}

	if site := c.GetHeader("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := c.GetHeader("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != c.Request.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Scheme == "https") != sessionHTTPS(c) {
			return false
		}
	}
	return true
}

func setAdminSessionCookie(c *gin.Context, token string, remove bool) {
	secure := sessionHTTPS(c)
	age := 0
	if remove {
		age = -1
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: adminSessionCookie, Value: token, Path: "/api/v1/admin", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: age})
}

func registerAdminRestore(group *gin.RouterGroup, service AdminService) {
	group.GET("/session", func(c *gin.Context) {
		restorer, ok := service.(adminSessionRestorer)
		if !ok || !sameOriginSession(c) || len(c.Request.Header.Values("X-Session-Restore")) != 1 || c.GetHeader("X-Session-Restore") != "1" {
			adminError(c, adminauth.ErrForbidden)
			return
		}
		cookieCount := 0
		for _, cookie := range c.Request.Cookies() {
			if cookie.Name == adminSessionCookie {
				cookieCount++
			}
		}
		if cookieCount > 1 {
			adminError(c, adminauth.ErrInvalid)
			return
		}
		token, err := c.Cookie(adminSessionCookie)
		if err != nil || strings.TrimSpace(token) == "" {
			adminError(c, adminauth.ErrUnauthenticated)
			return
		}
		out, err := restorer.Restore(c.Request.Context(), token, c.GetString("request_id"))
		if err != nil {
			if errors.Is(err, adminauth.ErrUnauthenticated) {
				setAdminSessionCookie(c, "", true)
			}
			adminError(c, err)
			return
		}
		adminResult(c, http.StatusOK, out)
	})
}
