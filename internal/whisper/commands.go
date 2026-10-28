package whisper

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

func (s *Service) definition(kind, method, path, id string, restricted bool) command.Definition {
	access := command.Ordinary
	if restricted {
		access = command.Restricted
	}
	d := command.Definition{RetainReceipt: true, Kind: kind, Method: method, ResourcePath: path, ActorKind: "student", Access: access}
	d.Discover = func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
		if id == "" {
			return command.Discovery{}, nil
		}
		return command.Discovery{Resources: []command.Resource{{Kind: command.WhisperPost, ID: id}}}, nil
	}
	d.Authorize = func(ctx context.Context, tx *command.Tx) error {
		if id != "" {
			var p entity.WhisperPost
			if e := postOwner(ctx, tx.DB(), id, &p); e != nil {
				return e
			}
			if p.OwnerID != tx.Actor().ID {
				return command.ErrNotFound
			}
		}
		if !restricted {
			g, ok := tx.Guard(tx.Actor().ID)
			if !ok {
				return command.ErrInternal
			}
			if g.Verification != "verified" {
				return ErrVerificationRequired
			}
		}
		return nil
	}
	return d
}

func (s *Service) execute(ctx context.Context, d command.Definition, token, key, rid string, value any, expected int64, id, budget string, checks ...command.Expectation) (out command.Outcome, err error) {
	if !s.config.Enabled {
		return out, ErrModuleUnavailable
	}
	if !ValidKey(key) {
		return out, command.ErrInvalid
	}
	b, e := json.Marshal(value)
	if e != nil {
		return out, command.ErrInvalid
	}
	defer clear(b)
	input := command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: keyed(s.config.ReceiptKey, "whisper.command."+d.Kind, b)}
	if id != "" {
		d.RequiredExpectations = []string{"expected_version"}
		input.Expectations = []command.Expectation{{Name: "expected_version", Target: command.Resource{Kind: command.WhisperPost, ID: id}, Value: expected}}
	}
	if len(checks) > 0 {
		input.Expectations = checks
	}
	var lease string
	var callbackFailed bool
	var admissionErr error
	apply := d.Apply
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		// 引擎在回执重放与版本复核之后才进入此处；重试复用同一预算租约。
		// 限频只作短时安全准入，不发送业务消息；超时失败关闭，失败事务释放预留。
		if budget != "" && lease == "" {
			lease, admissionErr = s.admission.Reserve(ctx, tx.Actor().ID, budget)
			if admissionErr != nil {
				return "", command.ErrUnavailable
			}
		}
		ref, failure := apply(ctx, tx)
		callbackFailed = failure != nil
		return ref, failure
	}
	resolve := d.Resolve
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		failure := resolve(ctx, tx, ref)
		if failure != nil {
			callbackFailed = true
		}
		return failure
	}
	out, err = s.engine.Execute(ctx, d, input)
	if admissionErr != nil {
		err = admissionErr
	}
	if lease != "" && err != nil {
		// 存储不可用可能意味着提交结果不明，不能释放后放大已接受预算；回执重试负责确认。
		known := boundaryError(err)
		if callbackFailed || known != command.ErrUnavailable && known != command.ErrInternal {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			if e := s.admission.Release(cleanup, lease); e != nil {
				s.logger.Warn("树洞限频预留释放失败")
			}
		}
	}
	return
}

