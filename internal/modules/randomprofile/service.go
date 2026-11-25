// Package randomprofile 管理系统随机头像与随机昵称素材。
package randomprofile

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

const (
	// MaxAvatarBytes 与用户头像上传入口保持相同的原图大小上限。
	MaxAvatarBytes  = 2 * 1024 * 1024
	maxAvatarPixels = 4 * 1024 * 1024
	maxNickname     = 40
	maxWord         = maxNickname - 2
	adjective       = "adjective"
	noun            = "noun"
)

var (
	// ErrPoolEmpty 表示停用操作会让至少一个池没有可抽取项。
	ErrPoolEmpty = errors.New("每个随机资料池至少需要一个启用项")
	// ErrNicknameTooLong 表示至少一组启用词条拼接后超出资料昵称长度。
	ErrNicknameTooLong = errors.New("启用词条拼接后不能超过40个字符")
	// ErrDuplicateWord 表示同类词条下已有相同文本。
	ErrDuplicateWord = errors.New("已有相同词条")
	// ErrImageRejected 表示素材未通过图片内容检查。
	ErrImageRejected = errors.New("头像图片未通过安全扫描")
	// ErrImageBusy 表示多张图片正在接受受限的安全扫描。
	ErrImageBusy = errors.New("头像图片扫描繁忙")
)

// Avatar 是后台头像素材投影。
type Avatar struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
	ImagePath string    `json:"image_path"`
}

// Word 是后台随机昵称词条投影。
type Word struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Enabled   bool      `json:"enabled"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Catalog 是分为头像、形容词和名词三部分的素材快照。
type Catalog struct {
	Avatars    []Avatar `json:"avatars"`
	Adjectives []Word   `json:"adjectives"`
	Nouns      []Word   `json:"nouns"`
}

// DrawInput 可指定上一组结果以避免重复。
type DrawInput struct {
	PreviousAvatarID string `json:"previous_avatar_id,omitempty"`
	PreviousNickname string `json:"previous_nickname,omitempty"`
}

// Draw 是单次抽取的昵称与可下载头像。
type Draw struct {
	AvatarID  string `json:"avatar_id"`
	ImagePath string `json:"image_path"`
	Nickname  string `json:"nickname"`
	CanReroll bool   `json:"can_reroll"`
}

// EditAvatarInput 修改头像名称与启用状态。
type EditAvatarInput struct {
	Name            string `json:"name"`
	Enabled         *bool  `json:"enabled"`
	ExpectedVersion int64  `json:"expected_version"`
}

// CreateWordInput 创建一个立即参与随机昵称抽取的词条。
type CreateWordInput struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// EditWordInput 修改词条文本与启用状态。
type EditWordInput struct {
	Text            string `json:"text"`
	Enabled         *bool  `json:"enabled"`
	ExpectedVersion int64  `json:"expected_version"`
}

// Scanner 审核已规范化的图片，不接收客户上传的原始编码。
type Scanner interface {
	Scan(context.Context, []byte, string) (bool, error)
}

// StudentAccess 复用学生资料保存入口的会话和动作判定。
type StudentAccess func(context.Context, string, string) error

// Service 借用运行数据库、图片扫描器与学生授权边界。
type Service struct {
	db      *gorm.DB
	engine  *command.Engine
	config  command.Config
	logger  *zap.Logger
	scanner Scanner
	student StudentAccess
	slots   chan struct{}
}

// New 构造随机资料服务，不访问外部供应商或修改数据库。
func New(db *gorm.DB, logger *zap.Logger, cfg command.Config, scanner Scanner, student StudentAccess) (*Service, error) {
	if scanner == nil || student == nil {
		return nil, command.ErrInvalid
	}
	engine, err := command.New(db, logger, cfg)
	if err != nil {
		return nil, err
	}
	return &Service{
		db:      db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}),
		engine:  engine,
		config:  cfg,
		logger:  logger,
		scanner: scanner,
		student: student,
		slots:   make(chan struct{}, 2),
	}, nil
}

func imagePath(id string) string { return "/random-profiles/images/" + id }

func avatarView(row entity.RandomProfileAvatar) Avatar {
	return Avatar{
		ID: row.ID, Name: row.Name, Enabled: row.Enabled, Version: row.Version,
		UpdatedAt: row.UpdatedAt, ImagePath: imagePath(row.ImageID),
	}
}

func wordView(row entity.RandomProfileWord) Word {
	return Word{ID: row.ID, Text: row.Text, Enabled: row.Enabled, Version: row.Version, UpdatedAt: row.UpdatedAt}
}

func canonicalID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id.String() == s
}

func validLabel(s string, limit int) bool {
	return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit &&
		strings.IndexFunc(s, unicode.IsControl) == -1
}

func report(s *Service, operation, rid string, started time.Time, err error) {
	result := "success"
	if err != nil {
		result = "failure"
	}
	s.logger.Info("随机资料操作完成", zap.String("operation", operation),
		zap.String("request_id", rid), zap.String("result", result), zap.Duration("duration", time.Since(started)))
}
