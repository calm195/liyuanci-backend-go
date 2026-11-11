package moderation_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/modules/privacy"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// verifyMatchmakingPrivacy 在隔离原生数据库中验证即时撤权、双方副本删除及完成回执恢复重删。
func verifyMatchmakingPrivacy(t *testing.T, root *gorm.DB, service *privacy.Service, codec *securedata.Codec, student func() (string, string), admin, adminToken string) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, token := student()
	peer, peerToken := student()
	now := time.Now()
	activityID, pairID, taskID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	conversationID := uuid.NewString()
	trait := entity.MatchmakingTrait{ID: uuid.NewString(), Name: "合成隐私特质", Status: "approved", Version: 1, Evidence: json.RawMessage(`{"matches":[],"routes":[]}`), CreatedAt: now}
	must(root.Create(&trait).Error)
	ownerTrait := entity.MatchmakingTraitSelection{AccountID: owner, TraitID: trait.ID, CreatedAt: now}
	peerTrait := entity.MatchmakingTraitSelection{AccountID: peer, TraitID: trait.ID, CreatedAt: now}
	must(root.Create(&ownerTrait).Error)
	must(root.Create(&peerTrait).Error)
	activity := entity.MatchmakingActivity{ID: activityID, Version: 1, Title: "合成活动", Description: "合成活动说明", Status: "published", PublishedAt: &now, TasksPublishedAt: &now, Campuses: json.RawMessage(`["wushan"]`), RegistrationOpensAt: now.Add(-4 * time.Hour), RegistrationClosesAt: now.Add(-3 * time.Hour), ResultsAt: now.Add(-2 * time.Hour), StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), Days: 1, CreatedAt: now}
	must(root.Create(&activity).Error)
	enrollments := []entity.MatchmakingEnrollment{}
	members := []entity.MatchmakingMember{}
	for _, id := range []string{owner, peer} {
		row := entity.MatchmakingEnrollment{ID: uuid.NewString(), ActivityID: activityID, AccountID: id, Version: 1, ProfileVersion: 1, Status: "approved", CreatedAt: now}
		cipher, err := codec.Encrypt(ctx, securedata.Binding{Purpose: "matchmaking.profile.v1", ObjectID: row.ID, Version: 1}, []byte(`{"name":"合成个人姓名"}`))
		must(err)
		row.ProfileCipher, err = json.Marshal(cipher)
		must(err)
		must(root.Create(&row).Error)
		enrollments = append(enrollments, row)
		members = append(members, entity.MatchmakingMember{PairID: pairID, EnrollmentID: row.ID, ActivityID: activityID, AccountID: id, ProfileVersion: 1, Active: true})
	}
	pair := entity.MatchmakingPair{ID: pairID, ActivityID: activityID, Version: 1, Status: "published", Score: 80, ScoreDetails: json.RawMessage(`{}`), Algorithm: "legacy-fixed-v1", PublishedAt: &now, CreatedAt: now}
	conversation := entity.Conversation{ID: conversationID, MatchmakingPairID: &pairID, CreatedAt: now}
	task := entity.MatchmakingTask{ID: taskID, PairID: pairID, DayIndex: 1, Kind: "online", Body: "合成任务", UnlocksAt: now.Add(-time.Hour)}
	opens := []entity.MatchmakingOpen{{TaskID: taskID, AccountID: owner, OpenedAt: now}, {TaskID: taskID, AccountID: peer, OpenedAt: now}}
	audit := entity.MatchmakingAudit{ID: uuid.NewString(), ActivityID: activityID, ActorID: admin, SubjectRef: pairID, Action: "pair", At: now}
	access := entity.AccessAudit{ID: uuid.NewString(), ActorID: admin, SubjectRef: pairID, Purpose: "matchmaking.pair", At: now, Outcome: "allowed"}
	notice := entity.Notification{ID: uuid.NewString(), RecipientID: peer, Type: "matchmaking_published", TargetRef: pairID, Version: 1, CreatedAt: now}
	receipts := []entity.CommandReceipt{
		{ActorKind: "admin", ActorID: admin, CommandKind: "matchmaking.pair", KeyHash: bytes.Repeat([]byte{81}, 32), RequestHash: bytes.Repeat([]byte{82}, 32), ResultRef: pairID, ExpiresAt: now.Add(time.Hour), LifecycleBound: true},
		{ActorKind: "student", ActorID: peer, CommandKind: "matchmaking.task_open", KeyHash: bytes.Repeat([]byte{83}, 32), RequestHash: bytes.Repeat([]byte{84}, 32), ResultRef: taskID, ExpiresAt: now.Add(time.Hour), LifecycleBound: true},
	}
	for _, v := range []any{&pair, &members, &conversation, &task, &opens, &audit, &access, &notice, &receipts} {
		must(root.Create(v).Error)
	}
	matchmakingService, err := matchmaking.New(root, zap.NewNop(), codec, matchmaking.Config{
		Command:   command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3},
		Grades:    func(context.Context) []string { return []string{"synthetic"} },
		CheckText: func(context.Context, *gorm.DB, string) error { return nil },
	})
	must(err)
	chats, err := chat.New(root, zap.NewNop(), codec, chat.Config{
		Command: command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3},
		Key:     bytes.Repeat([]byte{85}, 32), MaxBodyBytes: 65536,
	})
	must(err)
	chats.SetMatchmakingNotificationTarget(matchmaking.NotificationTarget)
	sent, err := chats.Send(ctx, peerToken, uuid.NewString(), "matchmaking-privacy-send", conversationID, chat.MessageInput{
		ClientMessageID:    uuid.NewString(),
		Body:               "合成待审聊天",
		ExpectedWriteEpoch: 1,
	})
	must(err)
	notifications, err := chats.Notifications(ctx, peerToken, "matchmaking-notifications", chat.Query{Limit: 50})
	must(err)
	found := false
	for _, item := range notifications.Items {
		if item.ID == notice.ID {
			found = true
			if item.TargetKind == nil || *item.TargetKind != "matchmaking" || item.TargetRef == nil || *item.TargetRef != activityID {
				t.Fatal("成员通知未投影为活动导航")
			}
		}
	}
	if !found {
		t.Fatal("成员未收到包分配站内通知")
	}
	outsider, outsiderToken := student()
	unrelated := entity.Notification{ID: uuid.NewString(), RecipientID: outsider, Type: "matchmaking_published", TargetRef: pairID, Version: 1, CreatedAt: now}
	must(root.Create(&unrelated).Error)
	otherNotifications, err := chats.Notifications(ctx, outsiderToken, "matchmaking-other-notifications", chat.Query{Limit: 50})
	must(err)
	for _, item := range otherNotifications.Items {
		if item.ID == unrelated.ID && (item.TargetKind != nil || item.TargetRef != nil) {
			t.Fatal("非成员通知暴露了其他配对的活动导航")
		}
	}
	var account entity.Account
	must(root.Where("id=?", owner).Take(&account).Error)
	accepted, err := service.Request(ctx, token, uuid.NewString(), "matchmaking-delete", privacy.DeletionInput{Confirmation: "delete_account", ExpectedVersion: 1})
	must(err)
	traitStats, err := matchmakingService.ReadTraits(ctx, adminToken, "privacy-traits", matchmaking.TraitQuery{Q: "合成隐私特质"})
	must(err)
	if len(traitStats.Items) != 1 || traitStats.Items[0].Heat != 1 {
		t.Fatal("注销受理未立即退出特质热度")
	}
	var activated entity.MatchmakingActivity
	must(root.Where("id=?", activityID).Take(&activated).Error)
	if activated.Version != activity.Version+1 {
		t.Fatal("注销解除配对未推进活动聚合版本")
	}
	_, err = service.Recover(ctx, "", 100, "account")
	must(err)
	var repeated entity.MatchmakingActivity
	must(root.Where("id=?", activityID).Take(&repeated).Error)
	if repeated.Version != activated.Version {
		t.Fatal("重复激活注销无状态变化却增加活动版本")
	}
	var active int64
	must(root.Model(&entity.MatchmakingMember{}).Where("pair_id=? AND active", pairID).Count(&active).Error)
	if active != 0 {
		t.Fatal("受理注销后仍保留有效配对成员")
	}
	if _, err = chats.Messages(ctx, peerToken, "matchmaking-privacy-revoked", conversationID, chat.Query{Limit: 20}); !errors.Is(err, command.ErrNotFound) {
		t.Fatal("注销后配对会话仍可读取", err)
	}
	var cancelled entity.Message
	must(root.Where("id=?", sent.MessageID).Take(&cancelled).Error)
	if cancelled.Status != "cancelled" {
		t.Fatal("注销未取消幸存方待审消息")
	}
	var peerEnrollment entity.MatchmakingEnrollment
	must(root.Where("id=?", enrollments[1].ID).Take(&peerEnrollment).Error)
	if peerEnrollment.Status != "approved" {
		t.Fatal("注销错误覆盖幸存报名")
	}
	drain := func() {
		t.Helper()
		for i := 0; i < 10; i++ {
			processed, err := service.ProcessNext(ctx, "account")
			must(err)
			if !processed {
				return
			}
		}
		t.Fatal("包分配删除未收敛")
	}
	assertClean := func() {
		t.Helper()
		for _, spec := range []struct {
			model any
			where string
			arg   any
		}{
			{&entity.MatchmakingTraitSelection{}, "account_id=?", owner},
			{&entity.MatchmakingPair{}, "id=?", pairID}, {&entity.MatchmakingMember{}, "pair_id=?", pairID},
			{&entity.Conversation{}, "id=?", conversationID}, {&entity.Message{}, "id=?", sent.MessageID},
			{&entity.ContentRegistry{}, "entity_id=?", sent.MessageID},
			{&entity.MatchmakingTask{}, "id=?", taskID}, {&entity.MatchmakingOpen{}, "task_id=?", taskID},
			{&entity.MatchmakingEnrollment{}, "id=?", enrollments[0].ID}, {&entity.MatchmakingAudit{}, "subject_ref=?", pairID},
			{&entity.AccessAudit{}, "subject_ref=?", pairID},
			{&entity.Notification{}, "target_ref=?", pairID}, {&entity.CommandReceipt{}, "result_ref IN ?", []string{pairID, taskID}},
		} {
			var n int64
			must(root.Model(spec.model).Where(spec.where, spec.arg).Count(&n).Error)
			if n != 0 {
				t.Fatalf("包分配副本残留 %T", spec.model)
			}
		}
		var n int64
		must(root.Model(&entity.MatchmakingTraitSelection{}).Where("account_id=? AND trait_id=?", peer, trait.ID).Count(&n).Error)
		if n != 1 {
			t.Fatal("幸存用户特质选用被删除")
		}
		must(root.Model(&entity.MatchmakingEnrollment{}).Where("id=? AND status='approved'", enrollments[1].ID).Count(&n).Error)
		if n != 1 {
			t.Fatal("幸存报名被删除")
		}
		currentNotifications, err := chats.Notifications(ctx, peerToken, "matchmaking-clean-notifications", chat.Query{Limit: 50})
		must(err)
		for _, item := range currentNotifications.Items {
			if item.ID == notice.ID {
				t.Fatal("物理清理后仍向幸存方返回已删配对通知")
			}
		}
		workspace, err := matchmakingService.ReadAdmin(ctx, adminToken, "privacy-workspace", activityID, 1, 20)
		must(err)
		if !workspace.TemplatesLocked {
			t.Fatal("最后已发布配对物理清理后题库被解冻")
		}
		_, err = matchmakingService.SaveTemplates(ctx, adminToken, uuid.NewString(), "privacy-templates", activityID, matchmaking.TemplatesInput{ExpectedVersion: workspace.Activity.Version, Items: []matchmaking.Template{{Kind: "online", Body: "不应覆盖已发布题库", FixedDay: 1}}})
		if !errors.Is(err, command.ErrConflict) {
			t.Fatalf("注销后允许改写已发布题库：%v", err)
		}
		remaining, err := privacy.AccountRemaining(ctx, root, owner)
		must(err)
		if remaining != 0 {
			t.Fatal("删除完成残留未归零")
		}
	}
	drain()
	assertClean()
	status, err := service.Status(ctx, "matchmaking-status", accepted.Receipt)
	must(err)
	if status.Status != "completed" {
		t.Fatal("包分配删除回执未完成")
	}
	// 模拟恢复已确认删除前的同事务数据，包含幸存方查看和管理员回执。
	for _, v := range []any{&account, &ownerTrait, &enrollments[0], &pair, &members, &conversation, &task, &opens, &audit, &access, &notice, &receipts} {
		must(root.Create(v).Error)
	}
	_, err = service.Recover(ctx, "", 100, "account")
	must(err)
	drain()
	assertClean()
	// 即使主体已不存在，独立审计残留也会使完成回执重新进入清理。
	orphan := entity.MatchmakingAudit{ID: uuid.NewString(), ActivityID: activityID, ActorID: owner, SubjectRef: activityID, Action: "withdraw", At: now}
	must(root.Create(&orphan).Error)
	remaining, err := privacy.AccountRemaining(ctx, root, owner)
	must(err)
	if remaining != 1 {
		t.Fatal("恢复残留门禁漏掉多态主体审计")
	}
	_, err = service.Recover(ctx, "", 100, "account")
	must(err)
	drain()
	assertClean()
}
