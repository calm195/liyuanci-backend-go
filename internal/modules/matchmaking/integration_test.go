package matchmaking_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
	"github.com/calm195/liyuanci-backend-go/internal/testconfig"
)

type syntheticKeys struct{}

func (syntheticKeys) Wrap(_ context.Context, value []byte) (string, []byte, error) {
	return "synthetic", append([]byte(nil), value...), nil
}

func (syntheticKeys) Unwrap(_ context.Context, _ string, value []byte) ([]byte, error) {
	return append([]byte(nil), value...), nil
}

type actor struct{ id, token string }

type exchange struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

type fixture struct {
	t        *testing.T
	db       *gorm.DB
	service  *matchmaking.Service
	server   *httptest.Server
	admin    actor
	captures []exchange
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dsn := testconfig.Value("matchmaking_test_dsn")
	if dsn == "" {
		t.Skip("需显式隔离 PostgreSQL 测试配置")
	}
	open := func(value string) *gorm.DB {
		t.Helper()
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		must(t, err)
		pool, err := db.DB()
		must(t, err)
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	parent := open(dsn)
	var base string
	must(t, parent.Raw("SELECT current_database()").Scan(&base).Error)
	if base != "postgres" && base != "liyuanci" && base != "whisper_test" {
		t.Fatal("拒绝非测试父库")
	}
	name := "matchmaking_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(t, parent.Exec("CREATE DATABASE "+name).Error)
	t.Cleanup(func() { must(t, parent.Exec("DROP DATABASE "+name).Error) })
	child := strings.Replace(dsn, "dbname="+base+" ", "dbname="+name+" ", 1)
	if child == dsn {
		t.Fatal("需要显式 dbname 参数及后续空格")
	}
	db, log := open(child), zap.NewNop()
	must(t, migration.Run(context.Background(), db, log, "up"))
	codec, err := securedata.New(syntheticKeys{}, log)
	must(t, err)
	service, err := matchmaking.New(db, log, codec, matchmaking.Config{
		Command: command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3},
		Grades:  func(context.Context) []string { return []string{"undergraduate_3"} },
		CheckText: func(_ context.Context, _ *gorm.DB, value string) error {
			if strings.Contains(value, "合成禁止词") {
				return command.ErrReviewPending
			}
			return nil
		},
	})
	must(t, err)
	router := httpapi.New(log, func(context.Context) error { return nil })
	must(t, httpapi.RegisterMatchmaking(router, service))
	f := &fixture{t: t, db: db, service: service, server: httptest.NewServer(router)}
	t.Cleanup(f.server.Close)
	f.admin = f.account("review_operator")
	return f
}

func (f *fixture) account(kind string) actor {
	f.t.Helper()
	a := actor{id: uuid.NewString(), token: uuid.NewString()}
	hash := sha256.Sum256([]byte(a.token))
	s := entity.Session{ID: uuid.NewString(), TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}
	if kind == "verified" || kind == "unverified" {
		must(f.t, f.db.Create(&entity.Account{ID: a.id, Nickname: "合成包分配参与者", Verification: kind}).Error)
		s.AccountID = &a.id
	} else {
		ref := uuid.NewString()
		must(f.t, f.db.Create(&entity.AdminAccount{ID: a.id, CredentialRef: &ref}).Error)
		must(f.t, f.db.Create(&entity.AdminRoleBinding{AdminID: a.id, Role: kind}).Error)
		s.AdminID = &a.id
	}
	must(f.t, f.db.Create(&s).Error)
	return a
}

