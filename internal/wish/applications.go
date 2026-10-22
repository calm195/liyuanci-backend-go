package wish

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/durable"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// ApplicationInput 保存留言及用户实际看到的心愿内容版本，不接受申请人或状态。
type ApplicationInput struct {
	Message            string `json:"message"`
	WishContentVersion int64  `json:"wish_content_version"`
}

// ApplicationResult 按当前授权返回本人状态或许愿人可处理的最小已审投影。
type ApplicationResult struct {
	ID           string        `json:"id"`
	Version      int64         `json:"version"`
	WishID       string        `json:"wish_id"`
	Status       string        `json:"status"`
	ReviewResult *ReviewResult `json:"review_result,omitempty"`
	Applicant    *PublicAuthor `json:"applicant,omitempty"`
	Message      *string       `json:"message,omitempty"`
}

// ApplicationPage 游标绑定主体、用途、心愿与状态筛选。
type ApplicationPage struct {
	Items      []ApplicationResult `json:"items"`
	NextCursor *string             `json:"next_cursor"`
}

// ApplicationQuery 仅本人查询允许状态筛选；许愿人列表固定只返回当前有效申请。
type ApplicationQuery struct {
	Status string
	Cursor string
	Limit  int
}

// AcceptInput 分别校验申请与心愿聚合版本，不能用内容版本代替。
type AcceptInput struct {
	ExpectedVersion     int64 `json:"expected_version"`
	WishExpectedVersion int64 `json:"wish_expected_version"`
}

// MatchResult 返回新关系及独立会话引用，Version是匹配自身版本。
type MatchResult struct {
	MatchID        string `json:"match_id"`
	ConversationID string `json:"conversation_id"`
	WishID         string `json:"wish_id"`
	Version        int64  `json:"version"`
}

func validApplicationMessage(message string) bool {
	return utf8.ValidString(message) && utf8.RuneCountInString(message) <= 100
}

func (s *Service) interactionDone(op, rid string, start time.Time, outErr *error) {
	if recover() != nil {
		*outErr = command.ErrInternal
	}
	s.policy.finish("应愿交互操作完成", op, rid, start, *outErr)
}

func (s *Service) applicationDigest(input ApplicationInput) string {
	b, _ := json.Marshal(input)
	defer clear(b)
	mac := hmac.New(sha256.New, s.receiptKey)
	mac.Write([]byte("application.create\x00"))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

// Apply 创建待扫描申请、加密留言和审核工作，不向许愿人返回待审正文。
// 回执仅存技术引用及带密钥摘要，重放按当前权限读取原申请，不重复通知或创建任务。
func (s *Service) Apply(ctx context.Context, token, key, rid, wishID string, in ApplicationInput) (out ApplicationResult, err error) {
	defer func() {
		if err != nil {
			out = ApplicationResult{}
		}
	}()
	defer s.interactionDone("application.create", rid, time.Now(), &err)
	if !validWishID(wishID) || !validCommandKey(key) || !validApplicationMessage(in.Message) || in.WishContentVersion < 1 {
		return out, command.ErrInvalid
	}
	id, registryID, taskID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	d := command.Definition{Kind: "application.create", Method: "POST", ResourcePath: "/api/v1/wishes/" + wishID + "/applications", ActorKind: "student", Access: command.Verified,
		Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
			return DiscoverInteraction(ctx, db, wishID)
		}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		a, e := readAggregate(ctx, tx.DB(), wishID, false)
		if e != nil {
			return e
		}
		if a.row.OwnerID == tx.Actor().ID {
			return command.ErrForbidden
		}
		// 当前对象访问先于回执查询；终态不阻止读取自己的旧回执，但不能绕过删除/拉黑/权限撤销。
		if !a.owner.Active || a.snapshot.PrivacyHeld || a.snapshot.Restricted {
			return command.ErrNotFound
		}
		ok, e := participantAllowed(ctx, tx.DB(), a.row.OwnerID, tx.Now())
		if e != nil {
			return e
		}
		if !ok {
			return command.ErrForbidden
		}
		denied, e := blocked(ctx, tx.DB(), tx.Actor().ID, a.row.OwnerID)
		if e != nil {
			return e
		}
		if denied {
			return command.ErrBlockedInteraction
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		a, e := readAggregate(ctx, tx.DB(), wishID, false)
		if e != nil {
			return "", e
		}
		if in.WishContentVersion != a.row.CurrentContentVersion {
			return "", command.ErrVersionConflict
		}
		if e = applicationTarget(ctx, tx.DB(), a, tx.Actor().ID, tx.Now()); e != nil {
			return "", e
		}
		var n int64
		if e = tx.DB().WithContext(ctx).Model(&entity.Application{}).Where("wish_id=? AND applicant_id=? AND status IN ?", wishID, tx.Actor().ID, []string{"pending_scan", "pending_review", "active"}).Count(&n).Error; e != nil {
			return "", e
		}
		if n > 0 {
			return "", command.ErrDuplicateApplication
		}
		cipher, e := applicationCipher(ctx, s.codec, id, "application_content", 1, in.Message)
		if e != nil {
			return "", e
		}
		row := entity.Application{ID: id, WishID: wishID, WishContentVersion: in.WishContentVersion, ApplicantID: tx.Actor().ID, MessageCipher: cipher, ContentVersion: 1, OwnerEpoch: &a.account.AuthEpoch}
		if e = tx.DB().WithContext(ctx).Create(&row).Error; e != nil {
			return "", e
		}
		if e = tx.DB().WithContext(ctx).Create(&entity.ContentRegistry{ID: registryID, Kind: "application", EntityID: id, AuthorID: row.ApplicantID, ContentVersion: 1, SchoolDetectionVersion: "pending"}).Error; e != nil {
			return "", e
		}
		epoch := a.row.RestrictionEpoch
		if e = tx.DB().WithContext(ctx).Create(&entity.ModerationTask{ID: taskID, ContentID: registryID, ContentVersion: 1, SubjectEpoch: tx.Actor().AuthEpoch, RestrictionEpoch: &epoch, HoldEpoch: a.snapshot.HoldEpoch, CreatedAt: tx.Now()}).Error; e != nil {
			return "", e
		}
		if e = tx.DB().WithContext(ctx).Model(&entity.Application{}).Where("id=?", id).Update("review_task_id", taskID).Error; e != nil {
			return "", e
		}
		if e = durable.Append(ctx, tx.DB(), s.policy.logger, ReviewRoute(), durable.Draft{WorkID: uuid.NewString(), OperationID: uuid.NewString(), EventID: uuid.NewString(), AggregateID: taskID, AggregateVersion: 1}); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		row, e := readApplication(ctx, tx.DB(), ref)
		if e != nil {
			return e
		}
		if row.ApplicantID != tx.Actor().ID || row.WishID != wishID {
			return command.ErrNotFound
		}
		out, e = s.applicationResult(ctx, tx.DB(), row, tx.Now(), false)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: s.applicationDigest(in)})
	return
}

