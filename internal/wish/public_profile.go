package wish

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// ProfileSummary 仅公开账号标识与昵称；账号没有独立已审标签，不从私有身份或某条内容推断。
type ProfileSummary struct {
	ID       string `json:"id"`
	Nickname string `json:"nickname"`
}

// ProfileItem 区分当前可见的心愿与故事，正文继续使用本域公开投影。
type ProfileItem struct {
	Kind    string        `json:"kind"`
	Content PublicContent `json:"content"`
}

// ProfileContents 是作者公开主页的混合内容流，不含本人草稿或身份材料。
type ProfileContents struct {
	Profile    ProfileSummary `json:"profile"`
	Items      []ProfileItem  `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

// PublicProfile 在同一权威快照复核会话、目标与每条内容，作者本人也不获得额外读取范围。
// 游标绑定读取账号及目标；标签只来自每条内容的已审公开投影并服从当前隐藏设置。
func (s *Service) PublicProfile(ctx context.Context, token, rid, target, cursor string, limit int) (out ProfileContents, err error) {
	defer func() {
		if err != nil {
			out = ProfileContents{}
		}
	}()
	defer s.storyReadDone("profile.public", rid, time.Now(), &err)
	if !validWishID(target) || limit < 1 || limit > 50 {
		return out, command.ErrInvalid
	}
	err = s.readTransaction(ctx, func(db *gorm.DB, now time.Time) error {
		actor := ""
		if token != "" {
			var e error
			actor, e = currentReader(ctx, db, token, now)
			if e != nil {
				return e
			}
		}
		scope := queryScope("profile/"+target, actor, Query{})
		after, e := s.decodeCursor(cursor, scope)
		if e != nil {
			return e
		}
		if after.ID != "" && after.PublishedAt.IsZero() {
			return command.ErrInvalid
		}
		out.Profile, e = readProfile(ctx, db, target)
		if e != nil {
			return e
		}
		out.Items = []ProfileItem{}
		positions := []pageCursor{}
		for len(out.Items) <= limit {
			rows, e := profileCandidates(ctx, db, target, after, now, s.config.MaxStoryBytes > 0)
			if e != nil {
				return e
			}
			if len(rows) == 0 {
				break
			}
			for _, row := range rows {
				after = pageCursor{Scope: scope, ID: row.ID, PublishedAt: row.Position}
				item, e := s.shareTarget(ctx, db, row.Kind, row.ID, rid, now)
				if errors.Is(e, command.ErrNotFound) {
					continue
				}
				if e != nil {
					return e
				}
				out.Items = append(out.Items, ProfileItem{Kind: row.Kind, Content: item})
				positions = append(positions, after)
				if len(out.Items) > limit {
					break
				}
			}
			if len(rows) < 100 {
				break
			}
		}
		if len(out.Items) > limit {
			next := s.encodeCursor(positions[limit-1])
			out.NextCursor = &next
			out.Items = out.Items[:limit]
		}
		return nil
	})
	return
}
