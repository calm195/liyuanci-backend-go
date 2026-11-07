package resourceplaza

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// scope 是持久幂等回执的命令标识，必须保持与既有HTTP操作一致，不能随文件重组改名。
func scope(op, id string) (string, error) {
	switch op {
	case "create":
		return "POST /admin/resources", nil
	case "update":
		return "PUT /admin/resources/" + id + "/content", nil
	case "publish", "offline":
		return "POST /admin/resources/" + id + "/" + op, nil
	case "category_create":
		return "POST /admin/resource-categories", nil
	case "category_update":
		return "PUT /admin/resource-categories/" + id, nil
	case "feedback":
		return "PUT /resources/" + id + "/feedback", nil
	}
	return "", command.ErrInvalid
}

func sameDownload(a download, in Input) bool {
	return a.URL == in.DownloadURL &&
		((a.Code == nil && in.ExtractionCode == nil) || (a.Code != nil && in.ExtractionCode != nil && *a.Code == *in.ExtractionCode))
}

// Write 在同一事务内授权、锁定、重放或更新，最后保存最小回执；不保存请求原文。
func (s *Service) Write(ctx context.Context, token, rid, key, op, id string, in Input) (out any, err error) {
	defer s.done(op, rid, time.Now(), &err)
	defer func() {
		if err != nil {
			out = nil
		}
	}()
	var e error
	in, e = normalizeInput(op, in)
	if e != nil {
		return nil, e
	}
	path, e := scope(op, id)
	if e != nil {
		return nil, e
	}
	if key == "" || len(key) > 128 {
		return nil, command.ErrInvalid
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return nil, command.ErrInvalid
		}
	}
	// 摘要基于归一化输入，回执只保存摘要和结果引用，不持久化下载信息原文。
	b, e := json.Marshal(in)
	if e != nil {
		return nil, command.ErrInvalid
	}
	digest := sha256.Sum256(b)
	clear(b)
	kh := sha256.Sum256([]byte(key))
	admin := op != "feedback"
	for attempt := 0; attempt < 3; attempt++ {
		err = s.transaction(ctx, token, admin, true, func(db *gorm.DB, a actor) error {
			r, c, e := s.lock(db, op, id, in, a)
			if e != nil {
				return e
			}
			if op == "feedback" && r.ContentVersion != in.ExpectedContentVersion {
				return command.ErrVersionConflict
			}
			kind := "student"
			if admin {
				kind = "admin"
			}
			// 目标与主体已经锁定，再读取幂等回执；重放同样要通过当前授权与版本条件。
			var receipts []entity.CommandReceipt
			if e = db.Where("actor_kind=? AND actor_id=? AND command_kind=? AND key_hash=?", kind, a.ID, path, kh[:]).
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Find(&receipts).
				Error; e != nil {
				return e
			}
			ref := ""
			if len(receipts) == 1 {
				if !slices.Equal(receipts[0].RequestHash, digest[:]) {
					return command.ErrIdempotencyConflict
				}
				ref = receipts[0].ResultRef
			} else {
				// 仅首次命令执行领域写入；重放路径直接使用已保存的结果引用。
				switch op {
				case "category_create", "category_update":
					ref, e = s.writeCategory(ctx, db, a, op, id, in, c)
				case "create":
					ref, e = s.createMaterial(ctx, db, a, op, in)
				case "update", "publish", "offline":
					ref, e = s.changeMaterial(ctx, db, a, op, in, r)
				case "feedback":
					ref, e = s.writeFeedback(ctx, db, a, in, r)
				}
				if e != nil {
					return e
				}
				receipt := entity.CommandReceipt{
					ActorKind:      kind,
					ActorID:        a.ID,
					CommandKind:    path,
					KeyHash:        kh[:],
					RequestHash:    digest[:],
					ResultRef:      ref,
					LifecycleBound: true,
					ExpiresAt:      a.Now.Add(24 * time.Hour),
				}
				if e = db.Create(&receipt).Error; e != nil {
					return e
				}
			}
			// 首次执行与幂等重放共用当前结果投影，不返回已经失效的旧下载或反馈版本。
			if op == "category_create" || op == "category_update" {
				c, e = category(db, ref)
				if e != nil {
					return e
				}
				out = catProjection(c, true)
				return nil
			}
			if op == "feedback" {
				var f entity.ResourceFeedback
				if e = db.Where("id=? AND resource_id=? AND account_id=? AND content_version=?", ref, id, a.ID, r.ContentVersion).Take(&f).Error; e != nil {
					if errors.Is(e, gorm.ErrRecordNotFound) {
						return command.ErrVersionConflict
					}
					return e
				}
				out = feedbackProjection(f)
				return nil
			}
			r, e = material(db, ref, a)
			if e != nil {
				return e
			}
			stats, e := summaries(db, []entity.ResourceMaterial{r})
			if e != nil {
				return e
			}
			out, e = s.project(db, a, r, true, stats, nil)
			return e
		})
		if !errors.Is(err, command.ErrRetry) {
			break
		}
	}
	return
}

