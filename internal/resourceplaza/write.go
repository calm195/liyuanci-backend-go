package resourceplaza

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/calm195/liyuanci-backend-go/internal/command"
	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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
func (s *Service) lock(db *gorm.DB, op, id string, in Input, a actor) (r entity.ResourceMaterial, c entity.ResourceCategory, err error) {
	// 分类集合技术锁仅用于分类创建/改名，先于类别行锁，保护唯一名称错误的稳定映射。
	if op == "category_create" || op == "category_update" {
		if err = db.Exec("SELECT pg_advisory_xact_lock(624109180)").Error; err != nil {
			return
		}
	}
	refs := []command.Resource{}
	if op == "category_update" {
		refs = append(refs, command.Resource{Kind: command.ResourceCategory, ID: id})
	}
	if op == "create" || op == "update" {
		refs = append(refs, command.Resource{Kind: command.ResourceCategory, ID: in.CategoryID})
	}
	if slices.Contains([]string{"update", "publish", "offline", "feedback"}, op) {
		r, err = material(db, id, a)
		if err != nil {
			return
		}
		refs = append(refs, command.Resource{Kind: command.ResourceCategory, ID: r.CategoryID}, command.Resource{Kind: command.ResourceMaterial, ID: r.ID})
	}
	if len(refs) > 0 {
		_, err = command.LockPlan(db.Statement.Context, db, command.Discovery{Resources: refs}, s.cfg.Command.LockTimeout)
		if err != nil {
			if errors.Is(err, command.ErrNotFound) && (op == "create" || op == "update") {
				err = command.ErrConflict
			}
			return
		}
	}
	if op == "category_update" {
		c, err = category(db, id)
		return
	}
	if r.ID != "" {
		old := r.CategoryID
		r, err = material(db, id, a)
		if err != nil {
			return
		}
		if old != r.CategoryID {
			err = command.ErrRetry
			return
		}
		c, err = category(db, r.CategoryID)
		if err == nil && !c.Enabled {
			err = command.ErrUnavailable
		}
	}
	return
}
func (s *Service) audit(db *gorm.DB, a actor, op, id string, version int64, reason string) error {
	r := entity.ResourceAudit{ID: uuid.NewString(), ActorID: a.ID, TargetID: id, Operation: op, Version: version, CreatedAt: a.Now}
	if reason != "" {
		b, e := s.encrypt(db.Statement.Context, "resource_offline_reason", r.ID, version, reason)
		if e != nil {
			return e
		}
		r.ReasonCipher = b
	}
	return db.Create(&r).Error
}
func sameDownload(a download, in Input) bool {
	return a.URL == in.DownloadURL && ((a.Code == nil && in.ExtractionCode == nil) || (a.Code != nil && in.ExtractionCode != nil && *a.Code == *in.ExtractionCode))
}
func clearFeedback(db *gorm.DB, id string) error {
	if e := db.Exec("DELETE FROM public.command_receipts WHERE actor_kind='student' AND result_ref IN (SELECT id FROM public.resource_feedback WHERE resource_id=?)", id).Error; e != nil {
		return e
	}
	return db.Where("resource_id=?", id).Delete(&entity.ResourceFeedback{}).Error
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
			var receipts []entity.CommandReceipt
			if e = db.Where("actor_kind=? AND actor_id=? AND command_kind=? AND key_hash=?", kind, a.ID, path, kh[:]).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&receipts).Error; e != nil {
				return e
			}
			ref := ""
			if len(receipts) == 1 {
				if !slices.Equal(receipts[0].RequestHash, digest[:]) {
					return command.ErrIdempotencyConflict
				}
				ref = receipts[0].ResultRef
			} else {
				switch op {
				case "category_create", "category_update":
					if op == "category_update" && c.Version != in.ExpectedVersion {
						return command.ErrVersionConflict
					}
					var n int64
					q := db.Model(&entity.ResourceCategory{}).Where("name_key=?", Fold(in.Name))
					if id != "" {
						q = q.Where("id<>?", id)
					}
					if e = q.Count(&n).Error; e != nil {
						return e
					}
					if n > 0 {
						return ErrCategoryName
					}
					if op == "category_update" && !in.Enabled {
						if e = db.Model(&entity.ResourceMaterial{}).Where("category_id=?", id).Count(&n).Error; e != nil {
							return e
						}
						if n > 0 {
							return ErrCategoryInUse
						}
					}
					if op == "category_update" && c.Name == in.Name && c.SortOrder == in.SortOrder && c.Enabled == in.Enabled {
						ref = c.ID
						break
					}
					if op == "category_create" {
						c = entity.ResourceCategory{ID: uuid.NewString(), Name: in.Name, NameKey: Fold(in.Name), SortOrder: in.SortOrder, Enabled: true, Version: 1, CreatedAt: a.Now, UpdatedAt: a.Now}
						e = db.Create(&c).Error
					} else {
						if c.Version == MaxInteger {
							return command.ErrUnavailable
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
						return e
					}
					if e = s.audit(db, a, op, c.ID, c.Version, ""); e != nil {
						return e
					}
					ref = c.ID
				case "create":
					c, e = category(db, in.CategoryID)
					if errors.Is(e, command.ErrNotFound) || e == nil && !c.Enabled {
						return command.ErrConflict
					}
					if e != nil {
						return e
					}
					r = entity.ResourceMaterial{ID: uuid.NewString(), CategoryID: c.ID, Title: in.Title, Description: in.Description, Version: 1, ContentVersion: 1, Status: "draft", CreatedAt: a.Now, UpdatedAt: a.Now}
					r.DownloadCipher, e = s.encrypt(ctx, "resource_download", r.ID, 1, download{in.DownloadURL, in.ExtractionCode})
					if e != nil {
						return e
					}
					if e = db.Create(&r).Error; e != nil {
						return e
					}
					if e = s.audit(db, a, op, r.ID, r.Version, ""); e != nil {
						return e
					}
					ref = r.ID
				case "update", "publish", "offline":
					if r.Version != in.ExpectedVersion {
						return command.ErrVersionConflict
					}
					if (op == "offline" && r.Status != "published") || (op != "offline" && r.Status == "published") || (op == "publish" && r.ContentBlocked) {
						return command.ErrConflict
					}
					if op == "update" {
						nc, e := category(db, in.CategoryID)
						if errors.Is(e, command.ErrNotFound) || e == nil && !nc.Enabled {
							return command.ErrConflict
						}
						if e != nil {
							return e
						}
						d, e := s.download(ctx, r)
						if e != nil {
							return e
						}
						if r.Title == in.Title && r.Description == in.Description && r.CategoryID == in.CategoryID && sameDownload(d, in) {
							ref = r.ID
							break
						}
						if r.Version == MaxInteger || r.ContentVersion == MaxInteger {
							return command.ErrUnavailable
						}
						r.Title = in.Title
						r.Description = in.Description
						r.CategoryID = in.CategoryID
						r.ContentVersion++
						r.DownloadCipher, e = s.encrypt(ctx, "resource_download", r.ID, r.ContentVersion, download{in.DownloadURL, in.ExtractionCode})
						if e != nil {
							return e
						}
						if e = clearFeedback(db, r.ID); e != nil {
							return e
						}
					} else if op == "publish" {
						r.Status = "published"
						r.PublishedAt = &a.Now
					} else {
						r.Status = "offline"
					}
					if r.Version == MaxInteger {
						return command.ErrUnavailable
					}
					r.Version++
					r.UpdatedAt = a.Now
					if e = db.Save(&r).Error; e != nil {
						return e
					}
					if e = s.audit(db, a, op, r.ID, r.Version, in.Reason); e != nil {
						return e
					}
					ref = r.ID
				case "feedback":
					var fs []entity.ResourceFeedback
					if e = db.Where("resource_id=? AND account_id=? AND content_version=?", r.ID, a.ID, r.ContentVersion).Clauses(clause.Locking{Strength: "UPDATE"}).Find(&fs).Error; e != nil {
						return e
					}
					var f entity.ResourceFeedback
					if len(fs) == 0 {
						if in.ExpectedFeedbackVersion != 0 {
							return command.ErrVersionConflict
						}
						f = entity.ResourceFeedback{ID: uuid.NewString(), ResourceID: r.ID, AccountID: a.ID, ContentVersion: r.ContentVersion, Kind: in.Kind, Version: 1, UpdatedAt: a.Now}
						e = db.Create(&f).Error
					} else {
						f = fs[0]
						if f.Version != in.ExpectedFeedbackVersion {
							return command.ErrVersionConflict
						}
						if f.Kind != in.Kind {
							if f.Version == MaxInteger {
								return command.ErrUnavailable
							}
							f.Version++
							f.Kind = in.Kind
							f.UpdatedAt = a.Now
							e = db.Save(&f).Error
						}
					}
					if e != nil {
						return e
					}
					ref = f.ID
				}
				receipt := entity.CommandReceipt{ActorKind: kind, ActorID: a.ID, CommandKind: path, KeyHash: kh[:], RequestHash: digest[:], ResultRef: ref, LifecycleBound: true, ExpiresAt: a.Now.Add(24 * time.Hour)}
				if e = db.Create(&receipt).Error; e != nil {
					return e
				}
			}
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

// SetBarrier 是受控治理装配调用的内部入口，不注册任何学生举报或普通后台HTTP路由。
// 调用方必须持有治理服务能力；与编辑使用相同分类、资料锁，屏障改变使资料离线且增加资源版本。
func (s *Service) SetBarrier(ctx context.Context, token, rid, key, id string, privacy, content bool) (err error) {
	defer s.done("barrier", rid, time.Now(), &err)
	return s.transaction(ctx, token, true, true, func(db *gorm.DB, a actor) error {
		// 受控操作只允许超级管理员，普通review_operator没有此能力。
		var n int64
		if e := db.Model(&entity.AdminRoleBinding{}).Where("admin_id=? AND role='super_admin'", a.ID).Count(&n).Error; e != nil {
			return e
		}
		if n != 1 {
			return command.ErrForbidden
		}
		var r entity.ResourceMaterial
		if e := db.Where("id=?", id).Take(&r).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return command.ErrNotFound
			}
			return e
		}
		oldCategory := r.CategoryID
		if _, e := command.LockPlan(ctx, db, command.Discovery{Resources: []command.Resource{{Kind: command.ResourceCategory, ID: r.CategoryID}, {Kind: command.ResourceMaterial, ID: id}}}, s.cfg.Command.LockTimeout); e != nil {
			return e
		}
		if e := db.Where("id=?", id).Take(&r).Error; e != nil {
			return e
		}
		if oldCategory != r.CategoryID {
			return command.ErrRetry
		}
		if r.PrivacyBlocked == privacy && r.ContentBlocked == content {
			return nil
		}
		if r.Version == MaxInteger {
			return command.ErrUnavailable
		}
		r.Version++
		r.PrivacyBlocked = privacy
		r.ContentBlocked = content
		if r.Status == "published" {
			r.Status = "offline"
		}
		r.UpdatedAt = a.Now
		if e := db.Save(&r).Error; e != nil {
			return e
		}
		return s.audit(db, a, "barrier", id, r.Version, "")
	})
}
