package app

import (
	"context"
	"slices"

	"github.com/gin-gonic/gin"

	"github.com/calm195/liyuanci-backend-go/internal/execution/command"
	"github.com/calm195/liyuanci-backend-go/internal/httpapi"
	"github.com/calm195/liyuanci-backend-go/internal/modules/moderation"
	"github.com/calm195/liyuanci-backend-go/internal/modules/wish"
)

func (s *serviceSet) registerWishImages(router *gin.Engine) error {
	if s.wishes == nil {
		return nil
	}
	if e := httpapi.RegisterWishImages(router, s.wishes); e != nil {
		return e
	}
	if s.contentReviews != nil {
		content, e := moderation.NewAdminWishContent(s.contentReviews, s.wishes)
		if e != nil {
			return e
		}
		httpapi.RegisterAdminWishImages(router, func(ctx context.Context, token, rid, caseID, wishID, imageID, variant string) ([]byte, error) {
			if (caseID == "") == (wishID == "") {
				return nil, command.ErrInvalid
			}
			authorize := func() error {
				if wishID != "" {
					detail, e := content.Detail(ctx, token, rid, wishID, 0)
					if e != nil {
						return e
					}
					if detail.Original == nil || !slices.ContainsFunc(detail.Original.Images, func(i wish.Image) bool { return i.ID == imageID }) {
						return command.ErrNotFound
					}
					return nil
				}
				detail, e := s.contentReviews.Case(ctx, token, rid, caseID)
				if e != nil {
					return e
				}
				if !slices.ContainsFunc(detail.Images, func(i wish.Image) bool { return i.ID == imageID }) {
					return command.ErrNotFound
				}
				return nil
			}
			if e := authorize(); e != nil {
				return nil, e
			}
			data, e := s.wishes.ReadReviewedImageVariant(ctx, imageID, variant)
			if e != nil {
				return nil, e
			}
			if e = authorize(); e != nil {
				clear(data)
				return nil, e
			}
			return data, nil
		})
	}
	return nil
}
