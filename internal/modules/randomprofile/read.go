package randomprofile

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// 素材写入统一持有同一事务锁，保证跨池约束和读取快照一致。
// 使用独立的两参数锁命名空间，避免与现有单参数迁移及身份锁交叉。
func lockCatalog(db *gorm.DB, shared bool) error {
	if shared {
		return db.Exec("SELECT pg_catalog.pg_advisory_xact_lock_shared(352009, 1)").Error
	}
	return db.Exec("SELECT pg_catalog.pg_advisory_xact_lock(352009, 1)").Error
}

func listCatalog(db *gorm.DB, enabledOnly bool) (Catalog, error) {
	avatars := make([]entity.RandomProfileAvatar, 0)
	words := make([]entity.RandomProfileWord, 0)
	if err := db.Where("(? OR enabled)", !enabledOnly).Order("updated_at DESC, id").Find(&avatars).Error; err != nil {
		return Catalog{}, command.ErrUnavailable
	}
	if err := db.Where("(? OR enabled)", !enabledOnly).Order("updated_at DESC, id").Find(&words).Error; err != nil {
		return Catalog{}, command.ErrUnavailable
	}
	out := Catalog{Avatars: make([]Avatar, 0, len(avatars)), Adjectives: make([]Word, 0), Nouns: make([]Word, 0)}
	for _, row := range avatars {
		out.Avatars = append(out.Avatars, avatarView(row))
	}
	for _, row := range words {
		if row.Kind == adjective {
			out.Adjectives = append(out.Adjectives, wordView(row))
		} else if row.Kind == noun {
			out.Nouns = append(out.Nouns, wordView(row))
		}
	}
	return out, nil
}

// adminRead 对有效后台账号、岗位和会话代次复核后，再读取配置或放行检查。
func (s *Service) adminRead(ctx context.Context, token string, run func(*gorm.DB) error) error {
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	if token == "" {
		return command.ErrUnauthenticated
	}
	err := s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		hash := sha256.Sum256([]byte(token))
		var session entity.Session
		err := db.Where("token_hash=? AND subject_kind='admin'", hash[:]).Take(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return command.ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		plan, err := command.LockPlan(ctx, db, command.Discovery{Admins: []string{session.SubjectID}}, s.config.LockTimeout)
		if err != nil {
			return err
		}
		var account entity.AdminAccount
		err = db.Where("id=? AND status='active'", session.SubjectID).Take(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return command.ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		err = db.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND revoked_at IS NULL AND expires_at>? AND auth_epoch=?", session.ID, plan.Now(), account.AuthEpoch).
			Take(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return command.ErrUnauthenticated
		}
		if err != nil {
			return err
		}
		var count int64
		if err = db.Model(&entity.AdminRoleBinding{}).Where("admin_id=? AND role='super_admin'", account.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return command.ErrForbidden
		}
		if run == nil {
			return nil
		}
		return run(db)
	})
	return readError(err)
}

func readError(err error) error {
	for _, known := range []error{command.ErrUnauthenticated, command.ErrForbidden, command.ErrNotFound, command.ErrRetry} {
		if errors.Is(err, known) {
			return known
		}
	}
	if err != nil {
		return command.ErrUnavailable
	}
	return nil
}

// ReadAdmin 提供当前有效超管的一致素材列表。
func (s *Service) ReadAdmin(ctx context.Context, token, rid string) (out Catalog, err error) {
	start := time.Now()
	defer func() { report(s, "admin_list", rid, start, err) }()
	err = s.adminRead(ctx, token, func(db *gorm.DB) error {
		if e := lockCatalog(db, true); e != nil {
			return e
		}
		out, err = listCatalog(db, false)
		return err
	})
	return out, err
}

func validDraw(input DrawInput) bool {
	return (input.PreviousAvatarID == "" || canonicalID(input.PreviousAvatarID)) &&
		(input.PreviousNickname == "" || validLabel(input.PreviousNickname, maxNickname))
}

