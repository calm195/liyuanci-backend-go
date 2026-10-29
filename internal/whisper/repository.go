package whisper

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

type audience struct {
	ID           string
	Verification string
	Restricted   bool
}

func reader(ctx context.Context, db *gorm.DB, token string, now time.Time, optional bool) (a audience, err error) {
	if token == "" {
		if optional {
			return a, nil
		}
		return a, command.ErrUnauthenticated
	}
	hash := sha256.Sum256([]byte(token))
	var rows []audience
	e := db.WithContext(ctx).Raw(`SELECT a.id,a.verification,EXISTS(SELECT 1 FROM public.penalties p WHERE p.account_id=a.id AND p.revoked_at IS NULL AND p.action_kind IN ('mute','ban') AND p.starts_at<=? AND (p.ends_at IS NULL OR p.ends_at>?)) AS restricted FROM public.sessions s JOIN public.accounts a ON a.id=s.account_id WHERE s.subject_kind='student' AND s.token_hash=? AND s.auth_epoch=a.auth_epoch AND s.revoked_at IS NULL AND s.expires_at>? AND a.lifecycle='active'`, now, now, hash[:], now).Scan(&rows).Error
	if e != nil {
		return a, e
	}
	if len(rows) != 1 {
		return a, command.ErrUnauthenticated
	}
	return rows[0], nil
}

type aggregate struct {
	post       entity.WhisperPost
	version    entity.WhisperPostVersion
	owner      entity.Account
	alias      string
	privacy    bool
	isolated   bool
	restricted bool
}

func loadPost(ctx context.Context, db *gorm.DB, id string) (a aggregate, err error) {
	db = db.WithContext(ctx)
	if e := db.Where("id=?", id).Take(&a.post).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return a, command.ErrNotFound
		}
		return a, e
	}
	if e := db.Where("post_id=? AND content_version=?", id, a.post.CurrentContentVersion).Take(&a.version).Error; e != nil {
		return a, e
	}
	if e := db.Where("id=?", a.post.OwnerID).Take(&a.owner).Error; e != nil {
		return a, e
	}
	var flags struct{ Privacy, Isolated, Restricted bool }
	e := db.Raw(`SELECT EXISTS(SELECT 1 FROM public.privacy_holds WHERE target_kind='whisper_post' AND target_id=? AND status='active') AS privacy, EXISTS(SELECT 1 FROM public.content_restrictions r JOIN public.content_registry cr ON cr.id=r.content_id WHERE cr.kind='whisper_post' AND cr.entity_id=? AND r.active AND r.kind='crisis') AS isolated, EXISTS(SELECT 1 FROM public.content_restrictions r JOIN public.content_registry cr ON cr.id=r.content_id WHERE cr.kind='whisper_post' AND cr.entity_id=? AND r.active) AS restricted`, id, id, id).Scan(&flags).Error
	if e != nil {
		return a, e
	}
	a.privacy = flags.Privacy
	a.isolated = flags.Isolated
	a.restricted = flags.Restricted
	if a.version.NicknameVersion != nil {
		n, e := nicknameRow(ctx, db, a.post.OwnerID, *a.version.NicknameVersion)
		if e != nil {
			return a, e
		}
		safe, e := nicknameSafe(ctx, db, n)
		if e != nil {
			return a, e
		}
		if !safe {
			a.restricted = true
		}
	}
	var alias entity.WhisperAlias
	if e := db.Where("post_id=? AND actor_id=?", id, a.post.OwnerID).Take(&alias).Error; e != nil {
		return a, e
	}
	a.alias = alias.Label
	return a, nil
}

func postState(a aggregate) string {
	if a.privacy {
		return "privacy_hold"
	}
	if a.isolated {
		return "isolated"
	}
	if a.restricted {
		return "restricted"
	}
	if a.post.Lifecycle == "withdrawn" {
		return "withdrawn"
	}
	if a.post.ReviewState == "approved" {
		if a.post.PublishedContentVersion != nil && *a.post.PublishedContentVersion == a.post.CurrentContentVersion && a.post.PublishIntent {
			return "published"
		}
		return "cancelled"
	}
	if a.post.ReviewState == "pending" {
		return "review_pending"
	}
	return a.post.ReviewState
}

func nicknameRow(ctx context.Context, db *gorm.DB, owner string, version int64) (out entity.WhisperNicknameVersion, err error) {
	err = db.WithContext(ctx).Where("account_id=? AND nickname_version=?", owner, version).Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNicknameNotReady
	}
	return
}

func nicknameSafe(ctx context.Context, db *gorm.DB, n entity.WhisperNicknameVersion) (bool, error) {
	if n.RevokedAt != nil || n.ReviewState != "approved" || n.PublicLabel == nil {
		return false, nil
	}
	return nicknameUnblocked(ctx, db, n)
}