// CreatePost 原子写入草稿、独立随机别名和加密不可变版本，不产生审核工作。
func (s *Service) CreatePost(ctx context.Context, token, key, rid string, input ContentInput) (out OwnerPost, err error) {
	defer s.completed("post_create", rid, time.Now(), &err)
	if !validText(input.Body, 1, 500) || !identityShape(input.Identity) {
		return out, command.ErrInvalid
	}
	id := uuid.NewString()
	d := s.definition("whisper.post.create", "POST", "/api/v1/me/whispers/posts", "", false)
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		if e := s.validateIdentity(ctx, tx.DB(), tx.Actor().ID, input.Identity); e != nil {
			return "", e
		}
		now := tx.Now()
		if e := insertRow(ctx, tx.DB(), &entity.WhisperPost{ID: id, OwnerID: tx.Actor().ID, Version: 1, CurrentContentVersion: 1, Lifecycle: "active", ReviewState: "draft", CommentsOpen: true, CommentsEpoch: 1, CreatedAt: now, UpdatedAt: now}); e != nil {
			return "", e
		}
		label, e := randomAlias()
		if e != nil {
			return "", e
		}
		if e = insertRow(ctx, tx.DB(), &entity.WhisperAlias{PostID: id, ActorID: tx.Actor().ID, Label: label}); e != nil {
			return "", e
		}
		return id, s.writeVersion(ctx, tx, id, 1, input)
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var e error
		out, e = s.ownerPost(ctx, tx.DB(), tx.Now(), ref, tx.Actor().ID)
		return e
	}
	_, err = s.execute(ctx, d, token, key, rid, input, 0, "", "post")
	return
}

func (s *Service) writeVersion(ctx context.Context, tx *command.Tx, id string, version int64, input ContentInput) error {
	body, e := s.encrypt(ctx, "whisper_post_body", id, version, input.Body)
	if e != nil {
		return e
	}
	identity, e := s.encrypt(ctx, "whisper_post_identity", id, version, input.Identity)
	if e != nil {
		return e
	}
	registryID := uuid.NewString()
	if e = registry(ctx, tx.DB(), registryID, "whisper_post", id, tx.Actor().ID, version); e != nil {
		return e
	}
	return insertRow(ctx, tx.DB(), &entity.WhisperPostVersion{PostID: id, ContentVersion: version, ContentRegistryID: registryID, BodyCipher: body, IdentityMode: input.Identity.Mode, IdentityCipher: identity, NicknameVersion: input.Identity.NicknameVersion, DictionaryVersion: input.Identity.DictionaryVersion, ReviewState: "draft", CreatedAt: tx.Now()})
}

