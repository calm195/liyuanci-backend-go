package command

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// Engine 拥有命令事务及提交后的完成日志，不依赖 Redis 权限或 MQ 投递结果。
type Engine struct {
	db     *gorm.DB
	logger *zap.Logger
	config Config
}

// New 拒绝外部事务，避免将 savepoint 成功误记为整个命令已提交。
func New(db *gorm.DB, logger *zap.Logger, config Config) (*Engine, error) {
	if db == nil || db.Statement == nil || logger == nil || config.Timeout <= 0 || config.LockTimeout < time.Millisecond || config.LockTimeout > config.Timeout || config.ReceiptTTL <= 0 || config.MaxAttempts < 1 || config.MaxAttempts > 10 {
		return nil, ErrInvalid
	}
	if _, nested := db.Statement.ConnPool.(gorm.TxCommitter); nested {
		return nil, ErrInvalid
	}
	return &Engine{db: db.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), logger: logger, config: config}, nil
}

func uuid(value string) bool {
	ok, _ := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, value)
	return ok
}

func validDefinition(d Definition) bool {
	required := map[string]bool{}
	for _, name := range d.RequiredExpectations {
		if required[name] || !slices.Contains([]string{"expected_version", "wish_expected_version", "expected_write_epoch", "expected_post_version", "expected_comments_epoch"}, name) {
			return false
		}
		required[name] = true
	}
	kind, _ := regexp.MatchString(`^[a-z][a-z0-9_.]{0,63}$`, d.Kind)
	if !kind || d.ResourcePath == "" || !strings.HasPrefix(d.ResourcePath, "/") || path.Clean(d.ResourcePath) != d.ResourcePath || strings.ContainsAny(d.ResourcePath, "?#%\r\n") || d.Discover == nil || d.Authorize == nil || d.Apply == nil || d.Resolve == nil {
		return false
	}
	if !slices.Contains([]string{"POST", "PUT", "PATCH", "DELETE"}, d.Method) {
		return false
	}
	if d.ActorKind == "student" {
		return len(d.AllowedRoles) == 0 && slices.Contains([]Access{Restricted, Ordinary, Verified}, d.Access)
	}
	if d.ActorKind != "admin" || d.Access != "" || len(d.AllowedRoles) == 0 {
		return false
	}
	for _, role := range d.AllowedRoles {
		if !slices.Contains([]string{"review_operator", "crisis_operator", "super_admin"}, role) {
			return false
		}
	}
	return true
}

