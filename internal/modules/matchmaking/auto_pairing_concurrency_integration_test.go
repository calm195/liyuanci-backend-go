package matchmaking_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/durable"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// TestPostgresAutoPairingConcurrentApply 验证多个不同命令同时确认同一预览只会建立一次占用。
func TestPostgresAutoPairingConcurrentApply(t *testing.T) {
	f := setup(t)
	codec, err := securedata.New(syntheticKeys{}, zap.NewNop())
	must(t, err)
	worker, err := matchmaking.NewAutoWorker(f.db, zap.NewNop(), codec, durable.ConsumerConfig{Timeout: 10 * time.Second, LockTimeout: time.Second, RetryDelay: time.Millisecond, MaxAttempts: 3})
	must(t, err)
	activity := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "concurrent-activity", activityInput(), 200)).ID
	f.command(activity, "publish", 200)
	for i, gender := range []string{"male", "female", "male", "female"} {
		a := f.account("verified")
		enrollment := f.enroll(activity, a, profile(fmt.Sprintf("合成并发%d", i), gender), 0)
		f.review(activity, enrollment, 1)
	}
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", activity).Update("registration_closes_at", time.Now().Add(-time.Minute)).Error)
	job, err := f.service.CreateAutoJob(context.Background(), f.admin.token, "concurrent-generate", "synthetic", activity, matchmaking.VersionInput{ExpectedVersion: f.workspace(activity).Activity.Version})
	must(t, err)
	_, err = worker.ProcessNext(context.Background())
	must(t, err)
	ready, err := f.service.ReadAutoJob(context.Background(), f.admin.token, "synthetic", activity, job.ID)
	must(t, err)
	if ready.Status != "ready" || ready.PairedCount != 2 {
		t.Fatal("未形成两对预览")
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	outcomes := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, e := f.service.ApplyAutoJob(context.Background(), f.admin.token, fmt.Sprintf("parallel-apply-%d", index), "synthetic", activity, job.ID, matchmaking.AutoApplyInput{ExpectedVersion: ready.Version, ActivityVersion: ready.ActivityVersion})
			outcomes <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(outcomes)
	successes := 0
	for e := range outcomes {
		if e == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("并发确认成功数应为1，实际%d", successes)
	}
	var pairs, members int64
	must(t, f.db.Model(&entity.MatchmakingPair{}).Where("activity_id=?", activity).Count(&pairs).Error)
	must(t, f.db.Model(&entity.MatchmakingMember{}).Where("activity_id=? AND active", activity).Count(&members).Error)
	if pairs != 2 || members != 4 {
		t.Fatalf("并发确认重复或部分落库：%d对/%d人", pairs, members)
	}
}