// ChangePost 在同一父帖锁下保存、送审或撤回，重放只重查当前结果。
func (s *Service) ChangePost(ctx context.Context, token, key, rid, id, action string, input PostInput) (out OwnerPost, err error) {
	defer s.completed("post_"+action, rid, time.Now(), &err)
	if !ValidID(id) || input.ExpectedVersion < 1 || !slices.Contains([]string{"update", "submit", "withdraw"}, action) {
		return out, command.ErrInvalid
	}
	if action == "update" && (!validText(input.Body, 1, 500) || !identityShape(input.Identity)) {
		return out, command.ErrInvalid
	}
	path := "/api/v1/me/whispers/posts/" + id
	method := "POST"
	if action == "update" {
		method = "PUT"
	} else {
		path += "/" + action
	}
	d := s.definition("whisper.post."+action, method, path, id, action == "withdraw")
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		a, e := loadPost(ctx, tx.DB(), id)
		if e != nil {
			return "", e
		}
		state := postState(a)
		if action != "withdraw" && slices.Contains([]string{"privacy_hold", "isolated", "restricted"}, state) {
			return "", command.ErrConflict
		}
		updates := map[string]any{"version": a.post.Version + 1, "updated_at": tx.Now()}
		switch action {
		case "update":
			if e = s.validateIdentity(ctx, tx.DB(), tx.Actor().ID, input.Identity); e != nil {
				return "", e
			}
			if e = cancelReviews(ctx, tx.DB(), id); e != nil {
				return "", e
			}
			if e = s.writeVersion(ctx, tx, id, a.post.CurrentContentVersion+1, input.ContentInput); e != nil {
				return "", e
			}
			updates["current_content_version"] = a.post.CurrentContentVersion + 1
			updates["review_state"] = "draft"
			updates["lifecycle"] = "active"
			updates["publish_intent"] = false
			updates["comments_epoch"] = a.post.CommentsEpoch + 1
			updates["published_content_version"] = nil
			updates["published_at"] = nil
		case "submit":
			if !slices.Contains([]string{"draft", "rejected", "withdrawn", "cancelled"}, state) {
				return "", command.ErrConflict
			}
			var identity IdentityInput
			if e = s.decrypt(ctx, "whisper_post_identity", id, a.post.CurrentContentVersion, a.version.IdentityCipher, &identity); e != nil {
				return "", e
			}
			if e = s.validateIdentity(ctx, tx.DB(), tx.Actor().ID, identity); e != nil {
				return "", e
			}
			if s.review == nil {
				return "", command.ErrUnavailable
			}
			if e = cancelReviews(ctx, tx.DB(), id); e != nil {
				return "", e
			}
			taskID, e := s.submit(ctx, tx, a.version.ContentRegistryID, "whisper_post", a.post.CurrentContentVersion, a.post.ReviewRound+1, a.post.PrivacyEpoch, a.post.RestrictionEpoch)
			if e != nil {
				return "", e
			}
			if e = startVersionReview(ctx, tx.DB(), id, a.post.CurrentContentVersion, taskID); e != nil {
				return "", e
			}
			updates["review_state"] = "scanning"
			updates["review_round"] = a.post.ReviewRound + 1
			updates["lifecycle"] = "active"
			updates["publish_intent"] = true
			updates["published_content_version"] = nil
			updates["published_at"] = nil
		case "withdraw":
			if e = cancelReviews(ctx, tx.DB(), id); e != nil {
				return "", e
			}
			updates["lifecycle"] = "withdrawn"
			updates["publish_intent"] = false
			updates["comments_epoch"] = a.post.CommentsEpoch + 1
			updates["published_content_version"] = nil
			updates["published_at"] = nil
		}
		e = updatePost(ctx, tx.DB(), id, updates)
		return id, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		if ref != id {
			return command.ErrNotFound
		}
		var e error
		out, e = s.ownerPost(ctx, tx.DB(), tx.Now(), ref, tx.Actor().ID)
		return e
	}
	budget := "maintenance"
	if action == "submit" {
		budget = "post"
	}
	_, err = s.execute(ctx, d, token, key, rid, input, input.ExpectedVersion, id, budget)
	return
}

func (s *Service) submit(ctx context.Context, tx *command.Tx, contentID, kind string, version, round, hold, restriction int64) (string, error) {
	if s.review == nil {
		return "", command.ErrUnavailable
	}
	g, ok := tx.Guard(tx.Actor().ID)
	if !ok {
		return "", command.ErrInternal
	}
	task := entity.ModerationTask{ID: uuid.NewString(), ContentID: contentID, ContentVersion: version, SubjectEpoch: g.AuthEpoch, HoldEpoch: hold, RestrictionEpoch: &restriction, Status: "pending", CreatedAt: tx.Now()}
	if e := insertRow(ctx, tx.DB(), &task); e != nil {
		return "", e
	}
	if e := resetRegistry(ctx, tx.DB(), contentID); e != nil {
		return "", e
	}
	e := s.review.Append(ctx, tx.DB(), ReviewSubmission{TaskID: task.ID, ContentID: contentID, Kind: kind, ContentVersion: version, Round: round})
	return task.ID, e
}

