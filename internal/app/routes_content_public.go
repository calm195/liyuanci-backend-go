package app

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/contentquery"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/modules/adminauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/whisper"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

// PublicContentOptions 装配公开候选缓存和周榜，所有模块必须在 HTTP 开始监听前完成注入。
type PublicContentOptions struct {
	Cache contentquery.CandidateCache

	wishes   *wish.Service
	whispers *whisper.Service
	stories  bool
}

// AttachWhispers 将独立树洞入口的领域服务加入同一热点榜单。
func (o *PublicContentOptions) AttachWhispers(api *WhisperAPI) {
	o.attachWhisper(api.services.service)
}

func (o *PublicContentOptions) attachWhisper(service *whisper.Service) {
	o.whispers = service
	service.SetCandidateCache(o.Cache)
	if o.wishes != nil {
		service.SetPublicBlockSource(o.wishes.ResolveBlockSource)
	}
}

func (o *PublicContentOptions) register(router *gin.Engine, wishes *wish.Service, stories bool) error {
	o.wishes = wishes
	if o.whispers != nil {
		o.whispers.SetPublicBlockSource(wishes.ResolveBlockSource)
	}
	o.stories = stories
	wishes.SetCandidateCache(o.Cache)
	return o.registerHot(router)
}

// RegisterWhisperOnly 在独立树洞模式补齐周榜入口，组合模式使用已有注册。
func (o *PublicContentOptions) RegisterWhisperOnly(router *gin.Engine) error {
	if o.wishes != nil {
		return nil
	}
	return o.registerHot(router)
}

func (o *PublicContentOptions) registerHot(router *gin.Engine) error {
	return httpapi.RegisterContentHot(router, o.weeklyWishes, o.weeklyWhispers)
}

func (o *PublicContentOptions) weeklyWishes(ctx context.Context, rid string) (wish.PublicPage, error) {
	if o.wishes == nil {
		return wish.PublicPage{}, command.ErrUnavailable
	}
	return o.wishes.Weekly(ctx, rid)
}

func (o *PublicContentOptions) weeklyWhispers(ctx context.Context, token, rid string) (whisper.Page[whisper.PublicPost], error) {
	if o.whispers == nil {
		return whisper.Page[whisper.PublicPost]{}, whisper.ErrModuleUnavailable
	}
	return o.whispers.Weekly(ctx, token, rid)
}

func (o *PublicContentOptions) registerAdminHot(router *gin.Engine, admin *adminauth.Service) {
	httpapi.RegisterAdminHot(router, admin.Restore, o.weeklyWishes, func(ctx context.Context, rid string) (whisper.Page[whisper.PublicPost], error) {
		return o.weeklyWhispers(ctx, "", rid)
	})
}

// Warm 以公开读入口填充候选缓存，同时复用当前资格判断。
func (o *PublicContentOptions) Warm(ctx context.Context) error {
	if o == nil || o.Cache == nil {
		return nil
	}
	ctx = contentquery.Refresh(ctx)
	var failures []error
	if o.wishes != nil {
		_, err := o.wishes.List(ctx, "", wish.Query{Limit: 20})
		failures = append(failures, err)
		_, err = o.wishes.Weekly(ctx, "")
		failures = append(failures, err)
		if o.stories {
			_, err = o.wishes.Stories(ctx, "", "", 20)
			failures = append(failures, err)
		}
	}
	if o.whispers != nil {
		_, err := o.whispers.ListPosts(ctx, "", "", false, whisper.Query{Limit: 20})
		if !errors.Is(err, whisper.ErrModuleUnavailable) {
			failures = append(failures, err)
			_, err = o.whispers.Weekly(ctx, "", "")
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
