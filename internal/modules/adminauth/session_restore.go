package adminauth

import (
	"context"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"gorm.io/gorm"
)

// Restore 重新复核数据库会话和当前权限，不延长到期时间或重新签发会话。
func (s *Service) Restore(ctx context.Context, token, requestID string) (out Session, err error) {
	start := time.Now()
	defer func() {
		if recover() != nil {
			err = command.ErrInternal
		}
		err = normalize(err)
		if err != nil {
			out = Session{}
		}
		s.complete("session_restore", requestID, start, err)
	}()
	ctx, cancel := context.WithTimeout(ctx, s.config.Command.Timeout)
	defer cancel()
	err = s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		if e := s.lockTimeout(ctx, db); e != nil {
			return e
		}
		actor, _, e := s.authorized(ctx, db, token)
		if e != nil {
			return e
		}
		out.Account, e = projection(ctx, db, actor.ID)
		return e
	})
	if err == nil {
		out.Token = token
	} else {
		out = Session{}
	}
	return
}