func requestHash(d Definition, input Input) ([]byte, error) {
	checks := append([]Expectation(nil), input.Expectations...)
	slices.SortFunc(checks, func(a, b Expectation) int {
		return strings.Compare(string(a.Target.Kind)+a.Target.ID+a.Name, string(b.Target.Kind)+b.Target.ID+b.Name)
	})
	seen := make(map[Resource]string)
	names := map[string]bool{}
	for _, check := range checks {
		// 父帖版本与评论代次绑定同一资源，但各自校验，不能互相替代。
		previous := seen[check.Target]
		parentPair := check.Target.Kind == WhisperPost && ((previous == "expected_post_version" && check.Name == "expected_comments_epoch") || (previous == "expected_comments_epoch" && check.Name == "expected_post_version"))
		if !uuid(check.Target.ID) || check.Value <= 0 || (previous != "" && !parentPair) || names[check.Name] {
			return nil, ErrInvalid
		}
		seen[check.Target] = check.Name
		names[check.Name] = true
		switch check.Name {
		case "expected_version":
			if !slices.Contains([]ResourceKind{Account, IdentityReview, Wish, Application, Message, ModerationTask, Notification, Report, Penalty, Appeal, CrisisCase, EvidenceCase, WhisperPost, WhisperComment, WhisperNickname, WhisperCapsule}, check.Target.Kind) {
				return nil, ErrInvalid
			}
		case "expected_post_version", "expected_comments_epoch":
			if check.Target.Kind != WhisperPost {
				return nil, ErrInvalid
			}
		case "wish_expected_version":
			if check.Target.Kind != Wish {
				return nil, ErrInvalid
			}
		case "expected_write_epoch":
			if check.Target.Kind != Conversation {
				return nil, ErrInvalid
			}
		default:
			return nil, ErrInvalid
		}
	}
	for _, name := range d.RequiredExpectations {
		if !names[name] {
			return nil, ErrInvalid
		}
	}
	encoded, err := json.Marshal(struct {
		Kind    string
		Request any
		Checks  []Expectation
	}{d.Kind, input.Request, checks})
	if err != nil {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256(encoded)
	return sum[:], nil
}

// Execute 在每次重试重新发现参与者，成功日志只在根事务提交之后记录。
func (e *Engine) Execute(ctx context.Context, d Definition, input Input) (out Outcome, err error) {
	start := time.Now()
	recovered := false
	safeKind := ""
	safeRequestID := ""
	defer func() {
		if recover() != nil {
			recovered = true
			err = ErrInternal
		}
		if err != nil {
			out.Ref = ""
			out.Replayed = false
		}
		result := "success"
		if err != nil {
			result = "failure"
		}
		fields := []zap.Field{zap.String("operation", safeKind), zap.String("result", result), zap.String("code", resultCode(err)), zap.Int("attempts", out.Attempts), zap.Duration("duration", time.Since(start)), zap.Bool("recovered", recovered)}
		if safeRequestID != "" {
			fields = append(fields, zap.String("request_id", safeRequestID))
		}
		if err != nil {
			e.logger.Error("数据库命令完成", fields...)
		} else {
			e.logger.Info("数据库命令完成", fields...)
		}
	}()
	if !validDefinition(d) || input.Token == "" || input.IdempotencyKey == "" {
		return out, ErrInvalid
	}
	safeKind = d.Kind
	if input.RequestID != "" {
		valid, _ := regexp.MatchString(`^[A-Za-z0-9_-]{1,128}$`, input.RequestID)
		if !valid {
			return out, ErrInvalid
		}
		safeRequestID = input.RequestID
	}
	digest, err := requestHash(d, input)
	if err != nil {
		return out, err
	}
	key := sha256.Sum256([]byte(input.IdempotencyKey))
	token := sha256.Sum256([]byte(input.Token))
	ctx, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()
	for attempt := 1; attempt <= e.config.MaxAttempts; attempt++ {
		out.Attempts = attempt
		if err = ctx.Err(); err != nil {
			return out, err
		}
		var subject Subject
		var discovered Discovery
		// 初次发现使用真正的只读事务，错误写入不能产生提前提交的业务副作用。
		err = e.db.WithContext(ctx).Transaction(func(reader *gorm.DB) error {
			var subjects []Subject
			// 只读发现使用单次联表快照；资格检查仍在取得 guard 后重新执行。
			query := reader.Model(&entity.Session{}).Table("public.sessions AS s").
				Select("s.subject_kind AS kind", "s.subject_id AS id").
				Where("s.token_hash = ? AND s.subject_kind = ? AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp() AND s.auth_epoch = a.auth_epoch", token[:], d.ActorKind)
			if d.ActorKind == "admin" {
				query = query.Joins("JOIN public.admin_accounts AS a ON a.id = s.admin_id").Where("a.status = ?", "active")
			} else {
				query = query.Joins("JOIN public.accounts AS a ON a.id = s.account_id").Where("a.lifecycle = ?", "active")
			}
			if failure := query.Find(&subjects).Error; failure != nil {
				return failure
			}
			if len(subjects) != 1 {
				return ErrUnauthenticated
			}
			subject = subjects[0]
			found, failure := d.Discover(ctx, reader, subject)
			if failure != nil {
				return failure
			}
			discovered, failure = normalize(found, subject)
			return failure
		}, &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true})
		if err == nil {
			err = e.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
				// 身份唯一占用锁先于所有主体锁，避免与登录和密钥轮换形成反向等待。
				if d.IdentityLock {
					if failure := db.Exec("SELECT pg_catalog.pg_advisory_xact_lock(624109174)").Error; failure != nil {
						return failure
					}
				}
				tx, failure := e.lock(ctx, db, subject, discovered)
				if failure != nil {
					return failure
				}
				// 锁账号后及锁资源后分别重读，任一集合变化均退出事务，禁止追加锁。
				found, failure := d.Discover(ctx, db, subject)
				if failure != nil {
					return failure
				}
				normalized, failure := normalize(found, subject)
				if failure != nil {
					return failure
				}
				if !same(discovered, normalized) {
					return ErrRetry
				}
				if failure = tx.lockResources(discovered.Resources); failure != nil {
					return failure
				}
				found, failure = d.Discover(ctx, db, subject)
				if failure != nil {
					return failure
				}
				normalized, failure = normalize(found, subject)
				if failure != nil {
					return failure
				}
				if !same(discovered, normalized) {
					return ErrRetry
				}
				if failure = db.Raw("SELECT clock_timestamp()").Scan(&tx.now).Error; failure != nil {
					return failure
				}
				if failure = e.authorize(db, tx, d, token[:]); failure != nil {
					return failure
				}
				if failure = d.Authorize(ctx, tx); failure != nil {
					return failure
				}
				scope := d.Method + " " + d.ResourcePath
				var receipts []struct {
					RequestHash    []byte
					ResultRef      string
					ExpiresAt      time.Time
					LifecycleBound bool
				}
				if failure = db.Model(&entity.CommandReceipt{}).Select("request_hash,result_ref,expires_at,lifecycle_bound").Where("actor_kind=? AND actor_id=? AND command_kind=? AND key_hash=?", subject.Kind, subject.ID, scope, key[:]).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&receipts).Error; failure != nil {
					return failure
				}
				// 同一主体 guard 串行化同键操作；数据库唯一键兜底，回执和业务结果同事务。
				// 生命周期回执不因技术到期被覆盖；普通回执仍检查期限，零行表示并发状态变化。
				if len(receipts) == 1 && (receipts[0].LifecycleBound || receipts[0].ExpiresAt.After(tx.now)) {
					if !slices.Equal(receipts[0].RequestHash, digest) {
						return ErrIdempotencyConflict
					}
					if failure = d.Resolve(ctx, tx, receipts[0].ResultRef); failure != nil {
						return failure
					}
					out.Ref = receipts[0].ResultRef
					out.Replayed = true
					return nil
				}
				for _, check := range input.Expectations {
					var actual int64
					if check.Target.Kind == Account {
						guard, present := tx.guards[check.Target.ID]
						if !present {
							return ErrInvalid
						}
						actual = guard.Version
					} else {
						snapshot, present := tx.resources[check.Target]
						if !present {
							return ErrInvalid
						}
						actual = snapshot.Version
						if check.Name == "expected_write_epoch" {
							actual = snapshot.WriteEpoch
						} else if check.Name == "expected_comments_epoch" {
							actual = snapshot.CommentsEpoch
						}
					}
					if actual != check.Value {
						return ErrVersionConflict
					}
				}
				if d.PersistIntent != nil {
					if failure = d.PersistIntent(ctx, tx); failure != nil {
						return failure
					}
				}
				ref, failure := d.Apply(ctx, tx)
				if failure != nil {
					return failure
				}
				if !uuid(ref) {
					return ErrInternal
				}
				if failure = d.Resolve(ctx, tx, ref); failure != nil {
					return failure
				}
				assignments := []string{"request_hash", "result_ref", "expires_at"}
				if d.RetainReceipt {
					assignments = append(assignments, "lifecycle_bound")
				}
				saved := db.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "actor_kind"}, {Name: "actor_id"}, {Name: "command_kind"}, {Name: "key_hash"}},
					DoUpdates: clause.AssignmentColumns(assignments),
					Where:     clause.Where{Exprs: []clause.Expression{clause.Lte{Column: clause.Column{Table: "command_receipts", Name: "expires_at"}, Value: tx.now}, clause.Eq{Column: clause.Column{Table: "command_receipts", Name: "lifecycle_bound"}, Value: false}}},
				}).Create(&entity.CommandReceipt{
					LifecycleBound: d.RetainReceipt, ActorKind: subject.Kind, ActorID: subject.ID, CommandKind: scope, KeyHash: key[:],
					RequestHash: digest, ResultRef: ref, ExpiresAt: tx.now.Add(e.config.ReceiptTTL),
				})
				if saved.Error != nil {
					return saved.Error
				}
				if saved.RowsAffected != 1 {
					return ErrRetry
				}
				out.Ref = ref
				out.Replayed = false
				return nil
			}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		}
		err = normalizeError(err)
		if err == nil {
			return out, nil
		}
		out.Ref = ""
		out.Replayed = false
		if !errors.Is(err, ErrRetry) && !errors.Is(err, ErrVersionConflict) {
			return out, err
		}
	}
	return out, err
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrClientUpgradeRequired, ErrVerificationRequired, ErrModeUnavailable, ErrDictionaryChanged, ErrNicknameNotReady, ErrWarningWait, ErrInvalid, ErrUnauthenticated, ErrForbidden, ErrNotFound, ErrVersionConflict, ErrIdempotencyConflict, ErrConflict, ErrAssetNotReady, ErrCapsuleLimit, ErrActiveWishLimit, ErrDuplicateApplication, ErrBlockedInteraction, ErrReviewPending, ErrRetry, ErrUnavailable, ErrInternal, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		switch state.SQLState() {
		case "40001", "40P01", "55P03":
			return ErrRetry
		case "25006":
			return ErrInternal
		case "23505", "23503", "23514":
			return ErrConflict
		}
	}
	return ErrUnavailable
}

func resultCode(err error) string {
	if err == nil {
		return "success"
	}
	switch {
	case errors.Is(err, ErrWarningWait):
		return "warning_wait_required"
	case errors.Is(err, ErrInvalid):
		return "invalid"
	case errors.Is(err, ErrUnauthenticated):
		return "unauthenticated"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrVersionConflict):
		return "version_conflict"
	case errors.Is(err, ErrIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, ErrAssetNotReady):
		return "asset_not_ready"
	case errors.Is(err, ErrCapsuleLimit):
		return "capsule_limit"
	case errors.Is(err, ErrActiveWishLimit):
		return "active_wish_limit"
	case errors.Is(err, ErrDuplicateApplication):
		return "duplicate_application"
	case errors.Is(err, ErrBlockedInteraction):
		return "blocked_interaction"
	case errors.Is(err, ErrReviewPending):
		return "review_pending"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrRetry):
		return "retry_exhausted"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	case errors.Is(err, ErrInternal):
		return "internal"
	default:
		return "unavailable"
	}
}