// WithdrawApplication 只允许本人撤销未匹配申请，旧工作和案件在同一事务失效。
func (s *Service) WithdrawApplication(ctx context.Context, token, key, rid, id string, version int64) (out ApplicationResult, err error) {
	defer func() {
		if err != nil {
			out = ApplicationResult{}
		}
	}()
	defer s.interactionDone("application.withdraw", rid, time.Now(), &err)
	if !validWishID(id) || !validCommandKey(key) || version < 1 {
		return out, command.ErrInvalid
	}
	d := command.Definition{Kind: "application.withdraw", Method: "POST", ResourcePath: "/api/v1/applications/" + id + "/withdraw", ActorKind: "student", Access: command.Verified, RequiredExpectations: []string{"expected_version"}, Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
		return applicationPlan(ctx, db, id)
	}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		row, e := readApplication(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		if row.ApplicantID != tx.Actor().ID {
			return command.ErrNotFound
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		row, e := readApplication(ctx, tx.DB(), id)
		if e != nil {
			return "", e
		}
		if row.Status != "pending_scan" && row.Status != "pending_review" && row.Status != "active" {
			return "", command.ErrConflict
		}
		if e = CancelApplication(ctx, tx.DB(), row, "withdrawn", "applicant_withdrew"); e != nil {
			return "", e
		}
		if row.ReviewTaskID != nil {
			if e = obsoleteWork(ctx, tx, "moderation_task", []string{*row.ReviewTaskID}); e != nil {
				return "", e
			}
		}
		if e = obsoleteWork(ctx, tx, "application", []string{id}); e != nil {
			return "", e
		}
		return id, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		row, e := readApplication(ctx, tx.DB(), ref)
		if e != nil {
			return e
		}
		if ref != id || row.ApplicantID != tx.Actor().ID {
			return command.ErrNotFound
		}
		out, e = s.applicationResult(ctx, tx.DB(), row, tx.Now(), false)
		return e
	}
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Expectations: []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.Application, ID: id}, Value: version}}})
	return
}

