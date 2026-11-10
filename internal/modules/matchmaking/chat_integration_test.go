package matchmaking_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/modules/chat"
	"github.com/calm195/liyuanci-backend-go/internal/modules/matchmaking"
	"github.com/calm195/liyuanci-backend-go/internal/platform/securedata"
)

// TestMatchmakingConversation 验证发布事务、双方送达、权限变化和现有聊天列表投影。
func TestMatchmakingConversation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	log := zap.NewNop()
	codec, err := securedata.New(syntheticKeys{}, log)
	must(t, err)
	chats, err := chat.New(f.db, log, codec, chat.Config{
		Command:      command.Config{Timeout: 10 * time.Second, LockTimeout: time.Second, ReceiptTTL: time.Hour, MaxAttempts: 3},
		Key:          []byte(strings.Repeat("c", 32)),
		MaxBodyBytes: 4096,
	})
	must(t, err)

	a, b, outsider := f.account("verified"), f.account("verified"), f.account("verified")
	id := decoded[matchmaking.Receipt](t, f.call("POST", "/admin/matchmaking/activities", f.admin, "create-chat-activity", activityInput(), 200)).ID
	f.command(id, "publish", 200)
	aID := f.enroll(id, a, profile("聊天参与者甲", "male"), 0)
	bID := f.enroll(id, b, profile("聊天参与者乙", "female"), 0)
	f.review(id, aID, 1)
	f.review(id, bID, 1)
	f.template(id, []matchmaking.Template{
		{Kind: "online", Body: "首日合成问候", FixedDay: 1},
		{Kind: "online", Body: "次日合成问候", FixedDay: 2},
		{Kind: "offline", Body: "三日合成散步", FixedDay: 3},
	}, 200)
	now := time.Now().UTC()
	must(t, f.db.Model(&entity.MatchmakingActivity{}).Where("id=?", id).Updates(map[string]any{
		"registration_closes_at": now.Add(-time.Hour),
		"results_at":             now.Add(-time.Minute),
	}).Error)
	f.call("POST", adminPath(id)+"/pairs", f.admin, "pair-chat-accounts", matchmaking.PairInput{
		ExpectedVersion: f.workspace(id).Activity.Version,
		First:           aID,
		Second:          bID,
	}, 200)
	query := chat.Query{Limit: 20}
	page, err := chats.Conversations(ctx, a.token, "list-draft", query)
	must(t, err)
	if len(page.Items) != 0 {
		t.Fatal("未发布配对泄露会话")
	}
	publish := matchmaking.VersionInput{ExpectedVersion: f.workspace(id).Activity.Version}
	for range 2 {
		f.call("POST", adminPath(id)+"/results/publish", f.admin, "publish-chat-activity", publish, 200)
	}
	pa, pb := f.detail(id, a).Pair, f.detail(id, b).Pair
	if pa == nil || pb == nil || pa.ConversationID == "" || pa.ConversationID != pb.ConversationID || pa.Accessible {
		t.Fatal("发布结果后双方应取得同一会话，活动尚未开始时任务仍隐藏")
	}
	conversation := pa.ConversationID
	for _, who := range []actor{a, b} {
		page, err := chats.Conversations(ctx, who.token, "list-published", query)
		must(t, err)
		if len(page.Items) != 1 || page.Items[0].ID != conversation || !page.Items[0].CanWrite {
			t.Fatal("会话列表没有正确投影已发布配对")
		}
	}
	if _, err = chats.Messages(ctx, outsider.token, "list-outsider", conversation, query); !errors.Is(err, command.ErrNotFound) {
		t.Fatal("第三人读取配对会话应返回未找到", err)
	}

	for _, peers := range [][2]actor{{a, b}, {b, a}} {
		sender, recipient := peers[0], peers[1]
		accepted, err := chats.Send(ctx, sender.token, uuid.NewString(), "send", conversation, chat.MessageInput{
			ClientMessageID:    uuid.NewString(),
			Body:               "合成配对聊天消息",
			ExpectedWriteEpoch: 1,
		})
		must(t, err)
		status, err := chats.MessageStatus(ctx, sender.token, "pending", accepted.MessageID)
		must(t, err)
		if status.Status != "pending" {
			t.Fatal("未扫描消息的授权绑定不正确", status.Status)
		}
		// 模拟终审事务调用同一投递函数，验证序号、正文、通知和配对限制。
		must(t, f.db.Transaction(func(db *gorm.DB) error {
			var message entity.Message
			if err := db.Where("id=?", accepted.MessageID).Take(&message).Error; err != nil {
				return err
			}
			if err := chat.Deliver(ctx, db, message, now); err != nil {
				return err
			}
			return db.Model(&entity.ContentRegistry{}).Where("kind='message' AND entity_id=?", message.ID).Update("status", "approved").Error
		}))
		messages, err := chats.Messages(ctx, recipient.token, "receive", conversation, query)
		must(t, err)
		if len(messages.Items) == 0 {
			t.Fatal("收件人未收到已终审消息")
		}
		message := messages.Items[len(messages.Items)-1]
		if message.MessageID != accepted.MessageID || message.ViewerIsSender || message.Body != "合成配对聊天消息" {
			t.Fatal("送达正文、顺序或相对作者错误")
		}
	}

	must(t, f.db.Create(&entity.Block{OwnerID: a.id, TargetID: b.id}).Error)
	if f.detail(id, a).Pair.ConversationID != "" {
		t.Fatal("拉黑后继续展示会话入口")
	}
	for _, who := range []actor{a, b} {
		page, err := chats.Conversations(ctx, who.token, "list-blocked", query)
		must(t, err)
		if len(page.Items) != 0 {
			t.Fatal("拉黑后仍显示会话")
		}
		if _, err := chats.Messages(ctx, who.token, "read-blocked", conversation, query); !errors.Is(err, command.ErrNotFound) {
			t.Fatal("拉黑后读取配对会话应返回未找到", err)
		}
		notifications, err := chats.Notifications(ctx, who.token, "notices-blocked", query)
		must(t, err)
		for _, notice := range notifications.Items {
			if notice.Kind == "message_delivered" && notice.TargetRef != nil {
				t.Fatal("拉黑后聊天通知仍可跳转会话")
			}
		}
	}
	must(t, f.db.Where("owner_id=? AND target_id=?", a.id, b.id).Delete(&entity.Block{}).Error)
	pair := f.workspace(id).Pairs[0]
	f.call("POST", adminPath(id)+"/pairs/"+pair.ID+"/end", f.admin, "end-chat-pair", matchmaking.VersionInput{
		ExpectedVersion: pair.Version,
		Reason:          "合成解除关系",
	}, 200)
	if f.detail(id, b).Pair.ConversationID != "" {
		t.Fatal("解除配对后仍可进入会话")
	}
	if _, err := chats.Messages(ctx, b.token, "read-ended", conversation, query); !errors.Is(err, command.ErrNotFound) {
		t.Fatal("解除配对后读取会话应返回未找到", err)
	}
}
