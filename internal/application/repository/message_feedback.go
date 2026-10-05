package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type messageFeedbackRepository struct {
	db *gorm.DB
}

// NewMessageFeedbackRepository returns the message-feedback repository.
func NewMessageFeedbackRepository(db *gorm.DB) interfaces.MessageFeedbackRepository {
	return &messageFeedbackRepository{db: db}
}

// Upsert inserts a feedback row or replaces the caller's earlier verdict on
// the same message (unique message_id + created_by).
func (r *messageFeedbackRepository) Upsert(ctx context.Context, fb *types.MessageFeedback) error {
	var existing types.MessageFeedback
	err := r.db.WithContext(ctx).
		Where("message_id = ? AND created_by = ?", fb.MessageID, fb.CreatedBy).
		First(&existing).Error
	if err == nil {
		fb.ID = existing.ID
		fb.CreatedAt = existing.CreatedAt
		return r.db.WithContext(ctx).Model(&types.MessageFeedback{}).
			Where("id = ?", existing.ID).
			Updates(map[string]interface{}{
				"rating":      fb.Rating,
				"reason_code": fb.ReasonCode,
				"comment":     fb.Comment,
				"snapshot":    fb.Snapshot,
				"request_id":  fb.RequestID,
				"updated_at":  time.Now(),
			}).Error
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	return r.db.WithContext(ctx).Create(fb).Error
}

// List filters by tenant, rating and reason with newest first, paged.
func (r *messageFeedbackRepository) List(ctx context.Context, filter types.MessageFeedbackFilter) ([]*types.MessageFeedback, int64, error) {
	q := r.db.WithContext(ctx).Model(&types.MessageFeedback{}).
		Where("tenant_id = ?", filter.TenantID)
	if filter.Rating != "" {
		q = q.Where("rating = ?", filter.Rating)
	}
	if filter.ReasonCode != "" {
		q = q.Where("reason_code = ?", filter.ReasonCode)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := filter.Page, filter.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	rows := []*types.MessageFeedback{}
	err := q.Order("created_at DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error
	return rows, total, err
}
