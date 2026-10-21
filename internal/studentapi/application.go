// Package studentapi 提供学生身份、学生卡、人工认证及可选心愿的显式应用装配入口。
package studentapi

import (
	"bytes"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/verification"
	"github.com/calm195/liyuanci-backend-go/internal/wish"
)

// Identity 让登录和认证申请使用同一身份锁及双代唯一检索实现。
type Identity interface {
	studentauth.Resolver
	verification.Binder
}

// Objects 将上传和授权原卡读取指向同一私有、不可变版本对象适配器。
type Objects interface {
	studentcard.Objects
	verification.Objects
}

// Dependencies 必须由宿主显式提供；数据库池须分别使用学生维护和后台审核最小权限身份。
// 装配器不拥有池或供应商，宿主必须先停止 HTTP 并等待在途请求结束，再释放这些资源。
type Dependencies struct {
	// WishDB 使用独立普通业务运行池，仅在显式启用心愿模块时必填。
	WishDB    *gorm.DB
	Grades    wish.Grades
	StudentDB *gorm.DB
	ReviewDB  *gorm.DB
	Logger    *zap.Logger
	Ready     httpapi.Readiness
	Exchange  studentauth.Exchanger
	Identity  Identity
	Limiter   studentauth.Limiter
	Codec     *securedata.Codec
	Objects   Objects
	Scanner   studentcard.Scanner
	Rules     verification.Rules
}

// Config 明确区分学生会话、学生卡与认证材料的技术预算及独立密钥。
type Config struct {
	// Wish 为空时保持认证应用原有能力，不虚报心愿写动作。
	Wish         *wish.ServiceConfig
	Auth         studentauth.Config
	Card         studentcard.Config
	Verification verification.Config
}

// New 完整构造认证及可选心愿路由后才交给宿主，不执行 DDL、供应商调用或启动后台任务。
// 任一依赖/配置缺失时不返回局部可用的路由，避免 Me 动作与实际入口不一致。
func New(d Dependencies, cfg Config) (out *gin.Engine, err error) {
	if d.Logger == nil {
		return nil, command.ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		d.Logger.Info("学生认证应用装配完成", zap.String("result", result), zap.Duration("duration", time.Since(start)), zap.Bool("wishes_enabled", cfg.Wish != nil))
	}()
	if d.StudentDB == nil || d.ReviewDB == nil || d.Ready == nil {
		return nil, command.ErrInvalid
	}
	// 所有合法上传都必须能在后台完整解密读取，禁止两个入口的资源预算互相矛盾。
	if cfg.Verification.MaxCardBytes < cfg.Card.MaxBytes {
		return nil, command.ErrInvalid
	}
	// 不允许跨用途复用会话和材料回执密钥，避免单个用途泄漏扩大到其他入口。
	if bytes.Equal(cfg.Auth.SessionKey, cfg.Card.ReceiptKey) || bytes.Equal(cfg.Auth.SessionKey, cfg.Verification.ReceiptKey) || bytes.Equal(cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey) {
		return nil, command.ErrInvalid
	}
	studentPool, e := d.StudentDB.DB()
	if e != nil {
		return nil, command.ErrInvalid
	}
	reviewPool, e := d.ReviewDB.DB()
	if e != nil || studentPool == reviewPool {
		return nil, command.ErrInvalid
	}
	var wishes *wish.Service
	if cfg.Wish != nil {
		if d.WishDB == nil {
			return nil, command.ErrInvalid
		}
		pool, failure := d.WishDB.DB()
		if failure != nil || pool == studentPool || pool == reviewPool {
			return nil, command.ErrInvalid
		}
		for _, key := range [][]byte{cfg.Auth.SessionKey, cfg.Card.ReceiptKey, cfg.Verification.ReceiptKey} {
			if bytes.Equal(key, cfg.Wish.ReceiptKey) {
				return nil, command.ErrInvalid
			}
		}
		var failureNew error
		wishes, failureNew = wish.NewService(d.WishDB, d.Logger, d.Codec, d.Grades, *cfg.Wish)
		if failureNew != nil {
			return nil, failureNew
		}
	}
	cfg.Auth.Wishes = wishes != nil
	// 路由由本构造器整体注册，动作提示只在同一成功装配中开启，不采纳调用方的过期标志。
	cfg.Auth.StudentCardAssets = true
	cfg.Auth.VerificationRequests = true
	auth, e := studentauth.New(d.StudentDB, d.Logger, d.Exchange, d.Identity, d.Limiter, cfg.Auth)
	if e != nil {
		return nil, e
	}
	cards, e := studentcard.New(d.StudentDB, d.Logger, auth, d.Codec, d.Objects, d.Scanner, cfg.Card)
	if e != nil {
		return nil, e
	}
	students, e := verification.New(d.StudentDB, d.Logger, d.Codec, d.Identity, d.Rules, d.Objects, cfg.Verification)
	if e != nil {
		return nil, e
	}
	reviews, e := verification.New(d.ReviewDB, d.Logger, d.Codec, d.Identity, d.Rules, d.Objects, cfg.Verification)
	if e != nil {
		return nil, e
	}
	router := httpapi.New(d.Logger, d.Ready)
	if e = httpapi.RegisterStudent(router, auth); e != nil {
		return nil, e
	}
	if e = httpapi.RegisterStudentCards(router, cards); e != nil {
		return nil, e
	}
	if e = httpapi.RegisterVerification(router, students, reviews); e != nil {
		return nil, e
	}
	if wishes != nil {
		if e = httpapi.RegisterWishes(router, wishes); e != nil {
			return nil, e
		}
	}
	return router, nil
}
