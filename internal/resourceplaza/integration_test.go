package resourceplaza_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/migration"
	"github.com/calm195/liyuanci-backend-go/internal/resourceplaza"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testKeys struct{}

func (testKeys) Wrap(_ context.Context, b []byte) (string, []byte, error) {
	return "synthetic", append([]byte(nil), b...), nil
}
func (testKeys) Unwrap(_ context.Context, _ string, b []byte) ([]byte, error) {
	return append([]byte(nil), b...), nil
}

// TestPostgresHTTP 使用独立临时数据库及真实受限PG身份、HTTP；KMS和账号是明确合成夹具。
func TestPostgresHTTP(t *testing.T) {
	dsn := os.Getenv("APP_RESOURCE_TEST_DSN")
	if dsn == "" {
		t.Skip("需显式隔离PostgreSQL管理员DSN")
	}
	ctx, log := context.Background(), zap.NewNop()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	open := func(v string) *gorm.DB {
		t.Helper()
		db, e := gorm.Open(postgres.New(postgres.Config{DSN: v, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		must(e)
		p, e := db.DB()
		must(e)
		t.Cleanup(func() { _ = p.Close() })
		return db
	}
	parent := open(dsn)
	var base string
	must(parent.Raw("SELECT current_database()").Scan(&base).Error)
	if base != "postgres" && base != "liyuanci" && base != "whisper_test" {
		t.Fatal("拒绝非测试父库")
	}
	name := "resource_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	must(parent.Exec("CREATE DATABASE " + name).Error)
	t.Cleanup(func() {
		if e := parent.Exec("DROP DATABASE " + name).Error; e != nil {
			t.Error(e)
		}
	})
	dbdsn := strings.Replace(dsn, "dbname="+base+" ", "dbname="+name+" ", 1)
	if dbdsn == dsn {
		t.Fatal("需要显式dbname参数及后续空格")
	}
	root := open(dbdsn)
	must(migration.Run(ctx, root, log, "up"))
	must(migration.ApplyPermissions(ctx, root, log))
	api, adminDB := open(dbdsn+" options='-c role=liyuanci_api'"), open(dbdsn+" options='-c role=liyuanci_admin_api'")
	var missingComments int64
	must(root.Raw(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid WHERE n.nspname='public' AND c.relname IN ('resource_categories','resource_materials','resource_feedback','resource_audits') AND a.attnum>0 AND NOT a.attisdropped AND nullif(btrim(col_description(c.oid,a.attnum)),'') IS NULL`).Scan(&missingComments).Error)
	if missingComments != 0 {
		t.Fatal("新表字段缺中文注释")
	}
	for _, v := range []struct {
		db  *gorm.DB
		sql string
	}{{api, "UPDATE public.resource_materials SET title='禁止' WHERE false"}, {adminDB, "DELETE FROM public.resource_materials WHERE false"}, {open(dbdsn + " options='-c role=liyuanci_crisis_api'"), "SELECT * FROM public.resource_materials LIMIT 0"}} {
		if e := v.db.Exec(v.sql).Error; e == nil {
			t.Fatal("越权数据库操作放行")
		}
	}
	codec, e := securedata.New(testKeys{}, log)
	must(e)
	cfg := resourceplaza.Config{Command: command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3}, Key: bytes.Repeat([]byte{71}, 32)}
	student, e := resourceplaza.New(api, log, codec, cfg)
	must(e)
	admin, e := resourceplaza.New(adminDB, log, codec, cfg)
	must(e)
	r := httpapi.New(log, func(context.Context) error { return nil })
	must(httpapi.RegisterResources(r, student, admin))
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	session := func(id string, isAdmin bool) string {
		tok := uuid.NewString()
		h := sha256.Sum256([]byte(tok))
		s := entity.Session{ID: uuid.NewString(), TokenHash: h[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}
		if isAdmin {
			s.AdminID = &id
		} else {
			s.AccountID = &id
		}
		must(root.Create(&s).Error)
		return tok
	}
	makeAdmin := func(role string) (string, string) {
		id, ref := uuid.NewString(), uuid.NewString()
		must(root.Create(&entity.AdminAccount{ID: id, CredentialRef: &ref}).Error)
		must(root.Create(&entity.AdminRoleBinding{AdminID: id, Role: role}).Error)
		return id, session(id, true)
	}
	makeStudent := func(verification string) (string, string) {
		id := uuid.NewString()
		must(root.Create(&entity.Account{ID: id, Nickname: "隔离测试学生", Verification: verification}).Error)
		return id, session(id, false)
	}
	aid, at := makeAdmin("review_operator")
	_, st := makeAdmin("super_admin")
	_, ct := makeAdmin("crisis_operator")
	uid, ut := makeStudent("verified")
	_, nt := makeStudent("unverified")
	captures := []map[string]any{}
	defer func() {
		if path := os.Getenv("APP_RESOURCE_HTTP_CAPTURE"); path != "" && !t.Failed() {
			b, e := json.MarshalIndent(captures, "", "  ")
			must(e)
			must(os.WriteFile(path, b, 0600))
		}
	}()
	type response struct {
		Code  int            `json:"code"`
		Error string         `json:"error_code"`
		Data  map[string]any `json:"data"`
	}
	call := func(method, path, token, key string, body any, want int) response {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, e = json.Marshal(body)
			must(e)
		}
		req, e := http.NewRequest(method, server.URL+"/api/v1"+path, bytes.NewReader(raw))
		must(e)
		req.Header.Set("Authorization", "Bearer "+token)
		if method != "GET" {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", key)
		}
		res, e := server.Client().Do(req)
		must(e)
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		must(e)
		var out response
		must(json.Unmarshal(b, &out))
		captures = append(captures, map[string]any{"method": method, "path": path, "status": res.StatusCode, "body": json.RawMessage(b)})
		if res.StatusCode != want {
			t.Fatalf("%s %s got %d: %s", method, path, res.StatusCode, b)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("缺no-store")
		}
		if want >= 400 && out.Data != nil {
			t.Fatal("错误泄露data")
		}
		return out
	}
	_ = call("GET", "/resources", nt, "", nil, 403)
	_ = call("GET", "/admin/resources", ut, "", nil, 403)
	_ = call("GET", "/resources", at, "", nil, 403)
	_ = call("GET", "/admin/resources/"+uuid.NewString(), ct, "", nil, 403)
	cat := call("POST", "/admin/resource-categories", at, "cat1", map[string]any{"name": "  数学  ", "sort_order": 1}, 201).Data
	cid := cat["id"].(string)
	call("POST", "/admin/resource-categories", at, "cat2", map[string]any{"name": "数学"}, 409)
	call("GET", "/resource-categories", ut, "", nil, 200)
	call("GET", "/admin/resource-categories", at, "", nil, 200)
	call("GET", "/admin/resource-categories/"+cid, at, "", nil, 200)
	cat = call("PUT", "/admin/resource-categories/"+cid, at, "cat-edit", map[string]any{"name": "数学", "sort_order": 2, "enabled": true, "expected_version": 1}, 200).Data
	input := map[string]any{"title": " ABC资料 ", "category_id": cid, "description": "学期\r\n完整资料", "download_url": "https://example.test/a?x=%2F", "extraction_code": "aB12"}
	res := call("POST", "/admin/resources", at, "r1", input, 201).Data
	id := res["id"].(string)
	if res["title"] != "ABC资料" {
		t.Fatal("归一化失效")
	}
	call("GET", "/resources/"+id, ut, "", nil, 404)
	call("GET", "/admin/resources", at, "", nil, 200)
	call("GET", "/admin/resources/"+id, at, "", nil, 200)
	res = call("POST", "/admin/resources/"+id+"/publish", at, "pub1", map[string]any{"expected_version": 1}, 200).Data
	replay := call("POST", "/admin/resources", at, "r1", input, 201).Data
	if replay["id"] != id || replay["status"] != "published" {
		t.Fatal("创建重放应返回当前状态")
	}
	call("GET", "/resources?q=abc", ut, "", nil, 200)
	call("GET", "/resources/"+id, ut, "", nil, 200)
	download := call("GET", "/resources/"+id+"/download-info?expected_version=2", ut, "", nil, 200).Data
	if download["copy_text"] != "https://example.test/a?x=%2F\n提取码：aB12" {
		t.Fatal("复制协议错误")
	}
	call("GET", "/resources/"+id+"/download-info?expected_version=1", ut, "", nil, 409)
	// 禁言仍可读与复制，封禁先于未认证提示；已保存反馈不会被处罚追溯删除。
	penalty := func(kind string) string {
		pid := uuid.NewString()
		starts := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
		level := "general"
		var until *time.Time
		if kind == "mute" {
			x := starts.Add(168 * time.Hour)
			until = &x
		} else {
			level = "severe"
		}
		must(root.Create(&entity.Penalty{ID: pid, AccountID: uid, Level: level, ActionKind: kind, ViolationRef: uuid.NewString(), ContentVersion: 1, StartsAt: starts, EndsAt: until, ReasonRef: uuid.NewString(), SourceKind: "direct"}).Error)
		return pid
	}
	mute := penalty("mute")
	call("GET", "/resources/"+id, ut, "", nil, 200)
	call("GET", "/resources/"+id+"/download-info?expected_version=2", ut, "", nil, 200)
	call("PUT", "/resources/"+id+"/feedback", ut, "muted", map[string]any{"kind": "valid", "expected_content_version": 1, "expected_feedback_version": 0}, 403)
	must(root.Delete(&entity.Penalty{}, "id=?", mute).Error)
	ban := penalty("ban")
	must(root.Model(&entity.Account{}).Where("id=?", uid).Update("verification", "unverified").Error)
	if got := call("GET", "/resources", ut, "", nil, 403); got.Error != "FORBIDDEN" {
		t.Fatal("封禁优先级错误")
	}
	must(root.Delete(&entity.Penalty{}, "id=?", ban).Error)
	must(root.Model(&entity.Account{}).Where("id=?", uid).Update("verification", "verified").Error)
	must(root.Delete(&entity.AdminRoleBinding{}, "admin_id=?", aid).Error)
	call("GET", "/admin/resources", at, "", nil, 403)
	must(root.Create(&entity.AdminRoleBinding{AdminID: aid, Role: "review_operator"}).Error)
	must(root.Model(&entity.AdminAccount{}).Where("id=?", aid).Update("auth_epoch", 2).Error)
	call("GET", "/admin/resources", at, "", nil, 401)
	must(root.Model(&entity.AdminAccount{}).Where("id=?", aid).Update("auth_epoch", 1).Error)
	fb := map[string]any{"kind": "valid", "expected_content_version": 1, "expected_feedback_version": 0}
	call("PUT", "/resources/"+id+"/feedback", ut, "f1", fb, 200)
	fb2 := map[string]any{"kind": "outdated", "expected_content_version": 1, "expected_feedback_version": 1}
	call("PUT", "/resources/"+id+"/feedback", ut, "f2", fb2, 200)
	f := call("PUT", "/resources/"+id+"/feedback", ut, "f1", fb, 200).Data
	if f["kind"] != "outdated" || f["version"] != float64(2) {
		t.Fatal("反馈重放恢复旧选项")
	}
	call("PUT", "/resources/"+id+"/feedback", ut, "f3", fb, 409)
	res = call("GET", "/admin/resources/"+id, at, "", nil, 200).Data
	if res["version"] != float64(2) || res["feedback_summary"].(map[string]any)["outdated"] != float64(1) {
		t.Fatal("反馈污染资源版本或汇总")
	}
	call("PUT", "/admin/resource-categories/"+cid, at, "disable", map[string]any{"name": "数学", "sort_order": 2, "enabled": false, "expected_version": cat["version"]}, 409)
	call("POST", "/admin/resources/"+id+"/offline", at, "off1", map[string]any{"expected_version": 2, "reason": "更新资料"}, 200)
	update := map[string]any{"title": "ABC资料", "category_id": cid, "description": "学期\n完整资料", "download_url": "https://example.test/a?x=%2F", "extraction_code": "aB12", "expected_version": 3}
	res = call("PUT", "/admin/resources/"+id+"/content", at, "noop", update, 200).Data
	if res["version"] != float64(3) || res["feedback_summary"].(map[string]any)["total"] != float64(1) {
		t.Fatal("无变化保存清票")
	}
	update["title"] = "新版资料"
	res = call("PUT", "/admin/resources/"+id+"/content", at, "update", update, 200).Data
	if res["content_version"] != float64(2) || res["feedback_summary"].(map[string]any)["total"] != float64(0) {
		t.Fatal("换版未清票")
	}
	var n int64
	must(root.Model(&entity.CommandReceipt{}).Where("actor_id=?", uid).Count(&n).Error)
	if n != 0 {
		t.Fatal("旧反馈回执未删除")
	}
	call("POST", "/admin/resources/"+id+"/publish", at, "pub2", map[string]any{"expected_version": 4}, 200)
	call("GET", "/resources/"+id+"/download-info?expected_version=2", ut, "", nil, 409)
	must(admin.SetBarrier(ctx, st, "barrier", "barrier1", id, false, true))
	call("GET", "/resources/"+id, ut, "", nil, 404)
	res = call("GET", "/admin/resources/"+id, at, "", nil, 200).Data
	if res["status"] != "offline" || res["visibility_blocked"] != true {
		t.Fatal("屏障未下架")
	}
	must(admin.SetBarrier(ctx, st, "barrier", "barrier2", id, true, true))
	call("GET", "/admin/resources/"+id, at, "", nil, 404)
	call("POST", "/admin/resources", at, "r1", input, 404)
	// 分页和主体/筛选绑定，静态结果不重不漏；不同管理员的分类引用竞争。
	for i := 0; i < 3; i++ {
		v := call("POST", "/admin/resources", at, uuid.NewString(), map[string]any{"title": "分页资料", "category_id": cid, "download_url": "https://example.test/page"}, 201).Data
		call("POST", "/admin/resources/"+v["id"].(string)+"/publish", at, uuid.NewString(), map[string]any{"expected_version": 1}, 200)
	}
	page := call("GET", "/resources?q=分页&limit=1", ut, "", nil, 200).Data
	seen := map[string]bool{}
	firstCursor := page["next_cursor"].(string)
	for {
		for _, item := range page["items"].([]any) {
			id := item.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("重复分页")
			}
			seen[id] = true
		}
		c := page["next_cursor"]
		if c == nil {
			break
		}
		page = call("GET", "/resources?q=分页&limit=1&cursor="+url.QueryEscape(c.(string)), ut, "", nil, 200).Data
	}
	if len(seen) != 3 {
		t.Fatal("分页遗漏")
	}
	_, otherToken := makeStudent("verified")
	for _, path := range []string{"/resources?q=分页&limit=2&cursor=", "/resources?q=其他&limit=1&cursor="} {
		call("GET", path+url.QueryEscape(firstCursor), ut, "", nil, 400)
	}
	call("GET", "/resources?q=分页&limit=1&cursor="+url.QueryEscape(firstCursor), otherToken, "", nil, 400)
	_, at2 := makeAdmin("review_operator")
	for i := 0; i < 5; i++ {
		v, e := admin.Write(ctx, at, "race", uuid.NewString(), "category_create", "", resourceplaza.Input{Name: uuid.NewString()[:20], Enabled: true})
		must(e)
		cat := v.(map[string]any)
		raceID := cat["id"].(string)
		start := make(chan struct{})
		failures := make(chan error, 2)
		go func() {
			<-start
			_, e := admin.Write(ctx, at, "race", uuid.NewString(), "create", "", resourceplaza.Input{Title: "并发资料", CategoryID: raceID, DownloadURL: "https://example.test/race"})
			failures <- e
		}()
		go func() {
			<-start
			_, e := admin.Write(ctx, at2, "race", uuid.NewString(), "category_update", raceID, resourceplaza.Input{Name: cat["name"].(string), Enabled: false, ExpectedVersion: 1})
			failures <- e
		}()
		close(start)
		a, b := <-failures, <-failures
		if (a == nil) == (b == nil) {
			t.Fatalf("竞争应只成功一方: %v %v", a, b)
		}
		var invalid int64
		must(root.Raw("SELECT count(*) FROM public.resource_materials r JOIN public.resource_categories c ON c.id=r.category_id WHERE NOT c.enabled").Scan(&invalid).Error)
		if invalid != 0 {
			t.Fatal("产生停用分类引用")
		}
	}
	// 原始协议负例不能到达服务层或改变数据库。
	for _, raw := range []string{`{"name":"a","name":"b"}`, `{"name":"a","sort_order":1.0}`, `{"name":"\ud800"}`} {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/admin/resource-categories", strings.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+at)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", uuid.NewString())
		resp, e := server.Client().Do(req)
		must(e)
		_ = resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatal("协议负例放行", resp.StatusCode)
		}
	}
	// 相同主体并发同键创建只生成一个分类，反复返回同一当前对象。
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := admin.Write(ctx, at, "parallel", "parallel-key", "category_create", "", resourceplaza.Input{Name: "并发分类", Enabled: true})
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(e)
	}
	must(root.Model(&entity.ResourceCategory{}).Where("name=?", "并发分类").Count(&n).Error)
	if n != 1 {
		t.Fatal("并发幂等重复创建")
	}
	var categoryQueries atomic.Int64
	must(api.Callback().Query().After("gorm:query").Register("resource_test_category_count", func(tx *gorm.DB) {
		if strings.HasSuffix(tx.Statement.Table, "resource_categories") {
			categoryQueries.Add(1)
		}
	}))
	for _, limit := range []string{"1", "50"} {
		before := categoryQueries.Load()
		call("GET", "/resources?limit="+limit, ut, "", nil, 200)
		if categoryQueries.Load()-before != 1 {
			t.Fatal("列表分类查询数随行数增长")
		}
	}
	// 单机隔离连接的查询耗时仅作实现回归，不代表真实供应商或生产容量。
	for _, path := range []string{"/resources?limit=20", "/resources?q=分页&limit=20", "/resource-categories"} {
		samples := []time.Duration{}
		for i := 0; i < 100; i++ {
			start := time.Now()
			call("GET", path, ut, "", nil, 200)
			samples = append(samples, time.Since(start))
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		t.Logf("隔离串行查询 %s n=100 P95=%s", path, samples[94])
	}
	t.Logf("真实PG/HTTP完成15操作、认证/权限、版本/幂等、反馈换版清理、分类引用、屏障与并发；独立库 %s", name)
}
