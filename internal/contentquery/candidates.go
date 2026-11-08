// Package contentquery 提供公开列表共用的候选排序、分页和缓存边界。
package contentquery

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

const (
	// WishListKey 定位默认心愿首页候选。
	WishListKey = "wish:list"
	// WishWeeklyKey 定位周热度心愿候选。
	WishWeeklyKey = "wish:weekly"
	// StoryListKey 定位默认还愿故事首页候选。
	StoryListKey = "story:list"
	// WhisperListKey 定位默认悄悄话首页候选。
	WhisperListKey = "whisper:list"
	// WhisperWeeklyKey 定位周热度悄悄话候选。
	WhisperWeeklyKey = "whisper:weekly"

	SortTime = "time"
	SortHot  = "hot"
	// WeeklyLimit 是每个热点类别的返回上限。
	WeeklyLimit = 10
	batchSize   = 100
)

// Candidate 只缓存公开列表候选定位字段，当前正文及可见性必须从数据库复核。
type Candidate struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Score int64     `json:"score,omitempty"`
}

// CandidateCache 缓存公开候选，不保存会话、用户维度或正文。
type CandidateCache interface {
	Get(context.Context, string) ([]Candidate, bool)
	Set(context.Context, string, []Candidate)
}

type refreshKey struct{}

// Refresh 让预热跳过缓存读取并以最新数据库候选替换缓存值。
func Refresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, refreshKey{}, true)
}

// Normalize 校验排序和关键词，保留空排序对旧游标的兼容性。
func Normalize(sort, keyword string) (string, error) {
	if sort != "" && sort != SortTime && sort != SortHot {
		return "", command.ErrInvalid
	}
	if !utf8.ValidString(keyword) || strings.IndexFunc(keyword, unicode.IsControl) >= 0 {
		return "", command.ErrInvalid
	}
	keyword = strings.TrimSpace(keyword)
	if utf8.RuneCountInString(keyword) > 80 {
		return "", command.ErrInvalid
	}
	return keyword, nil
}

// Match 在固定列上匹配字面关键词，用户输入不得作为 SQL 或通配符解释。
func Match(db *gorm.DB, title, body, keyword string) *gorm.DB {
	if keyword == "" {
		return db
	}
	pattern := "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(keyword) + "%"
	return db.Where("("+title+" ILIKE ? ESCAPE '!' OR "+body+" ILIKE ? ESCAPE '!')", pattern, pattern)
}

// Ordered 对候选分数、时间和随机标识应用稳定的键集分页。
func Ordered(db, candidates *gorm.DB, after Candidate, hot bool) *gorm.DB {
	q := db.Table("(?) AS candidates", candidates)
	if hot {
		q = q.Order("score DESC")
		if after.ID != "" {
			q = q.Where("(score,at,id)<(?,?,?)", after.Score, after.At, after.ID)
		}
	} else if after.ID != "" {
		q = q.Where("(at,id)<(?,?)", after.At, after.ID)
	}
	return q.Order("at DESC,id DESC")
}

// Collect 按排序读取候选，再复核实际公开资格。命中已失效候选时立即回源并刷新缓存。
func Collect[T any](ctx context.Context, cache CandidateCache, key string, after Candidate, limit int,
	fetch func(Candidate, int) ([]Candidate, error), project func(string) (T, error),
) (items []T, next Candidate, err error) {
	items = make([]T, 0, limit)
	readCache := key != "" && cache != nil && after.ID == "" && ctx.Value(refreshKey{}) == nil
	for {
		var batch []Candidate
		var cached bool
		if readCache {
			batch, cached = cache.Get(ctx, key)
			readCache = false
		}
		if !cached {
			batch, err = fetch(after, batchSize)
			if err != nil {
				return nil, Candidate{}, err
			}
			if key != "" && cache != nil && after.ID == "" {
				cache.Set(ctx, key, batch)
			}
		}
		stale := false
		for _, candidate := range batch {
			item, e := project(candidate.ID)
			if errors.Is(e, command.ErrNotFound) || errors.Is(e, gorm.ErrRecordNotFound) {
				if cached {
					stale = true
					break
				}
				continue
			}
			if e != nil {
				return nil, Candidate{}, e
			}
			if len(items) == limit {
				return items, next, nil
			}
			next = candidate
			items = append(items, item)
		}
		if stale {
			// 从第一条重扫，避免旧候选阻挡新发布内容进入榜单。
			items = items[:0]
			next = Candidate{}
			continue
		}
		if len(batch) < batchSize {
			return items, Candidate{}, nil
		}
		after = batch[len(batch)-1]
	}
}
