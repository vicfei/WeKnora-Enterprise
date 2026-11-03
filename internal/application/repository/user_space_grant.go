package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrGrantNotFound      = errors.New("user space grant not found")
	ErrGrantConflictState = errors.New("user space grant state conflict")
)

type userSpaceGrantRepository struct {
	db *gorm.DB
}

// NewUserSpaceGrantRepository returns the gorm-backed grant repository.
func NewUserSpaceGrantRepository(db *gorm.DB) interfaces.UserSpaceGrantRepository {
	return &userSpaceGrantRepository{db: db}
}

// Upsert inserts the (uum, tenant) row or updates it in place. The unique
// key collapses repeat authorizations onto one row per user+space.
func (r *userSpaceGrantRepository) Upsert(ctx context.Context, grant *types.UserSpaceGrant) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "uum_user_id"}, {Name: "tenant_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"role", "status", "valid_until", "requested_by", "approved_by", "reason", "updated_at",
		}),
	}).Create(grant).Error
}

func (r *userSpaceGrantRepository) GetByID(ctx context.Context, id string) (*types.UserSpaceGrant, error) {
	var grant types.UserSpaceGrant
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&grant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGrantNotFound
		}
		return nil, err
	}
	return &grant, nil
}

func (r *userSpaceGrantRepository) GetByUUMAndTenant(ctx context.Context, uumUserID string, tenantID uint64) (*types.UserSpaceGrant, error) {
	var grant types.UserSpaceGrant
	err := r.db.WithContext(ctx).
		Where("uum_user_id = ? AND tenant_id = ?", uumUserID, tenantID).
		First(&grant).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGrantNotFound
		}
		return nil, err
	}
	return &grant, nil
}

func (r *userSpaceGrantRepository) List(ctx context.Context, filter types.UserSpaceGrantFilter) ([]*types.UserSpaceGrant, error) {
	q := r.db.WithContext(ctx).Model(&types.UserSpaceGrant{})
	if filter.TenantID != 0 {
		q = q.Where("tenant_id = ?", filter.TenantID)
	}
	if filter.UUMUser != "" {
		q = q.Where("uum_user_id = ?", filter.UUMUser)
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	var grants []*types.UserSpaceGrant
	if err := q.Order("updated_at DESC").Limit(500).Find(&grants).Error; err != nil {
		return nil, err
	}
	return grants, nil
}

// ListEffective returns grants that currently grant access for the user.
func (r *userSpaceGrantRepository) ListEffective(ctx context.Context, uumUserID string, now time.Time) ([]*types.UserSpaceGrant, error) {
	var grants []*types.UserSpaceGrant
	err := r.db.WithContext(ctx).
		Where("uum_user_id = ? AND status = ? AND (valid_until IS NULL OR valid_until > ?)",
			uumUserID, types.UserSpaceGrantActive, now).
		Find(&grants).Error
	return grants, err
}

// SetStatus transitions one row's status (guarded by expectStatus when non-empty).
func (r *userSpaceGrantRepository) SetStatus(ctx context.Context, id string, expect types.UserSpaceGrantStatus, to types.UserSpaceGrantStatus) error {
	q := r.db.WithContext(ctx).Model(&types.UserSpaceGrant{}).
		Where("id = ?", id)
	if expect != "" {
		q = q.Where("status = ?", expect)
	}
	res := q.Update("status", to)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrGrantNotFound
	}
	return nil
}

// ExpireOverdue materializes status='expired' for active grants whose
// validity window has passed, returning the affected rows so the caller
// can drop the linked memberships in the same sweep.
func (r *userSpaceGrantRepository) ExpireOverdue(ctx context.Context, now time.Time) ([]*types.UserSpaceGrant, error) {
	var expired []*types.UserSpaceGrant
	err := r.db.WithContext(ctx).
		Where("status = ? AND valid_until IS NOT NULL AND valid_until <= ?",
			types.UserSpaceGrantActive, now).
		Find(&expired).Error
	if err != nil {
		return nil, err
	}
	for _, g := range expired {
		if err := r.SetStatus(ctx, g.ID, types.UserSpaceGrantActive, types.UserSpaceGrantExpired); err != nil &&
			!errors.Is(err, ErrGrantNotFound) {
			return expired, err
		}
	}
	return expired, nil
}

// ResolveUserIDByUUM maps a UUM id to a WeKnora user via the identity
// bridge mapping table (any tenant row). Empty string = user not
// provisioned yet (pre-authorization still pending first login).
func (r *userSpaceGrantRepository) ResolveUserIDByUUM(ctx context.Context, uumUserID string) (string, bool, error) {
	var identity types.ExternalIdentity
	err := r.db.WithContext(ctx).
		Where("uum_user_id = ?", uumUserID).
		Order("updated_at DESC").
		First(&identity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return identity.UserID, true, nil
}