// DrawAdmin 只向有效超管试抽一组已启用素材。
func (s *Service) DrawAdmin(ctx context.Context, token, rid string, input DrawInput) (out Draw, err error) {
	start := time.Now()
	defer func() { report(s, "admin_draw", rid, start, err) }()
	if !validDraw(input) {
		return out, command.ErrInvalid
	}
	err = s.adminRead(ctx, token, func(db *gorm.DB) error {
		out, err = drawCatalog(db, input)
		return err
	})
	return out, err
}

// DrawStudent 复用本人资料授权后从当前启用配置中抽取，不修改账号。
func (s *Service) DrawStudent(ctx context.Context, token, rid string, input DrawInput) (out Draw, err error) {
	start := time.Now()
	defer func() { report(s, "student_draw", rid, start, err) }()
	if !validDraw(input) {
		return out, command.ErrInvalid
	}
	if err = s.student(ctx, token, rid); err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		out, err = drawCatalog(db, input)
		return err
	})
	return out, readError(err)
}

func drawCatalog(db *gorm.DB, input DrawInput) (Draw, error) {
	if err := lockCatalog(db, true); err != nil {
		return Draw{}, err
	}
	catalog, err := listCatalog(db, true)
	if err != nil {
		return Draw{}, err
	}
	return pick(catalog, input)
}

func pick(catalog Catalog, input DrawInput) (Draw, error) {
	avatars, adjectives, nouns := catalog.Avatars, catalog.Adjectives, catalog.Nouns
	if len(avatars) == 0 || len(adjectives) == 0 || len(nouns) == 0 {
		return Draw{}, command.ErrUnavailable
	}
	previous := -1
	if len(avatars) > 1 {
		for i, avatar := range avatars {
			if avatar.ID == input.PreviousAvatarID {
				previous = i
				break
			}
		}
	}
	a := avatars[chooseIndex(len(avatars), previous)]
	i, j := rand.IntN(len(adjectives)), rand.IntN(len(nouns))
	nickname := adjectives[i].Text + "的" + nouns[j].Text
	if len(avatars) == 1 && nickname == input.PreviousNickname {
		// 只有一张头像时，排除拼接后与上一组相同的词对。
		if ai, nj, ok := pickDifferentWords(adjectives, nouns, nickname); ok {
			nickname = adjectives[ai].Text + "的" + nouns[nj].Text
		}
	}
	return Draw{
		AvatarID: a.ID, ImagePath: a.ImagePath, Nickname: nickname,
		CanReroll: len(avatars) > 1 || len(adjectives) > 1 || len(nouns) > 1,
	}, nil
}

func chooseIndex(count, excluded int) int {
	if excluded < 0 {
		return rand.IntN(count)
	}
	index := rand.IntN(count - 1)
	if index >= excluded {
		index++
	}
	return index
}

func pickDifferentWords(adjectives, nouns []Word, previous string) (int, int, bool) {
	nounIndices := make(map[string]int, len(nouns))
	for i, word := range nouns {
		nounIndices[word.Text] = i
	}
	// 每个形容词至多排除一个名词。按剩余词对数量抽样可避免单头像时偏向某个分类。
	excluded := make([]int, len(adjectives))
	count := len(adjectives) * len(nouns)
	for i, word := range adjectives {
		j, exists := nounIndices[strings.TrimPrefix(previous, word.Text+"的")]
		excluded[i] = -1
		if strings.HasPrefix(previous, word.Text+"的") && exists {
			excluded[i] = j
			count--
		}
	}
	if count == 0 {
		return 0, 0, false
	}
	index := rand.IntN(count)
	for i, forbidden := range excluded {
		size := len(nouns)
		if forbidden >= 0 {
			size--
		}
		if index < size {
			if forbidden >= 0 && index >= forbidden {
				index++
			}
			return i, index, true
		}
		index -= size
	}
	return 0, 0, false
}

// Image 仅以服务端生成的标识读取已检查并保持不可变的 PNG 字节。
func (s *Service) Image(ctx context.Context, id, rid string) (_ []byte, err error) {
	start := time.Now()
	defer func() { report(s, "image_read", rid, start, err) }()
	if !canonicalID(id) {
		return nil, command.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	var row entity.RandomProfileImage
	err = s.db.WithContext(ctx).Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, command.ErrNotFound
	}
	if err != nil {
		return nil, command.ErrUnavailable
	}
	return row.PNG, nil
}
