package chat

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
)

// CareCard 是本人关怀资料，不包含后台案件标识、正文或任何学号。
type CareCard struct {
	ID        string          `json:"id"`
	Resources json.RawMessage `json:"resources"`
}

// Care 复核当前存续学生会话与卡片归属，受罚本人也可使用援助资料。
func (s *Service) Care(ctx context.Context, token, rid, id string) (out CareCard, err error) {
	defer func() {
		if err != nil {
			out = CareCard{}
		}
	}()
	defer s.done("care.read", rid, time.Now(), &err)
	if !validID(id) {
		return out, command.ErrInvalid
	}
	err = s.read(ctx, token, func(db *gorm.DB, actor string, _ time.Time) error {
		var card entity.CareCard
		e := db.WithContext(ctx).Where("id=? AND recipient_id=?", id, actor).Take(&card).Error
		if e != nil {
			if e == gorm.ErrRecordNotFound {
				return command.ErrNotFound
			}
			return e
		}
		out = CareCard{ID: card.ID, Resources: card.Resources}
		return nil
	})
	return
}
