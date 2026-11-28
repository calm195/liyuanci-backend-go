package moderation_test

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
)

// checkEvidenceDeletionJobs 使用受限证据连接与真实HTTP，验证岗位和列表投影不扩大匿名回执读取范围。
func checkEvidenceDeletionJobs(t *testing.T, db *gorm.DB, evidence *privacy.Service, log *zap.Logger, studentID, studentToken string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	makeAdmin := func(role string) (string, string) {
		t.Helper()
		id, token := uuid.NewString(), uuid.NewString()
		ref := "synthetic-evidence-admin-" + id
		must(db.Create(&entity.AdminAccount{ID: id, CredentialRef: &ref}).Error)
		if role != "" {
			must(db.Create(&entity.AdminRoleBinding{AdminID: id, Role: role}).Error)
		}
		hash := sha256.Sum256([]byte(token))
		must(db.Create(&entity.Session{ID: uuid.NewString(), AdminID: &id, TokenHash: hash[:], AuthEpoch: 1, ExpiresAt: time.Now().Add(time.Hour)}).Error)
		return id, token
	}
	admin, superToken := makeAdmin("super_admin")
	_, reviewToken := makeAdmin("review_operator")
	_, crisisToken := makeAdmin("crisis_operator")
	_, emptyToken := makeAdmin("")
	router := httpapi.New(log, nil)
	must(httpapi.RegisterEvidenceDeletion(router, evidence))
	const path = "/api/v1/admin/evidence-deletion-jobs"
	call := func(token, suffix string, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path+suffix, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != status || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("证据删除列表状态：实际 %d 预期 %d", response.Code, status)
		}
		return response.Body.Bytes()
	}
	readPage := func(body []byte, page, total, items int) []privacy.EvidenceJobItem {
		t.Helper()
		var response struct {
			Code int                     `json:"code"`
			Data privacy.EvidenceJobPage `json:"data"`
		}
		must(json.Unmarshal(body, &response))
		got := response.Data
		if response.Code != 0 || got.Page != page || got.PageSize != 2 || got.Total == nil || *got.Total != int64(total) || got.Items == nil || len(got.Items) != items || got.NextCursor != nil {
			t.Fatal("证据任务列表分页不正确", string(body))
		}
		return got.Items
	}
	readPage(call(superToken, "?page=9&limit=2", 200), 1, 0, 0)

	now := time.Now().UTC().Truncate(time.Second)
	knownError, privateError := "database_cleanup_unavailable", "合成内部敏感错误"
	privateRef := uuid.NewString()
	legacy := entity.DeletionJob{
		JobID: "d3100000-0000-4000-8000-000000000001", Scope: "evidence", SourceModule: "legacy", Phase: "cleaning",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), NextAt: now.Add(time.Hour), TargetRef: &privateRef, ErrorCode: &knownError,
	}
	whisper := legacy
	whisper.JobID, whisper.SourceModule, whisper.Phase = "d3100000-0000-4000-8000-000000000002", "whispers", "completed"
	whisper.ErrorCode = &privateError
	older := legacy
	older.JobID, older.CreatedAt, older.ErrorCode = uuid.NewString(), now.Add(-time.Minute), &privateError
	expired := legacy
	expired.JobID, expired.ExpiresAt = uuid.NewString(), now.Add(-time.Minute)
	account := legacy
	account.JobID, account.Scope, account.SubjectRef = uuid.NewString(), "account", &studentID
	jobs := []entity.DeletionJob{legacy, whisper, older, expired, account}
	must(db.Create(&jobs).Error)
	t.Cleanup(func() {
		for _, job := range jobs {
			must(db.Where("job_id=?", job.JobID).Delete(&entity.DeletionJob{}).Error)
		}
	})

	for _, sample := range []struct {
		token string
		want  int
	}{
		{"", 401}, {studentToken, 401}, {emptyToken, 403}, {reviewToken, 403}, {crisisToken, 403},
	} {
		body := call(sample.token, "?page=2&limit=2", sample.want)
		if strings.Contains(string(body), `"total"`) || strings.Contains(string(body), legacy.JobID) {
			t.Fatal("未授权响应泄露任务列表")
		}
	}
	for _, suffix := range []string{"?page=0", "?page=1000001", "?limit=51", "?limit=2&limit=3", "?cursor=opaque", "?scope=account"} {
		call(superToken, suffix, 400)
	}
	first := call(superToken, "?page=1&limit=2", 200)
	last := call(superToken, "?page=9&limit=2", 200)
	for _, body := range [][]byte{first, last} {
		for _, private := range []string{privateRef, privateError, studentID, expired.JobID, account.JobID, "target_ref", "subject_ref", "remaining_counts"} {
			if strings.Contains(string(body), private) {
				t.Fatal("列表泄露非公开字段或范围")
			}
		}
	}
	rows := readPage(first, 1, 3, 2)
	if rows[0].ID != whisper.JobID || rows[0].SourceModule != "whispers" || rows[0].Status != "completed" || rows[0].RetryableError != "" || !rows[0].CreatedAt.Equal(now) {
		t.Fatal("树洞记录或同时间排序不正确", rows[0])
	}
	if rows[1].ID != legacy.JobID || rows[1].SourceModule != "legacy" || rows[1].Status != "processing" || rows[1].RetryableError != knownError {
		t.Fatal("普通记录或重试状态不正确", rows[1])
	}
	if row := readPage(last, 2, 3, 1)[0]; row.ID != older.JobID || row.RetryableError != "cleanup_unavailable" {
		t.Fatal("后续页缺少旧任务或安全重试提示", row)
	}
	call(superToken, "/"+legacy.JobID, 200)
	call(superToken, "/"+whisper.JobID, 404)
	must(db.Where("admin_id=? AND role='super_admin'", admin).Delete(&entity.AdminRoleBinding{}).Error)
	call(superToken, "?page=1&limit=2", 403)
}