func nicknameUnblocked(ctx context.Context, db *gorm.DB, n entity.WhisperNicknameVersion) (bool, error) {
	var blocked bool
	e := db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM public.content_restrictions WHERE content_id=? AND active) OR EXISTS(SELECT 1 FROM public.privacy_holds WHERE target_kind='whisper_nickname' AND target_id=? AND status='active')`, n.ContentRegistryID, n.ContentRegistryID).Scan(&blocked).Error
	return !blocked, e
}

func preferences(ctx context.Context, db *gorm.DB, owner string) (p entity.WhisperPreference, err error) {
	err = db.WithContext(ctx).Where("account_id=?", owner).Take(&p).Error
	// 尚未写入的账号具有同一逻辑初始版本；首次写入在账号 guard 下懒初始化。
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entity.WhisperPreference{AccountID: owner, Version: 1, NextNicknameVersion: 1}, nil
	}
	return
}

func registry(ctx context.Context, db *gorm.DB, id, kind, entityID, owner string, version int64) error {
	return db.WithContext(ctx).Create(&entity.ContentRegistry{ID: id, Kind: kind, EntityID: entityID, AuthorID: owner, ContentVersion: version, Status: "pending", SchoolDetectionState: "pending", SchoolDetectionVersion: "pending"}).Error
}

func cancelReviews(ctx context.Context, db *gorm.DB, postID string) error {
	// 账号 guard 与父帖锁串行化业务写入；消费者必须重验父帖版本与评论代次。
	return db.WithContext(ctx).Exec(`UPDATE public.moderation_tasks SET status='cancelled',version=version+1 WHERE status IN ('pending','machine_passed','human_required') AND content_id IN (SELECT id FROM public.content_registry WHERE (kind='whisper_post' AND entity_id=?) OR (kind='whisper_comment' AND entity_id IN (SELECT id FROM public.whisper_comments WHERE post_id=?)))`, postID, postID).Error
}

func restrictedAccount(ctx context.Context, db *gorm.DB, id string, now time.Time) (bool, error) {
	var n int64
	e := db.WithContext(ctx).Model(&entity.Penalty{}).Where("account_id=? AND revoked_at IS NULL AND action_kind IN ? AND starts_at<=? AND (ends_at IS NULL OR ends_at>?)", id, []string{"mute", "ban"}, now, now).Count(&n).Error
	return n > 0, e
}

func insertRow(ctx context.Context, db *gorm.DB, row any) error {
	return db.WithContext(ctx).Create(row).Error
}
func postOwner(ctx context.Context, db *gorm.DB, id string, out *entity.WhisperPost) error {
	return db.WithContext(ctx).Select("owner_id").Where("id=?", id).Take(out).Error
}
func updatePost(ctx context.Context, db *gorm.DB, id string, updates map[string]any) error {
	return db.WithContext(ctx).Model(&entity.WhisperPost{}).Where("id=?", id).Updates(updates).Error
}
func startVersionReview(ctx context.Context, db *gorm.DB, id string, version int64, taskID string) error {
	return db.WithContext(ctx).Model(&entity.WhisperPostVersion{}).Where("post_id=? AND content_version=?", id, version).Updates(map[string]any{"review_state": "scanning", "review_task_id": taskID, "public_body": nil, "public_identity": nil}).Error
}
func resetRegistry(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Model(&entity.ContentRegistry{}).Where("id=?", id).Updates(map[string]any{"status": "pending", "school_related": nil, "school_detection_state": "pending", "school_detection_version": "pending"}).Error
}
func ensurePreference(ctx context.Context, db *gorm.DB, owner string) error {
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&entity.WhisperPreference{AccountID: owner, Version: 1, NextNicknameVersion: 1}).Error
}
func cancelNickname(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Model(&entity.WhisperNicknameVersion{}).Where("content_registry_id=?", id).Update("review_state", "cancelled").Error
}
func cancelTask(ctx context.Context, db *gorm.DB, id string) error {
	return db.WithContext(ctx).Model(&entity.ModerationTask{}).Where("id=? AND status IN ?", id, []string{"pending", "machine_passed", "human_required"}).Updates(map[string]any{"status": "cancelled", "version": gorm.Expr("version+1")}).Error
}
func replaceCandidate(ctx context.Context, db *gorm.DB, owner string, preferenceVersion, nicknameVersion int64) error {
	return db.WithContext(ctx).Model(&entity.WhisperPreference{}).Where("account_id=?", owner).Updates(map[string]any{"version": preferenceVersion, "candidate_nickname_version": nicknameVersion, "next_nickname_version": nicknameVersion + 1}).Error
}
func nicknameByRef(ctx context.Context, db *gorm.DB, ref, owner string, out *entity.WhisperNicknameVersion) error {
	return db.WithContext(ctx).Where("content_registry_id=? AND account_id=?", ref, owner).Take(out).Error
}
func reviewTask(ctx context.Context, db *gorm.DB, id, contentID string, version int64, out *entity.ModerationTask) error {
	return db.WithContext(ctx).Where("id=? AND content_id=? AND content_version=?", id, contentID, version).Take(out).Error
}
func countHugs(ctx context.Context, db *gorm.DB, id string, out *int64) error {
	return db.WithContext(ctx).Table("public.whisper_hugs AS h").Joins("JOIN public.accounts a ON a.id=h.actor_id").Where("h.post_id=? AND h.active AND a.lifecycle='active' AND NOT EXISTS(SELECT 1 FROM public.penalties p WHERE p.account_id=a.id AND p.revoked_at IS NULL AND p.action_kind IN ('mute','ban') AND p.starts_at<=clock_timestamp() AND (p.ends_at IS NULL OR p.ends_at>clock_timestamp()))", id).Count(out).Error
}
func viewerHugs(ctx context.Context, db *gorm.DB, id, viewer string, out *[]entity.WhisperHug) error {
	return db.WithContext(ctx).Where("post_id=? AND actor_id=?", id, viewer).Find(out).Error
}
func postCandidates(ctx context.Context, db *gorm.DB, own bool, actor string, position cursor, limit int) (out []entity.WhisperPost, err error) {
	column := "published_at"
	if own {
		column = "created_at"
	}
	query := db.WithContext(ctx).Model(&entity.WhisperPost{}).Select("id," + column)
	if own {
		query = query.Where("owner_id=?", actor)
	} else {
		query = query.Where("published_content_version IS NOT NULL")
	}
	if position.ID != "" {
		query = query.Where("("+column+",id)<(?,?)", position.At, position.ID)
	}
	err = query.Order(column + " DESC,id DESC").Limit(limit).Find(&out).Error
	return
}
