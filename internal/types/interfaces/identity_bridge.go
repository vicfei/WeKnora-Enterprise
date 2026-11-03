package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// IdentityBridgeRepository persists UUM identity mappings and the UUM
// department↔tenant mapping table.
type IdentityBridgeRepository interface {
	CreateMapping(ctx context.Context, identity *types.ExternalIdentity) error
	// GetMappingByUUM returns the mapping of a UUM user within one tenant,
	// or gorm.ErrRecordNotFound when absent.
	GetMappingByUUM(ctx context.Context, uumUserID string, tenantID uint64) (*types.ExternalIdentity, error)
	// GetMappingsByUser returns every mapping row of a WeKnora user.
	GetMappingsByUser(ctx context.Context, userID string) ([]*types.ExternalIdentity, error)
	// GetAnyMappingByUUM returns the most recent mapping row of a UUM user
	// across tenants (nil when none) — used by the tenantless personal-space
	// bridge to find an already-provisioned user.
	GetAnyMappingByUUM(ctx context.Context, uumUserID string) (*types.ExternalIdentity, error)
	// DeleteMapping removes the (uumUserID, tenantID) mapping row. Transfer
	// semantics: the row is deleted, never stacked as history (decision 020).
	DeleteMapping(ctx context.Context, uumUserID string, tenantID uint64) error
	// UpdateMappingSnapshot refreshes the department snapshot column.
	UpdateMappingSnapshot(ctx context.Context, identity *types.ExternalIdentity) error
	// UpsertDepartment binds a UUM department to a tenant (1:1 both ways);
	// returns a conflict sentinel when either side is already bound
	// elsewhere.
	UpsertDepartment(ctx context.Context, dept *types.UUMDepartment) (*types.UUMDepartment, error)
	GetDepartment(ctx context.Context, uumDepartmentID string) (*types.UUMDepartment, error)
}

// IdentityBridgeService implements the UUM identity bridge contract between
// the external portal backend and WeKnora (decision 020). Web-portal users
// get real accounts with full native RBAC; the signed_token API channel is a
// separate concern that never touches this service.
type IdentityBridgeService interface {
	// Bridge declaratively ensures the UUM user holds access to the target
	// department tenant and returns a login response (token pair for the
	// Web portal channel) plus whether a local account was provisioned by
	// this call. Idempotent: repeated pushes neither fail nor duplicate
	// data.
	Bridge(ctx context.Context, req *types.IdentityBridgeRequest) (*types.LoginResponse, bool, error)
	// Disable deactivates the bridged user and revokes every issued token
	// (two-step combination — is_active alone would leave live JWTs).
	Disable(ctx context.Context, uumUserID string, tenantID uint64) error
	// Transfer moves the user between department tenants: membership moves,
	// the old mapping row is deleted, tokens are revoked (forcing a fresh
	// bridge), resources stay in the source tenant.
	Transfer(ctx context.Context, req *types.IdentityTransferRequest) (*types.ExternalIdentity, error)
	GetMapping(ctx context.Context, uumUserID string, tenantID uint64) (*types.ExternalIdentity, error)
	GetMappingsByUser(ctx context.Context, userID string) ([]*types.ExternalIdentity, error)
	UpsertDepartment(ctx context.Context, uumDepartmentID string, req *types.UUMDepartmentRequest) (*types.UUMDepartment, error)
}
