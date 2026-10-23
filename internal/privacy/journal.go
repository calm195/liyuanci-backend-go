package privacy

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/securedata"
)

// Intent 只包含恢复删除必需的随机引用，不复制身份、正文或管理员理由。
type Intent struct {
	ID     string `json:"id"`
	Scope  string `json:"scope"`
	Target string `json:"target"`
}

// Journal 必须使用独立于业务恢复的持久资源；Append成功即授权恢复程序继续删除。
// Pending不得漏过未确认意图，Confirm只在普通清理及外部副本适配器全部确认后调用。
type Journal interface {
	Append(context.Context, Intent) error
	Pending(context.Context, string, int) ([]Intent, error)
	Confirm(context.Context, string) error
}

// PostgresJournal 使用独立池；宿主分别提供writer/reader凭据，不与业务库同备份回滚。
// 构造器不执行DDL，不把不同逻辑库视为已证明物理故障隔离。
type PostgresJournal struct {
	writer, reader *gorm.DB
	codec          *securedata.Codec
	logger         *zap.Logger
	timeout        time.Duration
}

// NewJournal 校验独立日志布局，拒绝含业务账号表的连接及外部事务。
func NewJournal(ctx context.Context, writer, reader *gorm.DB, codec *securedata.Codec, logger *zap.Logger, timeout time.Duration) (*PostgresJournal, error) {
	if writer == nil || reader == nil || codec == nil || logger == nil || timeout <= 0 {
		return nil, command.ErrInvalid
	}
	type location struct {
		Name, Host string
		Started    time.Time
		Port       int
		TableID    uint32
	}
	var origin location
	for i, db := range []*gorm.DB{writer, reader} {
		if db.Statement == nil {
			return nil, command.ErrInvalid
		}
		if _, ok := db.Statement.ConnPool.(gorm.TxCommitter); ok {
			return nil, command.ErrInvalid
		}
		var valid bool
		c, cancel := context.WithTimeout(ctx, timeout)
		e := db.WithContext(c).Raw("SELECT to_regclass('public.recovery_delete_journal') IS NOT NULL AND to_regclass('public.accounts') IS NULL").Scan(&valid).Error
		cancel()
		if e != nil || !valid {
			return nil, command.ErrUnavailable
		}
		var current location
		c, cancel = context.WithTimeout(ctx, timeout)
		e = db.WithContext(c).Raw("SELECT current_database() AS name,coalesce(inet_server_addr()::text,'local') AS host,coalesce(inet_server_port(),0) AS port,to_regclass('public.recovery_delete_journal')::oid AS table_id,pg_postmaster_start_time() AS started").Scan(&current).Error
		cancel()
		if e != nil {
			return nil, command.ErrUnavailable
		}
		if i == 0 {
			origin = current
		} else if current != origin {
			return nil, command.ErrInvalid
		}
	}
	return &PostgresJournal{writer: writer.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), reader: reader.Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}), codec: codec, logger: logger, timeout: timeout}, nil
}
func (j *PostgresJournal) done(op string, start time.Time, err *error) {
	if recover() != nil {
		*err = command.ErrInternal
	}
	if *err != nil {
		*err = command.ErrUnavailable
	}
	result := "success"
	if *err != nil {
		result = "failure"
	}
	j.logger.Info("独立删除日志操作完成", zap.String("operation", op), zap.String("result", result), zap.Duration("duration", time.Since(start)))
}
func validIntent(in Intent) bool {
	return validID(in.ID) && validID(in.Target) && (in.Scope == "account" || in.Scope == "evidence")
}
func (j *PostgresJournal) decode(ctx context.Context, row entity.RecoveryDeleteJournal) (in Intent, err error) {
	var env securedata.Envelope
	if json.Unmarshal(row.EntryEnvelope, &env) != nil {
		return in, command.ErrUnavailable
	}
	b, e := j.codec.Decrypt(ctx, securedata.Binding{Purpose: "recovery_delete_intent", ObjectID: row.IntentID, Version: 1}, env)
	defer clear(b)
	if e != nil || json.Unmarshal(b, &in) != nil || !validIntent(in) || in.ID != row.IntentID || in.Scope != row.DeletionScope {
		return Intent{}, command.ErrUnavailable
	}
	return in, nil
}

// Append 幂等保留首次加密意图，拒绝同标识指向其他目标，不更新历史删除范围。
func (j *PostgresJournal) Append(ctx context.Context, in Intent) (err error) {
	defer j.done("append", time.Now(), &err)
	if !validIntent(in) {
		return command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	defer clear(b)
	env, e := j.codec.Encrypt(ctx, securedata.Binding{Purpose: "recovery_delete_intent", ObjectID: in.ID, Version: 1}, b)
	if e != nil {
		return e
	}
	raw, e := json.Marshal(env)
	if e != nil {
		return e
	}
	return j.writer.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entity.RecoveryDeleteJournal{IntentID: in.ID, DeletionScope: in.Scope, EntryEnvelope: raw}).Error; e != nil {
			return e
		}
		var row entity.RecoveryDeleteJournal
		if e := db.Where("intent_id=?", in.ID).Take(&row).Error; e != nil {
			return e
		}
		old, e := j.decode(ctx, row)
		if e != nil {
			return e
		}
		if old != in {
			return errors.New("删除意图不一致")
		}
		return nil
	})
}

// Pending 按技术序号读取所有未确认意图；每批重扫，不依赖易丢失的内存游标。
func (j *PostgresJournal) Pending(ctx context.Context, after string, limit int) (out []Intent, err error) {
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	defer j.done("pending", time.Now(), &err)
	if limit < 1 || limit > 100 || after != "" && !validID(after) {
		return nil, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	var rows []entity.RecoveryDeleteJournal
	if err = j.reader.WithContext(ctx).Where("confirmed_at IS NULL AND intent_id::text>?", after).Order("intent_id").Limit(limit).Find(&rows).Error; err != nil {
		return
	}
	for _, row := range rows {
		in, e := j.decode(ctx, row)
		if e != nil {
			return nil, e
		}
		out = append(out, in)
	}
	return
}

// Confirm 只更新确认时间，不清除恢复依据；到期清理由独立副本窗口验收决定。
func (j *PostgresJournal) Confirm(ctx context.Context, id string) (err error) {
	defer j.done("confirm", time.Now(), &err)
	if !validID(id) {
		return command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	r := j.writer.WithContext(ctx).Model(&entity.RecoveryDeleteJournal{}).Where("intent_id=?", id).Update("confirmed_at", gorm.Expr("coalesce(confirmed_at,clock_timestamp())"))
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return command.ErrNotFound
	}
	return nil
}

// AllIntents 供隔离恢复使用，包含已确认意图；业务备份不能以旧完成状态跳过重删。
// 本函数不授权对外开放，宿主须在全量重删及副本核验后人工放行。
func (j *PostgresJournal) AllIntents(ctx context.Context, after string, limit int) (out []Intent, err error) {
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	defer j.done("recovery.scan", time.Now(), &err)
	if limit < 1 || limit > 100 || after != "" && !validID(after) {
		return nil, command.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, j.timeout)
	defer cancel()
	var rows []entity.RecoveryDeleteJournal
	if err = j.reader.WithContext(ctx).Where("intent_id::text>?", after).Order("intent_id").Limit(limit).Find(&rows).Error; err != nil {
		return
	}
	for _, row := range rows {
		in, e := j.decode(ctx, row)
		if e != nil {
			return nil, e
		}
		out = append(out, in)
	}
	return
}
