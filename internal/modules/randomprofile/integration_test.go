package randomprofile_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/modules/randomprofile"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

type scanner struct {
	result atomic.Int32
}

func (s *scanner) Scan(context.Context, []byte, string) (bool, error) {
	switch s.result.Load() {
	case 1:
		return false, nil
	case 2:
		return false, command.ErrUnavailable
	}
	return true, nil
}

func pngBytes(t *testing.T, r uint8) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 32, 32))
	pixels.SetRGBA(0, 0, color.RGBA{R: r, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, pixels); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPostgresRandomProfiles 验证由命令事务保护的随机资料配置、素材和回执。
func TestPostgresRandomProfiles(t *testing.T) {
	dsn := testconfig.Value("resource_test_dsn")
	if dsn == "" {
		t.Skip("需要隔离 PostgreSQL 测试配置")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	want := func(err, expected error) {
		t.Helper()
		if !errors.Is(err, expected) {
			t.Fatalf("返回 %v，期望 %v", err, expected)
		}
	}
	open := func(dsn string) *gorm.DB {
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		must(err)
		return db
	}
	parent := open(dsn)
	parentPool, err := parent.DB()
	must(err)
	defer parentPool.Close()
	var base string
	must(parent.Raw("SELECT current_database()").Scan(&base).Error)
	if base != "liyuanci" && base != "postgres" {
		t.Fatal("非测试父库")
	}
	name := "randomprofile_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(parent.Exec("CREATE DATABASE " + name).Error)
	defer func() { must(parent.Exec("DROP DATABASE " + name).Error) }()
	targetDSN := strings.Replace(dsn, "dbname="+base+" ", "dbname="+name+" ", 1)
	if targetDSN == dsn {
		t.Fatal("测试连接缺少数据库名称")
	}
	db := open(targetDSN)
	pool, err := db.DB()
	must(err)
	defer pool.Close()

	ctx, log := context.Background(), zap.NewNop()
	must(migration.Run(ctx, db, log, "up"))
	authorizeStudent := func(_ context.Context, token, _ string) error {
		if token == "synthetic-student" {
			return nil
		}
		return command.ErrUnauthenticated
	}
	safetyScanner := &scanner{}
	service, err := randomprofile.New(db, log, command.Config{
		Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3,
	}, safetyScanner, authorizeStudent)
	must(err)

	session := func(role string) string {
		id, token, ref := uuid.NewString(), uuid.NewString(), uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		must(db.Create(&entity.AdminAccount{ID: id, CredentialRef: &ref}).Error)
		must(db.Create(&entity.AdminRoleBinding{AdminID: id, Role: role}).Error)
		must(db.Create(&entity.Session{
			ID: uuid.NewString(), TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour), AdminID: &id,
		}).Error)
		return token
	}
	admin, anotherAdmin, reviewer := session("super_admin"), session("super_admin"), session("review_operator")
	const rid = "randomprofile-test"
	initial, err := service.ReadAdmin(ctx, admin, rid)
	must(err)
	if len(initial.Avatars) != 4 || len(initial.Adjectives) < 2 || len(initial.Nouns) < 2 {
		t.Fatalf("缺少随机资料的初始化项：%+v", initial)
	}
	_, err = service.ReadAdmin(ctx, reviewer, rid)
	want(err, command.ErrForbidden)
	_, err = service.DrawAdmin(ctx, reviewer, rid, randomprofile.DrawInput{})
	want(err, command.ErrForbidden)
	_, err = service.DrawStudent(ctx, reviewer, rid, randomprofile.DrawInput{})
	want(err, command.ErrUnauthenticated)
	drawn, err := service.DrawStudent(ctx, "synthetic-student", rid, randomprofile.DrawInput{})
	must(err)
	if !drawn.CanReroll || !strings.Contains(drawn.Nickname, "的") {
		t.Fatalf("初始化结果不可用：%+v", drawn)
	}

	original := initial.Avatars[0]
	makeImageID := func(avatar randomprofile.Avatar) string {
		return strings.TrimPrefix(avatar.ImagePath, "/random-profiles/images/")
	}
	oldImage, err := service.Image(ctx, makeImageID(original), rid)
	must(err)
	if _, err := png.Decode(bytes.NewReader(oldImage)); err != nil {
		t.Fatal("初始化图不是有效 PNG", err)
	}
	safetyScanner.result.Store(1)
	input := pngBytes(t, 88)
	_, err = service.ReplaceAvatarImage(ctx, admin, uuid.NewString(), rid, original.ID, original.Version, "image/png", bytes.NewReader(input))
	want(err, randomprofile.ErrImageRejected)
	afterBlocked, err := service.ReadAdmin(ctx, admin, rid)
	must(err)
	for _, avatar := range afterBlocked.Avatars {
		if avatar.ID == original.ID && avatar != original {
			t.Fatal("扫描不通过时仍替换了旧图")
		}
	}
	safetyScanner.result.Store(0)
	key := uuid.NewString()
	replaced, err := service.ReplaceAvatarImage(ctx, admin, key, rid, original.ID, original.Version, "image/png", bytes.NewReader(input))
	must(err)
	if replaced.Version != original.Version+1 || replaced.ImagePath == original.ImagePath || replaced.Enabled != original.Enabled {
		t.Fatalf("替换图片没有创建新不可变引用：%+v", replaced)
	}
	stillOld, err := service.Image(ctx, makeImageID(original), rid)
	must(err)
	if !bytes.Equal(stillOld, oldImage) {
		t.Fatal("已发出的旧图地址发生变化")
	}
	safetyScanner.result.Store(2)
	replayed, err := service.ReplaceAvatarImage(ctx, admin, key, rid, original.ID, original.Version, "image/png", bytes.NewReader(input))
	must(err)
	if replayed != replaced {
		t.Fatalf("重复命令未返回已确认的快照：%+v", replayed)
	}
	safetyScanner.result.Store(0)
	newAvatar, err := service.CreateAvatar(ctx, admin, uuid.NewString(), rid, "新头像", "image/png", bytes.NewReader(input))
	must(err)
	if !newAvatar.Enabled || newAvatar.Version != 1 {
		t.Fatalf("新上传头像未默认启用：%+v", newAvatar)
	}
	trueValue, falseValue := true, false
	_, err = service.EditAvatar(ctx, admin, uuid.NewString(), rid, original.ID, randomprofile.EditAvatarInput{
		Name: original.Name, Enabled: &trueValue, ExpectedVersion: original.Version,
	})
	want(err, command.ErrVersionConflict)

	word, err := service.CreateWord(ctx, admin, uuid.NewString(), rid, randomprofile.CreateWordInput{
		Kind: "adjective", Text: "  合成新词  ",
	})
	must(err)
	if !word.Enabled || word.Text != "合成新词" {
		t.Fatalf("新词未按预期启用：%+v", word)
	}
	newNoun, err := service.CreateWord(ctx, admin, uuid.NewString(), rid, randomprofile.CreateWordInput{
		Kind: "noun", Text: "合成名词",
	})
	must(err)
	if !newNoun.Enabled {
		t.Fatalf("新名词未默认启用：%+v", newNoun)
	}
	_, err = service.CreateWord(ctx, admin, uuid.NewString(), rid, randomprofile.CreateWordInput{
		Kind: "adjective", Text: "合成新词",
	})
	want(err, randomprofile.ErrDuplicateWord)
	longWord := strings.Repeat("鲤", 38)
	_, err = service.CreateWord(ctx, admin, uuid.NewString(), rid, randomprofile.CreateWordInput{
		Kind: "adjective", Text: longWord,
	})
	want(err, randomprofile.ErrNicknameTooLong)
	_, err = service.EditWord(ctx, admin, uuid.NewString(), rid, word.ID, randomprofile.EditWordInput{
		Text: longWord, Enabled: &trueValue, ExpectedVersion: word.Version,
	})
	want(err, randomprofile.ErrNicknameTooLong)

	screen, err := service.ReadAdmin(ctx, admin, rid)
	must(err)
	for _, item := range screen.Adjectives {
		if item.Text == longWord {
			t.Fatal("超长组合的词条被写入")
		}
	}
	var left []randomprofile.Avatar
	for _, avatar := range screen.Avatars {
		if avatar.Enabled {
			left = append(left, avatar)
		}
	}
	for len(left) > 2 {
		last := left[len(left)-1]
		left = left[:len(left)-1]
		_, err = service.EditAvatar(ctx, admin, uuid.NewString(), rid, last.ID, randomprofile.EditAvatarInput{
			Name: last.Name, Enabled: &falseValue, ExpectedVersion: last.Version,
		})
		must(err)
	}
	results := make(chan error, 2)
	for i, actor := range []string{admin, anotherAdmin} {
		go func(item randomprofile.Avatar, actor string) {
			_, failure := service.EditAvatar(ctx, actor, uuid.NewString(), rid, item.ID, randomprofile.EditAvatarInput{
				Name: item.Name, Enabled: &falseValue, ExpectedVersion: item.Version,
			})
			results <- failure
		}(left[i], actor)
	}
	first, second := <-results, <-results
	if !((first == nil && errors.Is(second, randomprofile.ErrPoolEmpty)) ||
		(second == nil && errors.Is(first, randomprofile.ErrPoolEmpty))) {
		t.Fatalf("并发停用最后两张头像没有保持一个可用项：%v / %v", first, second)
	}
	var actions int64
	must(db.Model(&entity.RandomProfileAction{}).Count(&actions).Error)
	// 一次换图、三次新增、四次成功停用各写入一条审计记录。
	if actions != 8 {
		t.Fatalf("审计记录数不符：%d", actions)
	}
}
