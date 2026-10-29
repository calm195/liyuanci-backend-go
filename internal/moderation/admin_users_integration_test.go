package moderation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/safety"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// TestAdminUsersQuery 覆盖REQ-022/016/018/019/021的受限PG身份、真实HTTP与合成账号。
// 不访问真实供应商或微信；单客户端测量不代替获确认并发负载下的性能验收。
func TestAdminUsersQuery(t *testing.T) {
	dsn := os.Getenv("APP_MODERATION_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PostgreSQL，使用make integration执行")
	}
	if !strings.Contains(dsn, "dbname=liyuanci ") {
		t.Fatal("需要隔离集成DSN")
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	open := func(value string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: value, PreferSimpleProtocol: true}), &gorm.Config{Logger: gormlog.Default.LogMode(gormlog.Silent)})
		must(e)
		pool, e := db.DB()
		must(e)
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	parent := open(dsn)
	database := "admin_users_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(parent.Exec("CREATE DATABASE " + database).Error)
	// 只清理本用例创建的数据库，连接在此前关闭，不删除开发卷或其他数据库。
	t.Cleanup(func() {
		if e := parent.Exec("DROP DATABASE " + database).Error; e != nil {
			t.Error(e)
		}
	})
	dsn = strings.Replace(dsn, "dbname=liyuanci ", "dbname="+database+" ", 1)
	root := open(dsn)
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)
	must(migration.Run(context.Background(), root, log, "up"))
	must(migration.ApplyPermissions(context.Background(), root, log))
	api := open(dsn + " options='-c role=liyuanci_admin_api'")
	codec, e := securedata.New(testKeys{}, log)
	must(e)
	service, e := safety.New(api, log, codec, safety.Config{Command: command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}, Key: []byte(strings.Repeat("u", 32)), MaxTextBytes: 65536})
	must(e)
	router := httpapi.New(log, nil)
	must(httpapi.RegisterSafety(router, service, service))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 15 * time.Second
	session := func(id string, admin bool) string {
		t.Helper()
		token := uuid.NewString()
		hash := sha256.Sum256([]byte(token))
		s := entity.Session{ID: uuid.NewString(), TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}
		if admin {
			s.AdminID = &id
		} else {
			s.AccountID = &id
		}
		must(root.Create(&s).Error)
		return token
	}
	admin := func(role string) (string, string) {
		t.Helper()
		id, ref := uuid.NewString(), uuid.NewString()
		must(root.Create(&entity.AdminAccount{ID: id, CredentialRef: &ref}).Error)
		if role != "" {
			must(root.Create(&entity.AdminRoleBinding{AdminID: id, Role: role}).Error)
		}
		return id, session(id, true)
	}
	_, token := admin("review_operator")
	_, crisisToken := admin("crisis_operator")
	_, superToken := admin("super_admin")
	_, emptyToken := admin("")
	now := time.Now().UTC().Truncate(time.Microsecond)
	account := func(name string) string {
		t.Helper()
		id := uuid.NewString()
		must(root.Create(&entity.Account{ID: id, Nickname: name, Verification: "verified", CreatedAt: now, Version: 7}).Error)
		return id
	}
	target, twin := account("校园小鲤鱼"), account("校园小鲤鱼")
	fish, wide, blank := account("LiFish%_\\'校园"), account("ÄＡFish"), account("\u3000\u0085")
	studentToken := session(target, false)
	// 受限后台只取得会话域布尔值，验证函数授权与RLS隔离同时生效。
	var liveStudent bool
	studentHash := sha256.Sum256([]byte(studentToken))
	must(api.Raw("SELECT public.is_live_student_session(decode(?,'hex'))", hex.EncodeToString(studentHash[:])).Scan(&liveStudent).Error)
	if !liveStudent {
		t.Fatal("有效学生会话域校验失败")
	}
	var studentRows int64
	must(api.Model(&entity.Session{}).Where("account_id=?", target).Count(&studentRows).Error)
	if studentRows != 0 {
		t.Fatal("后台连接绕过学生会话RLS")
	}
	var studentCanExecute bool
	must(root.Raw("SELECT has_function_privilege('liyuanci_api','public.is_live_student_session(bytea)','EXECUTE')").Scan(&studentCanExecute).Error)
	if studentCanExecute {
		t.Fatal("学生运行身份获得后台会话域探测权限")
	}
	call := func(t *testing.T, auth, path string, want int) map[string]any {
		t.Helper()
		req, e := http.NewRequest("GET", server.URL+"/api/v1/admin/users"+path, nil)
		must(e)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, e := client.Do(req)
		must(e)
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		must(e)
		if resp.StatusCode != want {
			t.Fatalf("状态%d期望%d，响应%s", resp.StatusCode, want, b)
		}
		var body map[string]any
		must(json.Unmarshal(b, &body))
		if resp.Header.Get("Cache-Control") != "no-store" || body["request_id"] != resp.Header.Get("X-Request-ID") {
			t.Fatal("响应头或请求ID错误")
		}
		if want != 200 {
			if body["code"] != float64(want) || body["error_code"] == nil || body["data"] != nil {
				t.Fatal("错误封装错误")
			}
			return body
		}
		if len(body) != 4 || body["code"] != float64(0) {
			t.Fatal("成功封装错误")
		}
		return body["data"].(map[string]any)
	}
	items := func(d map[string]any) []any { return d["items"].([]any) }
	ids := func(d map[string]any) []string {
		out := []string{}
		for _, r := range items(d) {
			out = append(out, r.(map[string]any)["id"].(string))
		}
		sort.Strings(out)
		return out
	}
	assertIDs := func(path string, want ...string) {
		t.Helper()
		got := ids(call(t, token, path, 200))
		sort.Strings(want)
		if !slices.Equal(got, want) {
			t.Fatalf("集合不符got%d want%d", len(got), len(want))
		}
	}
	t.Run("角色和严格参数", func(t *testing.T) {
		for _, auth := range []string{token, crisisToken, superToken} {
			call(t, auth, "", 200)
		}
		call(t, "", "", 401)
		call(t, "invalid", "", 401)
		call(t, studentToken, "", 403)
		call(t, emptyToken, "", 403)
		call(t, emptyToken, "/"+uuid.NewString(), 403)
		for _, q := range []string{"?limit=0", "?limit=51", "?cursor=", "?user_id=", "?user_id=invalid", "?verification=all", "?restriction=all", "?nickname=a&nickname=a", "?limit=1&limit=1", "?unknown=x", "?nickname=" + url.QueryEscape(strings.Repeat("鲤", 51))} {
			call(t, token, q, 400)
		}
		call(t, token, "/"+uuid.NewString(), 404)
		call(t, token, "/"+uuid.NewString()+"/penalties", 404)
		assertIDs("?user_id="+strings.ToUpper(target), target)
		assertIDs("?user_id=" + uuid.NewString())
	})
	t.Run("昵称与键集分页", func(t *testing.T) {
		assertIDs("?nickname="+url.QueryEscape("小鲤"), target, twin)
		assertIDs("?nickname=FISH", fish, wide)
		assertIDs("?nickname="+url.QueryEscape("%_\\'"), fish)
		assertIDs("?nickname=" + url.QueryEscape("ä"))
		assertIDs("?nickname="+url.QueryEscape("Ａ"), wide)
		assertIDs("?nickname=" + url.QueryEscape("未设置"))
		assertIDs("?nickname=" + url.QueryEscape(strings.Repeat("鲤", 50)))
		if call(t, token, "/"+blank, 200)["display_name"] != nil {
			t.Fatal("空白昵称不为null")
		}
		path := "?nickname=" + url.QueryEscape("小鲤") + "&limit=1"
		first := call(t, token, path, 200)
		cursor := first["next_cursor"].(string)
		second := call(t, token, path+"&cursor="+url.QueryEscape(cursor), 200)
		if len(items(second)) != 1 || second["next_cursor"] != nil || ids(first)[0] <= ids(second)[0] {
			t.Fatal("UUID并列排序错误")
		}
		for _, q := range []string{"?limit=1", "?nickname=" + url.QueryEscape("小鲤") + "&limit=2", "?nickname=fish&limit=1"} {
			call(t, token, q+"&cursor="+url.QueryEscape(cursor), 400)
		}
		call(t, superToken, path+"&cursor="+url.QueryEscape(cursor), 400)
		call(t, token, "/"+target+"/penalties?limit=1&cursor="+url.QueryEscape(cursor), 400)
		call(t, token, "?cursor=synthetic", 400)
		must(root.Model(&entity.Account{}).Where("id=?", twin).Update("nickname", "当前新昵称").Error)
		assertIDs("?nickname="+url.QueryEscape("小鲤"), target)
	})
	content := func(kind string) string {
		t.Helper()
		id := uuid.NewString()
		must(root.Create(&entity.ContentRegistry{ID: id, Kind: kind, EntityID: uuid.NewString(), ContentVersion: 1, AuthorID: target, SchoolDetectionState: "pending", SchoolDetectionVersion: "synthetic"}).Error)
		return id
	}
	ordinary, crisis, anonymous := content("wish"), content("wish"), content("whisper_post")
	must(root.Create(&entity.ContentRestriction{ID: uuid.NewString(), ContentID: crisis, Kind: "crisis", SourceRef: uuid.NewString(), Active: true}).Error)
	reportID := uuid.NewString()
	must(root.Create(&entity.Report{ID: reportID, ReporterID: twin, TargetKind: "wish", TargetID: uuid.NewString(), ContentID: &ordinary, TypeCode: "synthetic", Status: "resolved"}).Error)
	penalty := func(cid *string, kind string, start time.Time, ref string) entity.Penalty {
		t.Helper()
		p := entity.Penalty{ID: uuid.NewString(), AccountID: target, Level: map[string]string{"warning": "minor", "mute": "general", "ban": "severe"}[kind], ActionKind: kind, ViolationRef: ref, ContentID: cid, ContentVersion: 1, StartsAt: start, ReasonRef: uuid.NewString(), ReasonCipher: json.RawMessage(`{"sensitive":"自由理由不得输出"}`)}
		if kind == "mute" {
			end := start.Add(168 * time.Hour)
			p.EndsAt = &end
		}
		must(root.Create(&p).Error)
		return p
	}
	active := penalty(&ordinary, "mute", now.Add(-time.Hour), reportID)
	hidden := penalty(&crisis, "ban", now.Add(-30*time.Minute), uuid.NewString())
	anon := penalty(&anonymous, "warning", now.Add(-20*time.Minute), uuid.NewString())
	penalty(nil, "mute", now.Add(-8*24*time.Hour), uuid.NewString())
	future := penalty(nil, "ban", now.Add(time.Hour), uuid.NewString())
	appealID := uuid.NewString()
	must(root.Create(&entity.Appeal{ID: appealID, AccountID: target, PenaltyID: active.ID, BodyCipher: json.RawMessage(`{}`)}).Error)
	t.Run("安全投影与限制", func(t *testing.T) {
		d := call(t, token, "/"+target, 200)
		if d["account_version"] != float64(7) || d["restriction"] != "banned" || d["muted_until"] != nil || len(d) != 10 {
			t.Fatal("版本或限制错误")
		}
		if len(d["active_penalties"].([]any)) != 1 || len(d["allowed_actions"].([]any)) != 3 {
			t.Fatal("权限投影错误")
		}
		if len(call(t, token, "/"+twin, 200)["allowed_actions"].([]any)) != 1 {
			t.Fatal("无案件仍有处置导航")
		}
		for _, auth := range []string{crisisToken, superToken} {
			if len(call(t, auth, "/"+target, 200)["active_penalties"].([]any)) != 2 {
				t.Fatal("授权限制缺失")
			}
		}
		page := call(t, token, "/"+target+"/penalties?limit=1", 200)
		seen := []string{}
		for {
			for _, v := range items(page) {
				p := v.(map[string]any)
				id := p["id"].(string)
				seen = append(seen, id)
				if len(p) != 10 || id == hidden.ID || id == future.ID || strings.Contains(fmt.Sprint(p), "自由理由") {
					t.Fatal("处罚泄漏")
				}
				if id == anon.ID && (p["case_link"] != nil || p["appeal_id"] != nil) {
					t.Fatal("匿名来源泄漏")
				}
				if id == active.ID && (p["case_link"] == nil || p["appeal_id"] != appealID) {
					t.Fatal("普通案件链接丢失")
				}
			}
			if page["next_cursor"] == nil {
				break
			}
			c := page["next_cursor"].(string)
			call(t, token, "/"+twin+"/penalties?limit=1&cursor="+url.QueryEscape(c), 400)
			page = call(t, token, "/"+target+"/penalties?limit=1&cursor="+url.QueryEscape(c), 200)
		}
		if len(seen) != 3 {
			t.Fatal("先过滤后分页遗漏")
		}
		must(root.Model(&entity.Penalty{}).Where("id=?", hidden.ID).Update("revoked_at", now).Error)
		d = call(t, token, "/"+target, 200)
		if d["restriction"] != "muted" || d["muted_until"] == nil {
			t.Fatal("撤一项错误解全罚")
		}
		assertIDs("?nickname="+url.QueryEscape("小鲤")+"&verification=verified&restriction=muted", target)
		assertIDs("?nickname=" + url.QueryEscape("小鲤") + "&restriction=normal")
		must(root.Model(&entity.Penalty{}).Where("id=?", active.ID).Update("revoked_at", now).Error)
		if call(t, token, "/"+target, 200)["restriction"] != "normal" {
			t.Fatal("警告或到期处罚仍限制账号")
		}
	})
	t.Run("撤权注销", func(t *testing.T) {
		id, auth := admin("review_operator")
		call(t, auth, "", 200)
		must(root.Where("admin_id=?", id).Delete(&entity.AdminRoleBinding{}).Error)
		call(t, auth, "", 403)
		must(root.Model(&entity.AdminAccount{}).Where("id=?", id).Update("auth_epoch", 2).Error)
		call(t, auth, "", 401)
		barrier := account("屏障合成目标")
		call(t, token, "/"+barrier, 200)
		old := call(t, token, "?limit=1", 200)["next_cursor"].(string)
		tx := root.Begin()
		must(tx.Error)
		defer tx.Rollback()
		_, e := command.LockPlan(context.Background(), tx, command.Discovery{Accounts: []string{barrier}}, time.Second)
		must(e)
		must(tx.Model(&entity.Account{}).Where("id=?", barrier).Updates(map[string]any{"lifecycle": "deleting", "version": 8}).Error)
		result := make(chan int, 1)
		go func() {
			req, _ := http.NewRequest("GET", server.URL+"/api/v1/admin/users/"+barrier, nil)
			req.Header.Set("Authorization", "Bearer "+token)
			resp, e := client.Do(req)
			if e != nil {
				result <- 0
				return
			}
			defer resp.Body.Close()
			result <- resp.StatusCode
		}()
		// 确认读取确实等待账号锁，再提交注销，避免把串行请求误记为并发证据。
		waiting := false
		for i := 0; i < 50; i++ {
			var n int64
			must(root.Raw("SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%lock_command_account%'").Scan(&n).Error)
			if n > 0 {
				waiting = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !waiting {
			t.Fatal("未观察到读取等待账号锁")
		}
		must(tx.Commit().Error)
		status := <-result
		if status != 404 && status != 503 {
			t.Fatalf("并发注销回显%d", status)
		}
		call(t, token, "/"+barrier, 404)
		call(t, token, "/"+barrier+"/penalties", 404)
		assertIDs("?user_id=" + barrier)
		if slices.Contains(ids(call(t, token, "?limit=1&cursor="+url.QueryEscape(old), 200)), barrier) {
			t.Fatal("旧游标回显")
		}
		var audits int64
		must(root.Model(&entity.AccessAudit{}).Where("subject_ref=?", barrier).Count(&audits).Error)
		if audits != 1 {
			t.Fatal("浏览审计关联错误")
		}
		var invalidRefs int64
		must(root.Model(&entity.AccessAudit{}).Where("purpose='admin_users_list' AND subject_ref<>actor_id").Count(&invalidRefs).Error)
		if invalidRefs != 0 {
			t.Fatal("列表审计存入整页学生ID")
		}
	})
	t.Run("八千用户与日志", func(t *testing.T) {
		must(root.Exec(`INSERT INTO public.accounts(id,nickname,verification,created_at) SELECT gen_random_uuid(),'合成用户'||g,CASE WHEN g%2=0 THEN 'verified' ELSE 'unverified' END,clock_timestamp()-g*interval '1 second' FROM generate_series(1,8000) AS g`).Error)
		// 足量有效处罚参与状态聚合，避免以几乎无处罚的空关联测量代替实际查询成本。
		must(root.Exec(`INSERT INTO public.penalties(id,account_id,level,action_kind,violation_ref,content_version,starts_at,ends_at,reason_ref)
 SELECT gen_random_uuid(),id,CASE WHEN rn%2=0 THEN 'general' ELSE 'severe' END,
 CASE WHEN rn%2=0 THEN 'mute' ELSE 'ban' END,gen_random_uuid(),1,
 CURRENT_TIMESTAMP-interval '1 hour',CASE WHEN rn%2=0 THEN CURRENT_TIMESTAMP+interval '167 hours' END,gen_random_uuid()
 FROM (SELECT id,row_number() OVER (ORDER BY id) AS rn FROM public.accounts WHERE nickname LIKE '合成用户%' LIMIT 4000) a`).Error)
		must(root.Exec("ANALYZE public.accounts").Error)
		must(root.Exec("ANALYZE public.penalties").Error)
		for _, scenario := range []struct {
			name, path string
			full       bool
		}{{"无筛选", "?limit=20", true}, {"认证", "?verification=verified&limit=20", true}, {"禁言", "?restriction=muted&limit=20", true}, {"封禁", "?restriction=banned&limit=20", true}, {"高命中", "?nickname=" + url.QueryEscape("合成") + "&limit=20", true}, {"单字", "?nickname=" + url.QueryEscape("用") + "&limit=20", true}, {"组合", "?nickname=" + url.QueryEscape("合成") + "&verification=verified&restriction=normal&limit=20", true}, {"无命中", "?nickname=nonexistent", false}, {"精确ID", "?user_id=" + target, false}} {
			var samples []time.Duration
			var nextSamples []time.Duration
			for i := 0; i < 40; i++ {
				start := time.Now()
				d := call(t, token, scenario.path, 200)
				samples = append(samples, time.Since(start))
				if scenario.full {
					if len(items(d)) != 20 {
						t.Fatal("样本不足量")
					}
					start = time.Now()
					next := call(t, token, scenario.path+"&cursor="+url.QueryEscape(d["next_cursor"].(string)), 200)
					if len(items(next)) != 20 {
						t.Fatal("后续页样本不足量")
					}
					nextSamples = append(nextSamples, time.Since(start))
				}
			}
			slices.Sort(samples)
			t.Logf("8000+合成账号/4000有效处罚，单HTTP客户端 %s 首页n=%d P95=%s（非并发负载验收）", scenario.name, len(samples), samples[37])
			if len(nextSamples) > 0 {
				slices.Sort(nextSamples)
				t.Logf("%s 后续页n=%d P95=%s", scenario.name, len(nextSamples), nextSamples[37])
			}
		}
		for _, entry := range logs.All() {
			b, _ := json.Marshal(entry.ContextMap())
			for _, secret := range []string{token, "小鲤", "LiFish", "自由理由", "nickname="} {
				if strings.Contains(string(b), secret) {
					t.Fatal("日志泄漏")
				}
			}
		}
	})
	t.Run("直接用户操作", func(t *testing.T) {
		uid := account("直接处置合成用户")
		post := func(auth, action, key string, version int64, want int) map[string]any {
			t.Helper()
			raw, _ := json.Marshal(map[string]any{"reason": "直接操作合成理由", "expected_version": version})
			req, e := http.NewRequest("POST", server.URL+"/api/v1/admin/users/"+uid+"/restrictions/"+action, strings.NewReader(string(raw)))
			must(e)
			req.Header.Set("Authorization", "Bearer "+auth)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
			resp, e := client.Do(req)
			must(e)
			defer resp.Body.Close()
			var body map[string]any
			must(json.NewDecoder(resp.Body).Decode(&body))
			if resp.StatusCode != want {
				t.Fatalf("%s 状态%d期望%d：%v", action, resp.StatusCode, want, body)
			}
			if want != 200 {
				return body
			}
			return body["data"].(map[string]any)
		}
		call(t, token, "/"+uid+"/restrictions", 403)
		call(t, crisisToken, "/"+uid+"/restrictions", 403)
		post(token, "ban", uuid.NewString(), 7, 403)
		d := call(t, superToken, "/"+uid+"/restrictions", 200)
		if d["banned"] != false || d["muted"] != false {
			t.Fatal("初始状态错误")
		}
		key := uuid.NewString()
		d = post(superToken, "ban", key, 7, 200)
		if d["banned"] != true || d["account_version"] != float64(8) {
			t.Fatal("封禁或账号版本未生效")
		}
		post(superToken, "ban", key, 7, 200)
		var count int64
		must(root.Model(&entity.Penalty{}).Where("account_id=?", uid).Count(&count).Error)
		if count != 1 {
			t.Fatal("幂等重放重复处罚")
		}
		post(superToken, "mute", uuid.NewString(), 7, 409)
		d = post(superToken, "mute", uuid.NewString(), 8, 200)
		if d["banned"] != true || d["muted"] != true || d["muted_until"] == nil {
			t.Fatal("并存状态丢失")
		}
		d = post(superToken, "unban", uuid.NewString(), 9, 200)
		if d["banned"] != false || d["muted"] != true {
			t.Fatal("解封错误解除禁言")
		}
		d = post(superToken, "unmute", uuid.NewString(), 10, 200)
		if d["banned"] != false || d["muted"] != false {
			t.Fatal("取消禁言未生效")
		}
		post(superToken, "unmute", uuid.NewString(), 11, 409)
		var rows []entity.Penalty
		must(root.Where("account_id=?", uid).Find(&rows).Error)
		for _, p := range rows {
			if p.RevokedAt == nil || p.RevokedBy == nil || len(p.RevokeReasonCipher) == 0 || strings.Contains(string(p.RevokeReasonCipher), "合成理由") {
				t.Fatal("解除审计缺失或理由明文")
			}
		}
		// 同类型多条处罚须全部解除，另一类型及内容屏障不受影响。
		post(superToken, "ban", uuid.NewString(), 11, 200)
		extra := entity.Penalty{ID: uuid.NewString(), AccountID: uid, Level: "severe", ActionKind: "ban", ViolationRef: uuid.NewString(), ContentVersion: 1, StartsAt: now, ReasonRef: uuid.NewString()}
		must(root.Create(&extra).Error)
		post(superToken, "unban", uuid.NewString(), 12, 200)
		must(root.Model(&entity.Penalty{}).Where("account_id=? AND action_kind='ban' AND revoked_at IS NULL", uid).Count(&count).Error)
		if count != 0 {
			t.Fatal("遗漏有效封禁")
		}
		// 直接处罚仍允许本人发起申诉，只有超管可读取并决定直接处罚申诉。
		post(superToken, "mute", uuid.NewString(), 13, 200)
		var direct entity.Penalty
		must(root.Where("account_id=? AND action_kind='mute' AND revoked_at IS NULL", uid).Take(&direct).Error)
		st := session(uid, false)
		studentService, e := safety.New(open(dsn+" options='-c role=liyuanci_api'"), log, codec, safety.Config{Command: command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}, Key: []byte(strings.Repeat("u", 32)), MaxTextBytes: 65536})
		must(e)
		result, e := studentService.Execute(context.Background(), st, uuid.NewString(), uuid.NewString(), "appeal", "", safety.Input{PenaltyID: direct.ID, Body: "合成申诉"})
		must(e)
		ap := result.(safety.Appeal)
		if _, e = service.Detail(context.Background(), token, uuid.NewString(), "appeal_detail", ap.ID); e != command.ErrForbidden {
			t.Fatalf("普通角色读取直接申诉: %v", e)
		}
		_, e = service.Detail(context.Background(), superToken, uuid.NewString(), "appeal_detail", ap.ID)
		must(e)
		_, e = service.Execute(context.Background(), superToken, uuid.NewString(), uuid.NewString(), "appeal_decision", ap.ID, safety.Input{Decision: "revoke", Reason: "合成申诉通过", ExpectedVersion: ap.Version})
		must(e)
		// 相同账号版本的并发直接操作只能一个提交；新类型与旧处罚共享账号锁。
		statuses := make(chan int, 2)
		for i := 0; i < 2; i++ {
			go func() {
				req, e := http.NewRequest("POST", server.URL+"/api/v1/admin/users/"+uid+"/restrictions/ban", strings.NewReader(`{"reason":"合成并发处置","expected_version":15}`))
				if e != nil {
					statuses <- 0
					return
				}
				req.Header.Set("Authorization", "Bearer "+superToken)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", uuid.NewString())
				resp, e := client.Do(req)
				if e != nil {
					statuses <- 0
					return
				}
				defer resp.Body.Close()
				statuses <- resp.StatusCode
			}()
		}
		statusesPair := []int{<-statuses, <-statuses}
		slices.Sort(statusesPair)
		if !slices.Equal(statusesPair, []int{200, 409}) {
			t.Fatalf("并发状态错误: %v", statusesPair)
		}
		if e := migration.Run(context.Background(), root, log, "down"); e == nil {
			t.Fatal("包含直接处置记录的迁移不应可回退")
		}
		call(t, superToken, "/"+uid+"/restrictions", 200)
		// 撤权后新读写及旧幂等重放均拒绝；注销屏障不能被直接操作复活。
		revokeID, revokeToken := admin("super_admin")
		must(root.Where("admin_id=?", revokeID).Delete(&entity.AdminRoleBinding{}).Error)
		post(revokeToken, "ban", uuid.NewString(), 15, 403)
		must(root.Model(&entity.Account{}).Where("id=?", uid).Update("lifecycle", "deleting").Error)
		call(t, superToken, "/"+uid+"/restrictions", 404)
		post(superToken, "ban", key, 7, 404)
	})

}