// SetNickname 只替换候选指针，旧 active 与历史内容快照保持不变。
func (s *Service) SetNickname(ctx context.Context, token, key, rid string, input NicknameInput) (out NicknameState, err error) {
	defer s.completed("nickname_set", rid, time.Now(), &err)
	if !validText(input.Label, 2, 20) || input.ExpectedVersion < 1 {
		return out, command.ErrInvalid
	}
	d := s.definition("whisper.nickname.set", "PUT", "/api/v1/me/whispers/nickname", "", false)
	// 昵称设置只要求 normal，不以学生认证为门槛。
	d.Authorize = func(context.Context, *command.Tx) error { return nil }
	d.Apply = func(ctx context.Context, tx *command.Tx) (string, error) {
		if s.review == nil {
			return "", command.ErrUnavailable
		}
		p, e := preferences(ctx, tx.DB(), tx.Actor().ID)
		if e != nil {
			return "", e
		}
		if p.Version != input.ExpectedVersion {
			return "", command.ErrVersionConflict
		}
		if e = ensurePreference(ctx, tx.DB(), p.AccountID); e != nil {
			return "", e
		}
		if p.CandidateNicknameVersion != nil {
			old, e := nicknameRow(ctx, tx.DB(), p.AccountID, *p.CandidateNicknameVersion)
			if e != nil {
				return "", e
			}
			if slices.Contains([]string{"scanning", "pending"}, old.ReviewState) {
				if e = cancelNickname(ctx, tx.DB(), old.ContentRegistryID); e != nil {
					return "", e
				}
			}
			if old.ReviewTaskID != nil {
				if e = cancelTask(ctx, tx.DB(), *old.ReviewTaskID); e != nil {
					return "", e
				}
			}
		}
		registryID := uuid.NewString()
		version := p.NextNicknameVersion
		cipher, e := s.encrypt(ctx, "whisper_nickname", registryID, version, input.Label)
		if e != nil {
			return "", e
		}
		if e = registry(ctx, tx.DB(), registryID, "whisper_nickname", registryID, p.AccountID, version); e != nil {
			return "", e
		}
		taskID, e := s.submit(ctx, tx, registryID, "whisper_nickname", version, 1, 0, 0)
		if e != nil {
			return "", e
		}
		n := entity.WhisperNicknameVersion{AccountID: p.AccountID, NicknameVersion: version, ContentRegistryID: registryID, LabelCipher: cipher, ReviewState: "scanning", ControlVersion: 1, ReviewRound: 1, ReviewTaskID: &taskID, CreatedAt: tx.Now()}
		if e = insertRow(ctx, tx.DB(), &n); e != nil {
			return "", e
		}
		e = replaceCandidate(ctx, tx.DB(), p.AccountID, p.Version+1, version)
		return registryID, e
	}
	d.Resolve = func(ctx context.Context, tx *command.Tx, ref string) error {
		var n entity.WhisperNicknameVersion
		e := nicknameByRef(ctx, tx.DB(), ref, tx.Actor().ID, &n)
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return command.ErrNotFound
		}
		if e != nil {
			return e
		}
		var failure error
		out, failure = s.nicknameState(ctx, tx.DB(), tx.Now(), tx.Actor().ID)
		return failure
	}
	_, err = s.execute(ctx, d, token, key, rid, input, 0, "", "nickname")
	return
}

func (s *Service) validateIdentity(ctx context.Context, db *gorm.DB, owner string, i IdentityInput) error {
	if !identityShape(i) {
		return command.ErrInvalid
	}
	switch i.Mode {
	case "semi":
		c, e := s.resources(ctx)
		if e != nil {
			return e
		}
		if c.Grades.Version == nil {
			return ErrModeUnavailable
		}
		if *i.DictionaryVersion != *c.Grades.Version {
			return ErrDictionaryChanged
		}
		if !tagsValid(i.Tags, c.Grades) {
			return command.ErrInvalid
		}
	case "named":
		p, e := preferences(ctx, db, owner)
		if e != nil {
			return e
		}
		if p.ActiveNicknameVersion == nil || *p.ActiveNicknameVersion != *i.NicknameVersion {
			return ErrNicknameNotReady
		}
		n, e := nicknameRow(ctx, db, owner, *i.NicknameVersion)
		if e != nil {
			return e
		}
		safe, e := nicknameSafe(ctx, db, n)
		if e != nil {
			return e
		}
		if !safe {
			return ErrNicknameNotReady
		}
	}
	return nil
}