// writeCategory 复核分类名称、停用约束和版本，在当前事务内保存分类与审计。
func (s *Service) writeCategory(ctx context.Context, db *gorm.DB, a actor, op, id string, in Input, c entity.ResourceCategory) (string, error) {
	db = db.WithContext(ctx)
	var e error
	if op == "category_update" && c.Version != in.ExpectedVersion {
		return "", command.ErrVersionConflict
	}
	var n int64
	q := db.Model(&entity.ResourceCategory{}).Where("name_key=?", Fold(in.Name))
	if id != "" {
		q = q.Where("id<>?", id)
	}
	if e = q.Count(&n).Error; e != nil {
		return "", e
	}
	if n > 0 {
		return "", ErrCategoryName
	}
	if op == "category_update" && !in.Enabled {
		if e = db.Model(&entity.ResourceMaterial{}).Where("category_id=?", id).Count(&n).Error; e != nil {
			return "", e
		}
		if n > 0 {
			return "", ErrCategoryInUse
		}
	}
	if op == "category_update" && c.Name == in.Name && c.SortOrder == in.SortOrder && c.Enabled == in.Enabled {
		return c.ID, nil
	}
	if op == "category_create" {
		c = entity.ResourceCategory{
			ID:        uuid.NewString(),
			Name:      in.Name,
			NameKey:   Fold(in.Name),
			SortOrder: in.SortOrder,
			Enabled:   true,
			Version:   1,
			CreatedAt: a.Now,
			UpdatedAt: a.Now,
		}
		e = db.Create(&c).Error
	} else {
		if c.Version == MaxInteger {
			return "", command.ErrUnavailable
		}
		c.Version++
		c.Name = in.Name
		c.NameKey = Fold(in.Name)
		c.SortOrder = in.SortOrder
		c.Enabled = in.Enabled
		c.UpdatedAt = a.Now
		e = db.Save(&c).Error
	}
	if e != nil {
		return "", e
	}
	if e = s.audit(db, a, op, c.ID, c.Version, ""); e != nil {
		return "", e
	}
	return c.ID, nil
}

// createMaterial 验证可用分类后创建草稿，将下载信息加密保存并写入审计。
func (s *Service) createMaterial(ctx context.Context, db *gorm.DB, a actor, op string, in Input) (string, error) {
	db = db.WithContext(ctx)
	c, e := category(db, in.CategoryID)
	if errors.Is(e, command.ErrNotFound) || e == nil && !c.Enabled {
		return "", command.ErrConflict
	}
	if e != nil {
		return "", e
	}
	r := entity.ResourceMaterial{
		ID:             uuid.NewString(),
		CategoryID:     c.ID,
		Title:          in.Title,
		Description:    in.Description,
		Version:        1,
		ContentVersion: 1,
		Status:         "draft",
		CreatedAt:      a.Now,
		UpdatedAt:      a.Now,
	}
	r.FileID, e = bindFile(db, a, in.FileID, nil)
	if e != nil {
		return "", e
	}
	r.DownloadCipher, e = s.encrypt(ctx, "resource_download", r.ID, 1, download{in.DownloadURL, in.ExtractionCode})
	if e != nil {
		return "", e
	}
	if e = db.Create(&r).Error; e != nil {
		return "", e
	}
	if e = s.audit(db, a, op, r.ID, r.Version, ""); e != nil {
		return "", e
	}
	return r.ID, nil
}

