package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrUUMDepartmentConflict is returned when a department or tenant side of
// the 1:1 department↔tenant mapping is already bound elsewhere.
var ErrUUMDepartmentConflict = errors.New("uum department mapping conflict")

// identityBridgeRepository persists UUM identity mappings.
type identityBridgeRepository struct {
	db *gorm.DB
}

// NewIdentityBridgeRepository creates a new identity bridge repository.
func NewIdentityBridgeRepository(db *gorm.DB) interfaces.IdentityBridgeRepository {
	return &identityBridgeRepository{db: db}
}

func (r *identityBridgeRepository) CreateMapping(ctx context.Context, identity *types.ExternalIdentity) error {
	return r.db.WithContext(ctx).Create(identity).Error
}

func (r *identityBridgeRepository) GetMappingByUUM(
	ctx context.Context,
	uumUserID string,
	tenantID uint64,
) (*types.ExternalIdentity, error) {
	var identity types.ExternalIdentity
	if err := r.db.WithContext(ctx).
		Where("uum_user_id = ? AND tenant_id = ?", uumUserID, tenantID).
		First(&identity).Error; err != nil {
		return nil, err
	}
	return &identity, nil
}

func (r *identityBridgeRepository) GetMappingsByUser(ctx context.Context, userID string) ([]*types.ExternalIdentity, error) {
	var identities []*types.ExternalIdentity
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&identities).Error; err != nil {
		return nil, err
	}
	return identities, nil
}

func (r *identityBridgeRepository) GetAnyMappingByUUM(ctx context.Context, uumUserID string) (*types.ExternalIdentity, error) {
	var identity types.ExternalIdentity
	err := r.db.WithContext(ctx).
		Where("uum_user_id = ?", uumUserID).
		Order("updated_at DESC").
		First(&identity).Error
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

func (r *identityBridgeRepository) DeleteMapping(ctx context.Context, uumUserID string, tenantID uint64) error {
	return r.db.WithContext(ctx).
		Where("uum_user_id = ? AND tenant_id = ?", uumUserID, tenantID).
		Delete(&types.ExternalIdentity{}).Error
}

func (r *identityBridgeRepository) UpdateMappingSnapshot(ctx context.Context, identity *types.ExternalIdentity) error {
	return r.db.WithContext(ctx).
		Model(&types.ExternalIdentity{}).
		Where("id = ?", identity.ID).
		Updates(map[string]interface{}{
			"uum_department_id": identity.UUMDepartmentID,
			"updated_at":        time.Now(),
		}).Error
}

// UpsertDepartment binds a UUM department to a WeKnora department tenant.
// Both sides of the mapping are unique (1:1); re-binding the same pair
// refreshes the name, while colliding with a different department or tenant
// returns ErrUUMDepartmentConflict so callers can surface a 409.
func (r *identityBridgeRepository) UpsertDepartment(
	ctx context.Context,
	dept *types.UUMDepartment,
) (*types.UUMDepartment, error) {
	var existing types.UUMDepartment
	err := r.db.WithContext(ctx).
		Where("uum_department_id = ?", dept.UUMDepartmentID).
		First(&existing).Error
	if err == nil {
		if existing.TenantID != dept.TenantID {
			return nil, ErrUUMDepartmentConflict
		}
		existing.DepartmentName = dept.DepartmentName
		existing.UpdatedAt = time.Now()
		if err := r.db.WithContext(ctx).Save(&existing).Error; err != nil {
			return nil, err
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var tenantBound types.UUMDepartment
	err = r.db.WithContext(ctx).
		Where("tenant_id = ?", dept.TenantID).
		First(&tenantBound).Error
	if err == nil && tenantBound.UUMDepartmentID != dept.UUMDepartmentID {
		return nil, ErrUUMDepartmentConflict
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	dept.UpdatedAt = time.Now()
	if err := r.db.WithContext(ctx).Create(dept).Error; err != nil {
		return nil, err
	}
	return dept, nil
}

func (r *identityBridgeRepository) GetDepartment(ctx context.Context, uumDepartmentID string) (*types.UUMDepartment, error) {
	var dept types.UUMDepartment
	if err := r.db.WithContext(ctx).
		Where("uum_department_id = ?", uumDepartmentID).
		First(&dept).Error; err != nil {
		return nil, err
	}
	return &dept, nil
}
