package dictionary

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/calm195/liyuanci-backend-go/internal/entity"
	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
)

// ReportType 是运营维护的举报类型，停用后仍在后台保留名称。
type ReportType struct {
	Code      string `json:"code"`
	Label     string `json:"label"`
	Published bool   `json:"published"`
}

// ReportTypesSnapshot 与展示字典共享发布版本和审计顺序。
type ReportTypesSnapshot struct {
	Version int64        `json:"version"`
	Items   []ReportType `json:"items"`
}

// SaveReportTypeInput 用明确发布状态创建或更新举报类型，代码保持稳定。
type SaveReportTypeInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Label           string `json:"label"`
	Published       *bool  `json:"published"`
}

func reportTypes(ctx context.Context, db *gorm.DB) (out ReportTypesSnapshot, err error) {
	h, err := head(ctx, db, "SHARE")
	if err != nil {
		return out, err
	}
	var items []entity.ReportType
	if err = db.WithContext(ctx).Order("code").Find(&items).Error; err != nil {
		return out, err
	}
	out.Version = h.Version
	out.Items = make([]ReportType, 0, len(items))
	for _, item := range items {
		out.Items = append(out.Items, ReportType{item.Code, item.Label, item.Published})
	}
	return out, nil
}

// ReadReportTypes 只向有效后台会话返回包含停用项的运营字典。
func (s *Service) ReadReportTypes(ctx context.Context, token, rid string) (out ReportTypesSnapshot, err error) {
	err = s.readAdmin(ctx, token, rid, func(db *gorm.DB) error {
		var e error
		out, e = reportTypes(db.Statement.Context, db)
		return e
	})
	if err != nil {
		out = ReportTypesSnapshot{}
	}
	return out, err
}

// SaveReportType 直接维护现有业务表，不复制为展示字典，提交即对后续举报生效。
func (s *Service) SaveReportType(ctx context.Context, token, key, rid, code string, input SaveReportTypeInput) (out ReportTypesSnapshot, err error) {
	if !validReportTypeInput(code, input) {
		return out, command.ErrInvalid
	}
	definition := command.Definition{
		Kind:         "dictionary.report_type.save",
		Method:       "PUT",
		ResourcePath: "/api/v1/admin/report-types/" + code,
		ActorKind:    "admin",
		AllowedRoles: []string{"super_admin"},
		Discover: func(context.Context, *gorm.DB, command.Subject) (command.Discovery, error) {
			return command.Discovery{}, nil
		},
		Authorize: func(ctx context.Context, tx *command.Tx) error {
			_, e := head(ctx, tx.DB(), "UPDATE")
			return e
		},
		Apply: func(ctx context.Context, tx *command.Tx) (string, error) {
			h, e := head(ctx, tx.DB(), "")
			if e != nil {
				return "", e
			}
			if h.Version != input.ExpectedVersion {
				return "", command.ErrVersionConflict
			}
			var item entity.ReportType
			e = tx.DB().WithContext(ctx).Where("code=?", code).Take(&item).Error
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return "", e
			}
			before, e := json.Marshal(ReportType{item.Code, item.Label, item.Published})
			if e != nil {
				return "", e
			}
			item = entity.ReportType{Code: code, Label: input.Label, Published: *input.Published}
			if e = tx.DB().WithContext(ctx).Save(&item).Error; e != nil {
				return "", e
			}
			if e = tx.DB().WithContext(ctx).Model(&entity.DisplayDictionary{}).Where("id=1").Update("version", h.Version+1).Error; e != nil {
				return "", e
			}
			after, e := json.Marshal(ReportType{item.Code, item.Label, item.Published})
			if e != nil {
				return "", e
			}
			e = tx.DB().WithContext(ctx).Create(&entity.DisplayDictionaryAction{Version: h.Version + 1, ActorID: tx.Actor().ID, Before: before, After: after, At: tx.Now()}).Error
			return "00000000-0000-4000-8000-000000000001", e
		},
		Resolve: func(ctx context.Context, tx *command.Tx, _ string) error {
			var e error
			out, e = reportTypes(ctx, tx.DB())
			return e
		},
	}
	_, err = s.engine.Execute(ctx, definition, command.Input{Token: token, IdempotencyKey: key, RequestID: rid, Request: input})
	if err != nil {
		out = ReportTypesSnapshot{}
	}
	return out, err
}

// validReportTypeInput 保留历史大写与连字符代码，长度遵循既有举报契约。
func validReportTypeInput(code string, input SaveReportTypeInput) bool {
	if len(code) == 0 || len(code) > 32 || input.Published == nil {
		return false
	}
	for _, r := range code {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return validDisplayFields(input.ExpectedVersion, input.Label)
}
