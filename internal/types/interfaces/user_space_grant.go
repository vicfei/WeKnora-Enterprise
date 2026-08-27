package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// UserSpaceGrantRepository persists user_space_grants rows.
type UserSpaceGrantRepository interface {
	Upsert(ctx context.Context, grant *types.UserSpaceGrant) error
	GetByID(ctx context.Context, id string) (*types.UserSpaceGrant, error)
	GetByUUMAndTenant(ctx context.Context, uumUserID string, tenantID uint64) (*types.UserSpaceGrant, error)
	List(ctx context.Context, filter types.UserSpaceGrantFilter) ([]*types.UserSpaceGrant, error)
	ListEffective(ctx context.Context, uumUserID string, now time.Time) ([]*types.UserSpaceGrant, error)
	SetStatus(ctx context.Context, id string, expect, to types.UserSpaceGrantStatus) error
	ExpireOverdue(ctx context.Context, now time.Time) ([]*types.UserSpaceGrant, error)
	ResolveUserIDByUUM(ctx context.Context, uumUserID string) (string, bool, error)
}

// UserSpaceGrantAdminRequest is the admin-console payload for creating or
// updating a grant. ActivateNow=true approves immediately (pre-authorized
// users get their membership materialized at first login instead).
type UserSpaceGrantAdminRequest struct {
	UUMUserID   string        `json:"uum_user_id"`
	TenantID    uint64        `json:"tenant_id"`
	Role        types.TenantRole `json:"role"`
	ValidUntil  *time.Time    `json:"valid_until"`
	Reason      string        `json:"reason"`
	ActivateNow bool          `json:"activate_now"`
}

// UserSpaceGrantSweepStats reports what one reconcile pass did.
type UserSpaceGrantSweepStats struct {
	Expired          int `json:"expired"`
	MembershipAdded  int `json:"membership_added"`
	MembershipSynced int `json:"membership_synced"`
	MembershipDropped int `json:"membership_dropped"`
}

// UserSpaceGrantService is the v2 grant state machine (admin-facing).
type UserSpaceGrantService interface {
	Create(ctx context.Context, req *UserSpaceGrantAdminRequest, actor string) (*types.UserSpaceGrant, error)
	Approve(ctx context.Context, id, actor string) (*types.UserSpaceGrant, error)
	UpdateRole(ctx context.Context, id string, role types.TenantRole, actor string) (*types.UserSpaceGrant, error)
	UpdateValidity(ctx context.Context, id string, validUntil *time.Time, actor string) (*types.UserSpaceGrant, error)
	Revoke(ctx context.Context, id, actor string) error
	Get(ctx context.Context, id string) (*types.UserSpaceGrant, error)
	List(ctx context.Context, filter types.UserSpaceGrantFilter) ([]*types.UserSpaceGrant, error)
	// ApplyForUser materializes the effective grants of one user into
	// tenant_members (and drops memberships whose grant stopped being
	// effective). Called at login/bridge time.
	ApplyForUser(ctx context.Context, uumUserID, userID string) error
	// Sweep expires overdue grants and reconciles memberships with grant
	// state; it converges partial failures of earlier transitions.
	Sweep(ctx context.Context, now time.Time) (UserSpaceGrantSweepStats, error)
}