func (f *fixture) call(method, path string, who actor, key string, input any, status int) json.RawMessage {
	f.t.Helper()
	var raw []byte
	if input != nil {
		var err error
		raw, err = json.Marshal(input)
		must(f.t, err)
	}
	req, err := http.NewRequest(method, f.server.URL+"/api/v1"+path, bytes.NewReader(raw))
	must(f.t, err)
	if who.token != "" {
		req.Header.Set("Authorization", "Bearer "+who.token)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := f.server.Client().Do(req)
	must(f.t, err)
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	must(f.t, err)
	f.captures = append(f.captures, exchange{Method: method, Path: path, Status: res.StatusCode, Body: body})
	if res.StatusCode != status {
		f.t.Fatalf("%s %s：状态 %d，期望 %d，响应 %s", method, path, res.StatusCode, status, body)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		f.t.Fatal("敏感响应缺少 no-store")
	}
	var envelope struct {
		Code  int             `json:"code"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error_code"`
	}
	must(f.t, json.Unmarshal(body, &envelope))
	if status == 200 && envelope.Code != 0 {
		f.t.Fatal("成功信封错误")
	}
	if status >= 400 && len(envelope.Data) > 0 {
		f.t.Fatal("失败响应泄露 data")
	}
	return envelope.Data
}

func decoded[T any](t *testing.T, value json.RawMessage) T {
	t.Helper()
	var out T
	must(t, json.Unmarshal(value, &out))
	return out
}

func profile(name, gender string) matchmaking.Profile {
	return matchmaking.Profile{Name: name, Gender: gender, Education: "本科生", Grade: "undergraduate_3", Campus: "wushan", MBTI: "INFP", Age: 21, AgeMin: 18, AgeMax: 25, Height: 175, HeightMin: 150, HeightMax: 190, Weight: 130, WeightMin: 90, WeightMax: 160, Wechat: "synthetic_" + gender, Introduction: "喜欢阅读和散步", Consent: true}
}

func activityInput() matchmaking.ActivityInput {
	now := time.Now().UTC().Truncate(time.Second)
	return matchmaking.ActivityInput{Title: "合成冬日相遇", Description: "真实数据库与 HTTP 合成账号验收", Campuses: []string{"wushan", "university_town"}, RegistrationOpensAt: now.Add(-2 * time.Hour), RegistrationClosesAt: now.Add(time.Hour), ResultsAt: now.Add(2 * time.Hour), StartsAt: now.Add(3 * time.Hour), EndsAt: now.Add(96 * time.Hour), Days: 3}
}

func adminPath(id string) string   { return "/admin/matchmaking/activities/" + id }
func studentPath(id string) string { return "/matchmaking/activities/" + id }

func (f *fixture) workspace(id string) matchmaking.Workspace {
	f.t.Helper()
	return decoded[matchmaking.Workspace](f.t, f.call("GET", adminPath(id)+"?page=1&limit=20", f.admin, "", nil, 200))
}

func (f *fixture) detail(id string, who actor) matchmaking.Detail {
	f.t.Helper()
	return decoded[matchmaking.Detail](f.t, f.call("GET", studentPath(id), who, "", nil, 200))
}

func (f *fixture) command(id, action string, want int) {
	f.t.Helper()
	f.call("POST", adminPath(id)+"/"+action, f.admin, uuid.NewString(), matchmaking.VersionInput{ExpectedVersion: f.workspace(id).Activity.Version, Reason: "合成运营操作"}, want)
}

func (f *fixture) enroll(id string, who actor, p matchmaking.Profile, version int64) string {
	f.t.Helper()
	current := decoded[matchmaking.AccountProfile](f.t, f.call("GET", "/matchmaking/profile", who, "", nil, 200))
	f.call("PUT", "/matchmaking/profile", who, uuid.NewString(), matchmaking.ProfileInput{ExpectedVersion: current.Version, Profile: p}, 200)
	return decoded[matchmaking.Receipt](f.t, f.call("PUT", studentPath(id)+"/enrollment", who, uuid.NewString(), matchmaking.EnrollmentInput{ExpectedVersion: version, ProfileVersion: current.Version + 1, Consent: true}, 200)).ID
}

func (f *fixture) review(id, enrollment string, version int64) {
	f.t.Helper()
	f.call("POST", adminPath(id)+"/enrollments/"+enrollment+"/review", f.admin, uuid.NewString(), matchmaking.ReviewInput{ExpectedVersion: version, Decision: "approved"}, 200)
}

func (f *fixture) template(id string, items []matchmaking.Template, want int) {
	f.t.Helper()
	f.call("PUT", adminPath(id)+"/templates", f.admin, uuid.NewString(), matchmaking.TemplatesInput{ExpectedVersion: f.workspace(id).Activity.Version, Items: items}, want)
}

func (f *fixture) unpublished(id string) {
	f.t.Helper()
	var tasks, published int64
	must(f.t, f.db.Model(&entity.MatchmakingTask{}).Where("pair_id IN (SELECT id FROM public.matchmaking_pairs WHERE activity_id=?)", id).Count(&tasks).Error)
	must(f.t, f.db.Model(&entity.MatchmakingPair{}).Where("activity_id=? AND status='published'", id).Count(&published).Error)
	if tasks != 0 || published != 0 {
		f.t.Fatalf("失败批次留下任务或已发布结果：%d/%d", tasks, published)
	}
}

// TestPostgresHTTP 使用本轮原生PG子库、真实HTTP与命令事务；账号、词库检查和加密供应商为合成夹具，不冒称RLS/真实供应商验收。
func TestPostgresHTTP(t *testing.T) {
	f := setup(t)
	defer func() {
		if path := testconfig.Value("matchmaking_http_capture"); path != "" && !t.Failed() {
			body, err := json.MarshalIndent(f.captures, "", "  ")
			must(t, err)
			must(t, os.WriteFile(path, body, 0600))
		}
	}()
	a, b, c, d := f.account("verified"), f.account("verified"), f.account("verified"), f.account("verified")
	outsider, crisis := f.account("unverified"), f.account("crisis_operator")
	input := activityInput()
	blockedInput := input
	blockedInput.Title = "合成禁止词"
	f.call("POST", "/admin/matchmaking/activities", f.admin, "blocked-activity", blockedInput, 409)
	id := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "create", input, 200)).ID
	if again := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "create", input, 200)).ID; again != id {
		t.Fatal("原请求重放创建重复活动")
	}
	changed := input
	changed.Title = "同键不同活动"
	f.call("POST", "/admin/matchmaking/activities", f.admin, "create", changed, 409)
	f.call("GET", adminPath(id), crisis, "", nil, 403)
	f.call("GET", adminPath(id), a, "", nil, 403)
	f.call("GET", studentPath(id), a, "", nil, 404)
	f.call("GET", "/admin/matchmaking/activities?page=1&page=2", f.admin, "", nil, 400)
	f.call("GET", "/admin/matchmaking/activities?limit=51", f.admin, "", nil, 400)
	f.call("POST", adminPath(id)+"/publish", f.admin, "duplicate-field", json.RawMessage(`{"expected_version":1,"expected_version":1}`), 400)
	f.call("POST", adminPath(id)+"/publish", f.admin, "case-alias", json.RawMessage(`{"expected_version":1,"EXPECTED_VERSION":1}`), 400)
	f.call("PUT", studentPath(id)+"/enrollment", a, "nested-case", json.RawMessage(`{"expected_version":0,"profile":{"name":"合成","NAME":"覆盖"}}`), 400)
	f.call("PUT", adminPath(id), f.admin, "unknown-field", json.RawMessage(`{"id":"forbidden"}`), 400)
	input.ExpectedVersion, input.Title = 1, "合成冬日相遇已调整"
	f.call("PUT", adminPath(id), f.admin, "edit", input, 200)
	f.call("PUT", adminPath(id), f.admin, "edit", input, 200)
	f.call("PUT", adminPath(id), f.admin, "stale-edit", input, 409)
	f.command(id, "publish", 200)
	f.call("GET", "/matchmaking/activities?page=1&limit=20", a, "", nil, 200)
	f.call("GET", "/admin/matchmaking/activities?page=1&limit=20", f.admin, "", nil, 200)
	f.call("PUT", studentPath(id)+"/enrollment", outsider, "unverified", matchmaking.EnrollmentInput{ProfileVersion: 1, Consent: true}, 403)
	pa := profile("合成甲", "male")
	blockedProfile := pa
	blockedProfile.Introduction = "合成禁止词"
	f.call("PUT", "/matchmaking/profile", a, "blocked-profile", matchmaking.ProfileInput{Profile: blockedProfile}, 409)
	aID := f.enroll(id, a, pa, 0)
	before := f.detail(id, a)
	if before.Enrollment == nil || before.Enrollment.Eligible || before.Enrollment.Status != "pending" {
		t.Fatal("新报名没有进入待审核")
	}
	f.review(id, aID, 1)
	pa.Introduction = "修改后的合成介绍"
	if next := f.enroll(id, a, pa, 2); next != aID {
		t.Fatal("修改报名生成另一记录")
	}
	if pending := f.detail(id, a).Enrollment; pending == nil || pending.Status != "pending" || pending.Version != 3 {
		t.Fatal("修改资料未重新审核")
	}
	f.call("POST", adminPath(id)+"/enrollments/"+aID+"/review", f.admin, "stale-review", matchmaking.ReviewInput{ExpectedVersion: 2, Decision: "approved"}, 409)
	f.review(id, aID, 3)
	pb := profile("合成乙", "female")
	pb.Campus, pb.MBTI, pb.Wechat = "international", "", ""
	bID := f.enroll(id, b, pb, 0)
	f.call("PUT", "/matchmaking/profile", b, "personality-change", matchmaking.ProfileInput{ExpectedVersion: 1, Profile: matchmaking.Profile{MBTI: "INTJ"}}, 200)
	current := decoded[matchmaking.AccountProfile](t, f.call("GET", "/matchmaking/profile", b, "", nil, 200))
	if current.Profile == nil || current.Ready || current.Profile.MBTI != "INTJ" || f.detail(id, b).Enrollment.Profile.MBTI != "" {
		t.Fatal("人格标签更新改动报名快照或部分资料可直接报名")
	}
	f.call("PUT", studentPath(id)+"/enrollment", b, "stale-profile", matchmaking.EnrollmentInput{ExpectedVersion: 1, ProfileVersion: 1, Consent: true}, 409)
	must(t, f.db.Model(&entity.Account{}).Where("id=?", b.id).Update("verification", "unverified").Error)
	f.call("POST", adminPath(id)+"/enrollments/"+bID+"/review", f.admin, "ineligible-review", matchmaking.ReviewInput{ExpectedVersion: 1, Decision: "approved"}, 409)
	if f.workspace(id).Activity.ID != id {
		t.Fatal("报名资格拒绝不应使运营会话失权")
	}
	must(t, f.db.Model(&entity.Account{}).Where("id=?", b.id).Update("verification", "verified").Error)
	cID := f.enroll(id, c, profile("合成丙", "male"), 0)
	dID := f.enroll(id, d, profile("合成丁", "female"), 0)
	for _, enrollment := range []string{bID, cID, dID} {
		f.review(id, enrollment, 1)
	}
	var encrypted entity.MatchmakingEnrollment
	must(t, f.db.Where("id=?", aID).Take(&encrypted).Error)
	if bytes.Contains(encrypted.ProfileCipher, []byte(pa.Wechat)) || bytes.Contains(encrypted.ProfileCipher, []byte(pa.Name)) {
		t.Fatal("报名个人资料未加密")
	}
	items := []matchmaking.Template{{Kind: "online", Body: "首日合成问候", FixedDay: 1}, {Kind: "offline", Body: "次日合成散步", FixedDay: 2}, {Kind: "online", Body: "随机合成分享"}}
	f.template(id, []matchmaking.Template{{Kind: "online", Body: "合成禁止词"}}, 409)
	f.template(id, items[:1], 200)
	// 遗漏或显式null不能冒充清空题库，空数组才是明确的清空请求。
	templateVersion := f.workspace(id).Activity.Version
	for index, body := range []map[string]any{
		{"expected_version": templateVersion},
		{"expected_version": templateVersion, "items": nil},
	} {
		f.call("PUT", adminPath(id)+"/templates", f.admin, fmt.Sprintf("missing-templates-%d", index), body, 400)
	}
	if current := f.workspace(id); current.Activity.Version != templateVersion || len(current.Templates) != 1 {
		t.Fatal("无效模板输入修改了活动版本或清空了题库")
	}
	f.template(id, []matchmaking.Template{}, 200)
	if current := f.workspace(id); len(current.Templates) != 0 {
		t.Fatal("显式空数组未能清空尚未发布的题库")
	}
	f.template(id, items[:1], 200)
	otherID := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "other-activity", activityInput(), 200)).ID
	f.command(otherID, "publish", 200)
	otherStudent := f.account("verified")
	foreignEnrollment := f.enroll(otherID, otherStudent, profile("合成异活动报名", "female"), 0)
	f.review(otherID, foreignEnrollment, 1)
	f.call("POST", adminPath(id)+"/enrollments/"+foreignEnrollment+"/review", f.admin, "foreign-review", matchmaking.ReviewInput{ExpectedVersion: 2, Decision: "approved"}, 404)
	now := time.Now().UTC().Truncate(time.Second)
	// 只在隔离夹具调整活动时钟边界，业务请求仍走真实数据库时间判断。
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).Updates(map[string]any{"registration_closes_at": now.Add(-30 * time.Minute)}).Error)
	f.call("PUT", studentPath(id)+"/enrollment", a, "after-close", matchmaking.EnrollmentInput{ExpectedVersion: 4, ProfileVersion: 2, Consent: true}, 409)
	candidates := decoded[struct {
		Items []matchmaking.Candidate `json:"items"`
	}](t, f.call("GET", adminPath(id)+"/candidates/"+aID, f.admin, "", nil, 200))
	if len(candidates.Items) != 2 || candidates.Items[0].Enrollment.Profile == nil {
		t.Fatal("推荐候选未正确投影")
	}
	pairInput := matchmaking.PairInput{ExpectedVersion: f.workspace(id).Activity.Version, First: aID, Second: bID}
	f.call("POST", adminPath(id)+"/pairs", f.admin, "foreign-pair", matchmaking.PairInput{ExpectedVersion: pairInput.ExpectedVersion, First: aID, Second: foreignEnrollment}, 404)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for i := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := f.service.SetPair(context.Background(), f.admin.token, fmt.Sprintf("race-pair-%d", i), "synthetic-pair-race", id, pairInput)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, command.ErrVersionConflict) && !errors.Is(err, command.ErrConflict) {
			t.Fatal("并发配对异常", err)
		}
	}
	if success != 1 {
		t.Fatalf("并发配对应恰好成功一次，实际 %d", success)
	}
	var occupied int64
	must(t, f.db.Model(&entity.MatchmakingMember{}).Where("enrollment_id=? AND active", aID).Count(&occupied).Error)
	if occupied != 1 {
		t.Fatal("同一报名被重复占用")
	}
	f.call("POST", adminPath(id)+"/pairs", f.admin, "second-pair", matchmaking.PairInput{ExpectedVersion: f.workspace(id).Activity.Version, First: cID, Second: dID}, 200)
	f.call("POST", adminPath(id)+"/pairs", f.admin, "occupied-pair", matchmaking.PairInput{ExpectedVersion: f.workspace(id).Activity.Version, First: aID, Second: dID}, 409)
	if f.detail(id, a).Pair != nil {
		t.Fatal("未发布配对泄露给学生")
	}
	f.command(id, "results/publish", 409)
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).Update("results_at", now.Add(-20*time.Minute)).Error)
	f.command(id, "results/publish", 409)
	f.unpublished(id)
	f.template(id, items, 200)
	// 固定让最后一对失效，确保前一对已经尝试写入后仍整批回滚。
	var lastPair entity.MatchmakingPair
	must(t, f.db.Where("activity_id=?", id).Order("id DESC").First(&lastPair).Error)
	var lastMembers []entity.MatchmakingMember
	must(t, f.db.Where("pair_id=?", lastPair.ID).Order("enrollment_id").Find(&lastMembers).Error)
	if len(lastMembers) != 2 {
		t.Fatal("原子性夹具缺少成员")
	}
	lastBlock := entity.Block{OwnerID: lastMembers[0].AccountID, TargetID: lastMembers[1].AccountID}
	must(t, f.db.Create(&lastBlock).Error)
	f.command(id, "results/publish", 409)
	f.unpublished(id)
	must(t, f.db.Where("owner_id=? AND target_id=?", lastBlock.OwnerID, lastBlock.TargetID).Delete(&entity.Block{}).Error)
	publishInput := matchmaking.VersionInput{ExpectedVersion: f.workspace(id).Activity.Version}
	f.call("POST", adminPath(id)+"/results/publish", f.admin, "publish-snapshot", publishInput, 200)
	var originalTasks, replayTasks []entity.MatchmakingTask
	must(t, f.db.Order("id").Find(&originalTasks).Error)
	f.call("POST", adminPath(id)+"/results/publish", f.admin, "publish-snapshot", publishInput, 200)
	must(t, f.db.Order("id").Find(&replayTasks).Error)
	if len(originalTasks) != 6 || !reflect.DeepEqual(originalTasks, replayTasks) {
		t.Fatal("发布重放修改任务标识、正文或解锁快照")
	}
	prestart := f.detail(id, a)
	if prestart.Pair == nil || prestart.Pair.Accessible || prestart.Pair.Peer != nil || len(prestart.Tasks) != 0 {
		t.Fatal("活动开始前提前展示对方或任务")
	}
	f.template(id, items, 409)
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).Update("starts_at", now.Add(-10*time.Minute)).Error)
	// 已生成任务时间属于不可变快照，测试通过显式夹具把首项置于当前可查看边界。
	must(t, f.db.Model(&entity.MatchmakingTask{}).Where("day_index=1 AND pair_id IN (SELECT id FROM public.matchmaking_pairs WHERE activity_id=?)", id).Update("unlocks_at", now.Add(-time.Minute)).Error)
	active := f.detail(id, a)
	if active.Pair == nil || !active.Pair.Accessible || active.Pair.Peer == nil || len(active.Tasks) != 3 {
		t.Fatal("活动开始后未显示合法配对和任务")
	}
	if active.Tasks[0].Body != items[0].Body || !active.Tasks[0].CanOpen || active.Tasks[1].Body != "" || active.Tasks[1].Kind != "" || active.Tasks[1].CanOpen {
		t.Fatal("任务正文未遵守完整解锁时间")
	}
	task := active.Tasks[0].ID
	f.call("POST", studentPath(otherID)+"/tasks/"+task+"/open", a, "foreign-task", struct{}{}, 404)
	f.call("POST", studentPath(id)+"/tasks/"+active.Tasks[1].ID+"/open", a, "early-open", struct{}{}, 409)
	openResults := make(chan error, 2)
	for _, who := range []actor{a, b} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := f.service.OpenTask(context.Background(), who.token, "first-open", "synthetic-open-race", id, task)
			openResults <- err
		}()
	}
	wait.Wait()
	close(openResults)
	for err := range openResults {
		must(t, err)
	}
	opened := f.detail(id, a).Tasks[0].OpenedAt
	if opened == nil || f.detail(id, b).Tasks[0].OpenedAt == nil {
		t.Fatal("双方查看记录没有独立保存")
	}
	f.call("POST", studentPath(id)+"/tasks/"+task+"/open", a, "first-open", struct{}{}, 200)
	if again := f.detail(id, a).Tasks[0].OpenedAt; again == nil || !again.Equal(*opened) {
		t.Fatal("幂等重放修改首次查看时间")
	}
	var opens int64
	must(t, f.db.Model(&entity.MatchmakingOpen{}).Where("task_id=?", task).Count(&opens).Error)
	if opens != 2 {
		t.Fatalf("双方查看记录应为两条，实际 %d", opens)
	}
	assertHidden := func() {
		t.Helper()
		value := f.detail(id, a)
		if value.Pair == nil || value.Pair.Accessible || value.Pair.Peer != nil || len(value.Tasks) != 0 {
			t.Fatal("当前资格失效后仍泄露对方或任务")
		}
	}
	must(t, f.db.Create(&entity.Block{OwnerID: b.id, TargetID: a.id}).Error)
	assertHidden()
	f.call("POST", studentPath(id)+"/tasks/"+task+"/open", a, "first-open", struct{}{}, 403)
	must(t, f.db.Where("owner_id=? AND target_id=?", b.id, a.id).Delete(&entity.Block{}).Error)
	starts := now.Add(-time.Minute)
	ends := starts.Add(7 * 24 * time.Hour)
	penalty := entity.Penalty{ID: uuid.NewString(), AccountID: b.id, Level: "general", ActionKind: "mute", ViolationRef: uuid.NewString(), ContentVersion: 1, StartsAt: starts, EndsAt: &ends, ReasonRef: uuid.NewString(), SourceKind: "direct"}
	must(t, f.db.Create(&penalty).Error)
	assertHidden()
	f.call("POST", studentPath(id)+"/tasks/"+task+"/open", a, "first-open", struct{}{}, 403)
	must(t, f.db.Model(&penalty).Update("revoked_at", time.Now()).Error)
	if !f.detail(id, a).Pair.Accessible {
		t.Fatal("恢复资格后仍无法读取配对")
	}
	// 受限账号仍可主动退出，不连带删除另一人的独立报名。
	withdrawPenalty := penalty
	withdrawPenalty.ID, withdrawPenalty.AccountID = uuid.NewString(), d.id
	withdrawPenalty.RevokedAt = nil
	must(t, f.db.Create(&withdrawPenalty).Error)
	withdrawInput := matchmaking.VersionInput{ExpectedVersion: 2, Reason: "本人退出"}
	beforeWithdraw := f.workspace(id).Activity.Version
	f.call("POST", studentPath(id)+"/withdraw", d, "restricted-withdraw", withdrawInput, 200)
	f.call("POST", studentPath(id)+"/withdraw", d, "restricted-withdraw", withdrawInput, 200)
	if after := f.workspace(id).Activity.Version; after != beforeWithdraw+1 {
		t.Fatalf("退出改变配对集合应仅增加一次活动版本：%d → %d", beforeWithdraw, after)
	}
	_, stalePublish := f.service.ActivityCommand(context.Background(), f.admin.token, "publish-before-withdraw", "synthetic-stale-publish", id, "results/publish", matchmaking.VersionInput{ExpectedVersion: beforeWithdraw})
	if !errors.Is(stalePublish, command.ErrVersionConflict) {
		t.Fatalf("退出后的旧活动快照应要求重新读取，实际：%v", stalePublish)
	}
	withdrawn, survivor := f.detail(id, d), f.detail(id, c)
	if withdrawn.Enrollment == nil || withdrawn.Enrollment.Status != "withdrawn" || survivor.Enrollment == nil || survivor.Enrollment.Status != "approved" || survivor.Pair == nil || survivor.Pair.Status != "ended" || survivor.Pair.Peer != nil {
		t.Fatal("退出未解除关系或影响幸存报名")
	}
	var remainingSlots int64
	must(t, f.db.Model(&entity.MatchmakingMember{}).Where("enrollment_id IN ? AND active", []string{cID, dID}).Count(&remainingSlots).Error)
	if remainingSlots != 0 {
		t.Fatal("退出未释放双方配对名额")
	}
	// 结束统计面向所有学生，退出报名和已解除配对不计入。
	if current := f.detail(id, a).Activity; current.Summary != nil {
		t.Fatal("进行中的活动提前公开统计")
	}
	var activityBeforeEnd entity.MatchmakingActivity
	must(t, f.db.Where("id=?", id).Take(&activityBeforeEnd).Error)
	originalEnd := activityBeforeEnd.EndsAt
	must(t, f.db.Model(&activityBeforeEnd).Update("ends_at", now.Add(-time.Minute)).Error)
	wantSummary := matchmaking.ActivitySummary{EnrollmentCount: 3, SuccessfulPairCount: 1}
	endedDetail := f.detail(id, outsider).Activity
	if endedDetail.Phase != "ended" || endedDetail.Summary == nil || *endedDetail.Summary != wantSummary {
		t.Fatalf("结束详情统计错误：%+v", endedDetail.Summary)
	}
	endedList := decoded[matchmaking.Page](t, f.call("GET", "/matchmaking/activities", outsider, "", nil, 200))
	found := false
	for _, item := range endedList.Items {
		if item.ID == id {
			found = item.Summary != nil && *item.Summary == wantSummary
		} else if item.Summary != nil {
			t.Fatal("未结束活动返回了统计")
		}
	}
	if !found {
		t.Fatal("结束列表统计与详情不一致")
	}
	must(t, f.db.Model(&activityBeforeEnd).Update("ends_at", originalEnd).Error)
	must(t, f.db.Model(&entity.Account{}).Where("id=?", b.id).Updates(map[string]any{"lifecycle": "deleting", "auth_epoch": 2}).Error)
	assertHidden()
	f.call("GET", studentPath(id), b, "", nil, 401)
	redacted := f.workspace(id)
	for _, enrollment := range redacted.Enrollments {
		if enrollment.ID == bID && enrollment.Profile != nil {
			t.Fatal("后台仍展示注销账号的密文解密资料")
		}
	}
	// 未发布草稿取消后仍不能出现在学生列表或通过标识探测正文。
	draft := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "cancelled-draft", activityInput(), 200)).ID
	f.command(draft, "cancel", 200)
	f.call("GET", studentPath(draft), a, "", nil, 404)
	public := decoded[matchmaking.Page](t, f.call("GET", "/matchmaking/activities", a, "", nil, 200))
	for _, value := range public.Items {
		if value.ID == draft {
			t.Fatal("取消草稿出现在学生列表")
		}
	}
	// 已发布配对解除后模板继续冻结，不能改写历史规则。
	var first matchmaking.Pair
	for _, pair := range redacted.Pairs {
		if pair.ID == active.Pair.ID {
			first = pair
		}
	}
	if first.ID == "" {
		t.Fatal("缺少第一组配对")
	}
	f.call("POST", adminPath(id)+"/pairs/"+first.ID+"/end", f.admin, "end-first", matchmaking.VersionInput{ExpectedVersion: first.Version, Reason: "合成运营解除"}, 200)
	if ended := f.detail(id, a); ended.Pair == nil || ended.Pair.Status != "ended" || ended.Pair.Reason != "配对已解除" || ended.Pair.Peer != nil {
		t.Fatal("解除后投影错误或泄露运营原因")
	}
	f.template(id, items, 409)
	// 隐私清理可以移除全部配对；活动自身必须记住题库已经发布，不能依赖存活个人记录。
	must(t, f.db.Where("matchmaking_pair_id IN (SELECT id FROM public.matchmaking_pairs WHERE activity_id=?)", id).Delete(&entity.Conversation{}).Error)
	must(t, f.db.Where("activity_id=?", id).Delete(&entity.MatchmakingPair{}).Error)
	if cleared := f.workspace(id); cleared.PairTotal != 0 || !cleared.TemplatesLocked {
		t.Fatal("配对全部清理后错误解除了题库冻结")
	}
	f.template(id, items, 409)
	f.command(id, "cancel", 200)
	if cancelled := f.detail(id, a); cancelled.Pair != nil || len(cancelled.Tasks) != 0 {
		t.Fatal("配对清理并取消后仍返回关系或任务")
	}
}