// changeMaterial 处理资料改稿、上架与下架；改稿推进内容版本，保留历史反馈并清理旧版回执。
func (s *Service) changeMaterial(ctx context.Context, db *gorm.DB, a actor, op string, in Input, r entity.ResourceMaterial) (string, error) {
	db = db.WithContext(ctx)
	var e error
	if r.Version != in.ExpectedVersion {
		return "", command.ErrVersionConflict
	}
	if (op == "offline" && r.Status != "published") || (op != "offline" && r.Status == "published") ||
		(op == "publish" && r.ContentBlocked) {
		return "", command.ErrConflict
	}
	if op == "update" {
		nc, e := category(db, in.CategoryID)
		if errors.Is(e, command.ErrNotFound) || e == nil && !nc.Enabled {
			return "", command.ErrConflict
		}
		if e != nil {
			return "", e
		}
		d, e := s.download(ctx, r)
		if e != nil {
			return "", e
		}
		if r.Title == in.Title && r.Description == in.Description && r.CategoryID == in.CategoryID && sameDownload(d, in) && ((r.FileID == nil && in.FileID == "") || (r.FileID != nil && *r.FileID == in.FileID)) {
			return r.ID, nil
		}
		if r.Version == MaxInteger || r.ContentVersion == MaxInteger {
			return "", command.ErrUnavailable
		}
		previousFile := r.FileID
		r.FileID, e = bindFile(db, a, in.FileID, r.FileID)
		if e != nil {
			return "", e
		}
		if previousFile != nil && (r.FileID == nil || *previousFile != *r.FileID) {
			if e = db.Model(&entity.ResourceFile{}).Where("id=?", *previousFile).Update("removable_after", a.Now.Add(10*time.Minute)).Error; e != nil {
				return "", e
			}
		}
		r.Title = in.Title
		r.Description = in.Description
		r.CategoryID = in.CategoryID
		r.ContentVersion++
		r.DownloadCipher, e = s.encrypt(ctx, "resource_download", r.ID, r.ContentVersion, download{in.DownloadURL, in.ExtractionCode})
		if e != nil {
			return "", e
		}
		if e = clearFeedback(db, r.ID); e != nil {
			return "", e
		}
	} else if op == "publish" {
		r.Status = "published"
		r.PublishedAt = &a.Now
	} else {
		r.Status = "offline"
	}
	if r.Version == MaxInteger {
		return "", command.ErrUnavailable
	}
	r.Version++
	r.UpdatedAt = a.Now
	if e = db.Save(&r).Error; e != nil {
		return "", e
	}
	if e = s.audit(db, a, op, r.ID, r.Version, in.Reason); e != nil {
		return "", e
	}
	return r.ID, nil
}

// writeFeedback 在已锁定资料的当前内容版本下写入本人反馈，重复类型不推进版本。
func (s *Service) writeFeedback(ctx context.Context, db *gorm.DB, a actor, in Input, r entity.ResourceMaterial) (string, error) {
	db = db.WithContext(ctx)
	var e error
	var fs []entity.ResourceFeedback
	if e = db.Where("resource_id=? AND account_id=? AND content_version=?", r.ID, a.ID, r.ContentVersion).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Find(&fs).
		Error; e != nil {
		return "", e
	}
	var f entity.ResourceFeedback
	if len(fs) == 0 {
		if in.ExpectedFeedbackVersion != 0 {
			return "", command.ErrVersionConflict
		}
		f = entity.ResourceFeedback{
			ID:             uuid.NewString(),
			ResourceID:     r.ID,
			AccountID:      a.ID,
			ContentVersion: r.ContentVersion,
			Kind:           in.Kind,
			Version:        1,
			UpdatedAt:      a.Now,
		}
		e = db.Create(&f).Error
	} else {
		f = fs[0]
		if f.Version != in.ExpectedFeedbackVersion {
			return "", command.ErrVersionConflict
		}
		if f.Kind != in.Kind {
			if f.Version == MaxInteger {
				return "", command.ErrUnavailable
			}
			f.Version++
			f.Kind = in.Kind
			f.UpdatedAt = a.Now
			e = db.Save(&f).Error
		}
	}
	if e != nil {
		return "", e
	}
	return f.ID, nil
}