// AcceptApplication 原子确定唯一应愿人、创建全新会话并使其他未终结申请失效。
// 任一步失败回滚全部结果及通知；重放重新检查当前关系，不把旧匹配历史当作当前授权。
func (s *Service) AcceptApplication(ctx context.Context, token, key, rid, id string, in AcceptInput) (out MatchResult, err error) {
	defer func() {
		if err != nil {
			out = MatchResult{}
		}
	}()
	defer s.interactionDone("application.accept", rid, time.Now(), &err)
	if !validWishID(id) || !validCommandKey(key) || in.ExpectedVersion < 1 || in.WishExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	var wishID string
	matchID, conversationID := uuid.NewString(), uuid.NewString()
	d := command.Definition{Kind: "application.accept", Method: "POST", ResourcePath: "/api/v1/applications/" + id + "/accept", ActorKind: "student", Access: command.Verified,
		Discover: func(ctx context.Context, db *gorm.DB, _ command.Subject) (command.Discovery, error) {
			return applicationPlan(ctx, db, id)
		}}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		row, e := readApplication(ctx, tx.DB(), id)
		if e != nil {
			return e
		}
		wishID = row.WishID
		a, e := readAggregate(ctx, tx.DB(), wishID, false)
		if e != nil {
			return e
		}
		if a.row.OwnerID != tx.Actor().ID {
			return command.ErrNotFound
		}
		ok, e := participantAllowed(ctx, tx.DB(), row.ApplicantID, tx.Now())
		if e != nil {
			return e
		}
		if !ok {
			return command.ErrForbidden
		}
		denied, e := blocked(ctx, tx.DB(), row.ApplicantID, a.row.OwnerID)
		if e != nil {
			return e
		}
		if denied {
			return command.ErrBlockedInteraction
		}
		if !ordinary(a.owner, a.snapshot) {
			return command.ErrForbidden
		}
		return nil
	}
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		row, e := readApplication(ctx, tx.DB(), id)
		if e != nil {
			return "", e
		}
		a, e := readAggregate(ctx, tx.DB(), row.WishID, false)
		if e != nil {
			return "", e
		}
		if row.Version != in.ExpectedVersion || a.row.Version != in.WishExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if row.Status == "pending_scan" || row.Status == "pending_review" {
			return "", command.ErrReviewPending
		}
		if row.Status != "active" {
			return "", command.ErrConflict
		}
		ok, e := ApplicationEligible(ctx, tx.DB(), row, tx.Now())
		if e != nil {
			return "", e
		}
		if !ok {
			return "", command.ErrConflict
		}
		var task entity.ModerationTask
		if e = tx.DB().WithContext(ctx).Where("id=? AND status='approved' AND decision='approved'", *row.ReviewTaskID).Take(&task).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return "", command.ErrConflict
			}
			return "", e
		}
		if len(row.PublicMessageCipher) == 0 {
			return "", command.ErrConflict
		}
		if e = tx.DB().WithContext(ctx).Model(&entity.Application{}).Where("id=?", id).Updates(map[string]any{"status": "accepted", "version": gorm.Expr("version+1")}).Error; e != nil {
			return "", e
		}
		if e = invalidateApplications(ctx, tx, wishID, "matched_elsewhere"); e != nil {
			return "", e
		}
		if e = tx.DB().WithContext(ctx).Create(&entity.Match{ID: matchID, ApplicationID: &id, WishID: wishID, RequesterID: a.row.OwnerID, HelperID: row.ApplicantID, Version: 1, StartedAt: tx.Now()}).Error; e != nil {
			return "", e
		}
		if e = tx.DB().WithContext(ctx).Create(&entity.Conversation{ID: conversationID, MatchID: matchID, CreatedAt: tx.Now()}).Error; e != nil {
			return "", e
		}
		next := a.snapshot
		next.State = Matching
		next.Version++
		if e = saveState(ctx, tx, a, next); e != nil {
			return "", e
		}
		if e = notify(ctx, tx.DB(), row.ApplicantID, "application_accepted", matchID); e != nil {
			return "", e
		}
		return matchID, nil
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var m entity.Match
		e := tx.DB().WithContext(ctx).Where("id=? AND application_id=? AND wish_id=? AND requester_id=? AND status='current'", ref, id, wishID, tx.Actor().ID).Take(&m).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return command.ErrConflict
		}
		if e != nil {
			return e
		}
		var c entity.Conversation
		if e = tx.DB().WithContext(ctx).Where("match_id=?", m.ID).Take(&c).Error; e != nil {
			return e
		}
		out = MatchResult{m.ID, c.ID, m.WishID, m.Version}
		return nil
	}
	// 心愿ID在引擎的只读发现阶段解析；版本分别在持有两个资源锁后的Apply校验，回执重放仍先重新授权。
	_, err = s.engine.Execute(ctx, d, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: in})
	return
}

func validApplicationStatus(s string) bool {
	return slices.Contains([]string{"pending_scan", "pending_review", "active", "accepted", "withdrawn", "rejected", "invalidated"}, s)
}
