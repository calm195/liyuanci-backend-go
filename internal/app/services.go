package app

import (
	"bytes"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/modules/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/modules/safety"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentauth"
	"github.com/calm195/liyuanci-backend-go/internal/modules/studentcard"
	"github.com/calm195/liyuanci-backend-go/internal/modules/verification"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
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

// ServiceDependencies 显式提供身份、互动、治理和资料模块的借用依赖；各模块可共用应用连接池，学生/后台授权由当前会话及领域函数执行。
// 装配器不拥有池或供应商，宿主必须先停止 HTTP 并等待在途请求结束，再释放这些资源。
type ServiceDependencies struct {
	// WishDB 与 CrisisDB 复用应用连接池；各领域方法仍复核会话、岗位及访问范围。
	// WishDB 仅在显式启用心愿模块时必填。
	// PrivacyDB及EvidenceDeletionDB借用同一应用池，按领域用途分别执行授权。
	PrivacyDB          *gorm.DB
	EvidenceDeletionDB *gorm.DB
	DeletionJournal    privacy.Journal
	Eraser             privacy.Eraser
	CrisisDB           *gorm.DB
	WishDB             *gorm.DB
	Grades             wish.Grades
	StudentDB          *gorm.DB
	ReviewDB           *gorm.DB
	Logger             *zap.Logger
	Ready              httpapi.Readiness
	Exchange           studentauth.Exchanger
	Identity           Identity
	Limiter            studentauth.Limiter
	Codec              *securedata.Codec
	Objects            Objects
	Scanner            studentcard.Scanner
	Rules              verification.Rules
}

// ServiceConfig 显式启用各业务模块并传入各自技术预算和用途密钥。
type ServiceConfig struct {
	// Resources 显式启用认证资料与后台维护入口。
	Resources *resourceplaza.Config
	// Crisis 显式启用独立危机后台服务。
	Crisis *moderation.Config
	// Privacy与EvidenceDeletion显式启用删除能力，使用领域授权限定操作范围。
	Privacy          *privacy.Config
	EvidenceDeletion *privacy.Config
	// Safety 仅在显式给出独立密钥和技术预算时注册安全治理路由。
	Safety *safety.Config
	// Chat 仅在心愿模块与技术预算同时显式配置时启用。
	Chat *chat.Config
	// Moderation 仅在显式提供独立游标密钥后注册正式后台审核能力。
	Moderation *moderation.Config
	// Wish 为空时保持认证应用原有能力，不虚报心愿写动作。
	Wish         *wish.ServiceConfig
	Auth         studentauth.Config
	Card         studentcard.Config
	Verification verification.Config
}

// serviceSet 只持有已构造的领域服务，不拥有连接池、供应商或调度协程。
// 字段显式列出，各职责构造器在全部成功后才交给HTTP注册边界。
type serviceSet struct {
	auth                              *studentauth.Service
	cards                             *studentcard.Service
	students, reviews                 *verification.Service
	wishes                            *wish.Service
	chats                             *chat.Service
	studentResources, adminResources  *resourceplaza.Service
	studentSafety, adminSafety        *safety.Service
	accountDeletion, evidenceDeletion *privacy.Service
	contentReviews, crisisReviews     *moderation.Service
}

// buildServices 按依赖顺序完整构造服务，不执行DDL、外部调用或启动后台任务。
func buildServices(d ServiceDependencies, cfg ServiceConfig) (out *serviceSet, err error) {
	if d.Logger == nil {
		return nil, command.ErrInvalid
	}
	start := time.Now()
	defer func() {
		result := "success"
		if err != nil {
			result = "failure"
		}
		d.Logger.Info("业务服务构造完成", zap.String("result", result), zap.Duration("duration", time.Since(start)), zap.Bool("wishes_enabled", cfg.Wish != nil))
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
	// 模块共享一个数据库账号及连接池；每个入口仍独立检查当前业务会话、角色和用途密钥。
	var e error
	for _, db := range []*gorm.DB{d.StudentDB, d.ReviewDB} {
		if _, e = db.DB(); e != nil {
			return nil, command.ErrInvalid
		}
	}
	out = &serviceSet{}
	if err = out.buildInteraction(d, cfg); err != nil {
		return nil, err
	}
	if err = out.buildResources(d, cfg); err != nil {
		return nil, err
	}
	if err = out.buildSafety(d, cfg); err != nil {
		return nil, err
	}
	if err = out.buildPrivacy(d, cfg); err != nil {
		return nil, err
	}
	if err = out.buildIdentity(d, cfg); err != nil {
		return nil, err
	}
	if err = out.buildModeration(d, cfg); err != nil {
		return nil, err
	}
	return out, nil
}
