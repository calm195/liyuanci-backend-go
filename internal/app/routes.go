package app

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
)

// newServiceRouter 先构造完整业务服务集合，再一次注册路由；失败不返回半套入口。
// 服务构造与HTTP注册分文件维护，宿主仍负责资源关闭和监听生命周期。
func newServiceRouter(d ServiceDependencies, cfg ServiceConfig, middleware ...gin.HandlerFunc) (out *gin.Engine, err error) {
	if d.Logger == nil {
		return nil, command.ErrInvalid
	}
	defer finish(d.Logger, "service_routes", time.Now(), &err)
	s, e := buildServices(d, cfg)
	if e != nil {
		return nil, e
	}
	router := httpapi.New(d.Logger, d.Ready)
	router.Use(middleware...)
	if s.accountDeletion != nil {
		if e = httpapi.RegisterPrivacy(router, s.accountDeletion); e != nil {
			return nil, e
		}
	}
	if s.evidenceDeletion != nil {
		if e = httpapi.RegisterEvidenceDeletion(router, s.evidenceDeletion); e != nil {
			return nil, e
		}
	}
	if s.crisisReviews != nil {
		if e = httpapi.RegisterCrisis(router, s.crisisReviews); e != nil {
			return nil, e
		}
	}
	if e = httpapi.RegisterDictionaries(router, s.dictionaries); e != nil {
		return nil, e
	}
	if e = httpapi.RegisterModuleGuide(router, s.guide); e != nil {
		return nil, e
	}
	if s.avatars {
		if e = httpapi.RegisterAvatar(router, s.auth); e != nil {
			return nil, e
		}
	}
	if e = httpapi.RegisterStudent(router, s.auth); e != nil {
		return nil, e
	}
	if e = httpapi.RegisterStudentCards(router, s.cards, cfg.Card.MaxBytes); e != nil {
		return nil, e
	}
	if e = httpapi.RegisterVerification(router, s.students, s.reviews); e != nil {
		return nil, e
	}
	if s.wishes != nil {
		publicContent := d.PublicContent
		if publicContent == nil {
			publicContent = &PublicContentOptions{}
		}
		if e = publicContent.register(router, s.wishes, cfg.Wish.MaxStoryBytes > 0); e != nil {
			return nil, e
		}
		if e = httpapi.RegisterWishes(router, s.wishes); e != nil {
			return nil, e
		}
		if e = httpapi.RegisterPublicProfile(router, s.wishes); e != nil {
			return nil, e
		}
		if e = httpapi.RegisterApplications(router, s.wishes); e != nil {
			return nil, e
		}
	}
	if s.chats != nil {
		if e = httpapi.RegisterCare(router, s.chats); e != nil {
			return nil, e
		}
		if e = httpapi.RegisterChat(router, s.chats); e != nil {
			return nil, e
		}
	}
	if s.wishes != nil && cfg.Wish.MaxStoryBytes > 0 {
		if e = httpapi.RegisterStories(router, s.wishes); e != nil {
			return nil, e
		}
	}
	if s.contentReviews != nil {
		if e = httpapi.RegisterModeration(router, s.contentReviews); e != nil {
			return nil, e
		}
		if s.wishes != nil {
			if e = httpapi.RegisterAdminWishContent(router, s.contentReviews, s.wishes); e != nil {
				return nil, e
			}
		}
	}
	if s.studentResources != nil {
		if e = httpapi.RegisterResources(router, s.studentResources, s.adminResources); e != nil {
			return nil, e
		}
	}
	if s.studentSafety != nil {
		if e = httpapi.RegisterSafety(router, s.studentSafety, s.adminSafety); e != nil {
			return nil, e
		}
	}
	return router, nil
}
