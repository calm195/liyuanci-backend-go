package app

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
)

// register 一次注册全部树洞操作；旧安全及注销路由已由宿主装配时复用其边界。
// 失败时宿主必须丢弃该router，禁止监听局部注册结果。
func (a *whisperServices) register(router *gin.Engine) (err error) {
	defer finish(a.runtime.r.log, "api_register", time.Now(), &err)
	if router == nil {
		return command.ErrInvalid
	}
	defer func() {
		if recover() != nil {
			err = command.ErrInvalid
		}
	}()
	exists := func(method, path string) bool {
		for _, route := range router.Routes() {
			if route.Method == method && route.Path == path {
				return true
			}
		}
		return false
	}
	if exists("GET", "/api/v1/whispers/config") {
		return command.ErrInvalid
	}
	for _, register := range []func() error{
		func() error { return httpapi.RegisterWhispers(router, a.service) }, func() error { return httpapi.RegisterWhisperInteractions(router, a.service) },
		func() error { return httpapi.RegisterWhisperCapsules(router, a.service) }, func() error { return httpapi.RegisterInteractionBlocks(router, a.service) },
		func() error {
			return httpapi.RegisterWhisperGovernance(router, a.reviews, a.crises, a.student, a.evidence)
		},
	} {
		if err = register(); err != nil {
			return
		}
	}
	if !exists("GET", "/api/v1/resources") {
		if err = httpapi.RegisterResources(router, a.studentResources, a.adminResources); err != nil {
			return
		}
	}
	if !exists("GET", "/api/v1/me/blocks") {
		if err = httpapi.RegisterSafety(router, a.studentSafety, a.adminSafety); err != nil {
			return
		}
	}
	if !exists("POST", "/api/v1/me/deletion-requests") {
		err = httpapi.RegisterPrivacy(router, a.deletion)
	}
	return
}
