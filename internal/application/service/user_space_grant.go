package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"

	"github.com/google/uuid"
)

var (
	ErrGrantInvalidRole   = errors.New("grant role must be admin/contributor/viewer (owner is never granted to persons)")
	ErrGrantTenantMissing = errors.New("target space (tenant) not found")
)

// userSpaceGrantService implements the v2 grant state machine:
//
//	pending → active → expired / revoked
//
// Effective state is projected into the native tenant_members table. The
// projection is NOT wrapped in one cross-table transaction (the codebase
// convention keeps transactions inside repositories); instead three
// healing paths converge partial failures, mirroring the declarative
// bridge semantics of decision 020:
//
//  1. every transition applies the membership side-effect immediately;
//  2. login/bridge calls ApplyForUser to re-materialize effective grants;
//  3. Sweep periodically expires overdue grants and reconciles every
//     grant-linked membership with its grant state.
//
// Scope guarantee: memberships for (user, tenant) pairs WITHOUT a grant
// row are never touched — native memberships (personal spaces, legacy
// department tenants) stay outside the grant engine.
type userSpaceGrantService struct {
	repo          interfaces.UserSpaceGrantRepository
	memberService interfaces.TenantMemberService
	tenantService interfaces.TenantService
}

// NewUserSpaceGrantService creates the grant service.
func NewUserSpaceGrantService(
	repo interfaces.UserSpaceGrantRepository,
	memberService interfaces.TenantMemberService,
	tenantService interfaces.TenantService,
) (interfaces.UserSpaceGrantService, error) {
	return &userSpaceGrantService{
		repo:          repo,
		memberService: memberService,
		tenantService: tenantService,
	}, nil
}

func (s *userSpaceGrantService) validateTarget(ctx context.Context, tenantID uint64) error {
	tenant, err := s.tenantService.GetTenantByID(ctx, tenantID)
	if err != nil || tenant == nil {
		return werrors.NewNotFoundError("目标空间不存在")
	}
	return nil
}

func validGrantRole(role types.TenantRole) bool {
	for _, r := range types.ValidGrantRoles() {
		if role == r {
			return true
		}
	}
	return false
}

func (s *userSpaceGrantService) Create(ctx context.Context, req *interfaces.UserSpaceGrantAdminRequest, actor string) (*types.UserSpaceGrant, error) {
	if req.UUMUserID == "" || req.TenantID == 0 {
		return nil, werrors.NewBadRequestError("uum_user_id 与 tenant_id 不能为空")
	}
	if req.Role == "" {
		req.Role = types.TenantRoleViewer
	}
	if !validGrantRole(req.Role) {
		return nil, werrors.NewBadRequestError(ErrGrantInvalidRole.Error())
	}
	if err := s.validateTarget(ctx, req.TenantID); err != nil {
		return nil, err
	}
	status := types.UserSpaceGrantPending
	if req.ActivateNow {
		status = types.UserSpaceGrantActive
	}
	grant := &types.UserSpaceGrant{
		ID:          uuid.New().String(),
		UUMUserID:   req.UUMUserID,
		TenantID:    req.TenantID,
		Role:        req.Role,
		Status:      status,
		ValidUntil:  req.ValidUntil,
		RequestedBy: actor,
		Reason:      req.Reason,
	}
	if err := s.repo.Upsert(ctx, grant); err != nil {
		return nil, err
	}
	if status == types.UserSpaceGrantActive {
		s.applyGrantMembership(ctx, grant)
	}
	logger.Infof(ctx, "UserSpaceGrant created, actor=%s uum=%s tenant=%d role=%s status=%s",
		actor, req.UUMUserID, req.TenantID, req.Role, status)
	return s.repo.GetByID(ctx, grant.ID)
}

// Approve flips pending → active and applies the membership side effect.
// The linked WeKnora user may not exist yet (pre-authorization): the
// membership is then materialized by ApplyForUser at first login.
func (s *userSpaceGrantService) Approve(ctx context.Context, id, actor string) (*types.UserSpaceGrant, error) {
	grant, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, werrors.NewNotFoundError("授权不存在")
	}
	if grant.Status != types.UserSpaceGrantPending {
		return grant, nil // idempotent: only pending rows can be approved
	}
	grant.Status = types.UserSpaceGrantActive
	grant.ApprovedBy = actor
	if err := s.repo.Upsert(ctx, grant); err != nil {
		return nil, err
	}
	s.applyGrantMembership(ctx, grant)
	logger.Infof(ctx, "UserSpaceGrant approved, actor=%s id=%s uum=%s tenant=%d", actor, id, grant.UUMUserID, grant.TenantID)
	return s.repo.GetByID(ctx, id)
}

func (s *userSpaceGrantService) UpdateRole(ctx context.Context, id string, role types.TenantRole, actor string) (*types.UserSpaceGrant, error) {
	if !validGrantRole(role) {
		return nil, werrors.NewBadRequestError(ErrGrantInvalidRole.Error())
	}
	grant, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, werrors.NewNotFoundError("授权不存在")
	}
	grant.Role = role
	if err := s.repo.Upsert(ctx, grant); err != nil {
		return nil, err
	}
	if grant.Status == types.UserSpaceGrantActive {
		s.applyGrantMembership(ctx, grant) // UpdateRole for active grants syncs the membership role
	}
	logger.Infof(ctx, "UserSpaceGrant role updated, actor=%s id=%s role=%s", actor, id, role)
	return s.repo.GetByID(ctx, id)
}

func (s *userSpaceGrantService) UpdateValidity(ctx context.Context, id string, validUntil *time.Time, actor string) (*types.UserSpaceGrant, error) {
	grant, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, werrors.NewNotFoundError("授权不存在")
	}
	grant.ValidUntil = validUntil
	// reactivating a revoked grant is a deliberate admin decision
	if grant.Status == types.UserSpaceGrantExpired {
		grant.Status = types.UserSpaceGrantActive
	}
	if err := s.repo.Upsert(ctx, grant); err != nil {
		return nil, err
	}
	if grant.Status == types.UserSpaceGrantActive {
		s.applyGrantMembership(ctx, grant)
	}
	logger.Infof(ctx, "UserSpaceGrant validity updated, actor=%s id=%s valid_until=%v", actor, id, validUntil)
	return s.repo.GetByID(ctx, id)
}

// Revoke withdraws the grant and removes the linked membership — the
// removal takes effect on the user's next request (per-request membership
// check, middleware/auth.go resolveTenantRole), no token revocation needed.
func (s *userSpaceGrantService) Revoke(ctx context.Context, id, actor string) error {
	grant, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return werrors.NewNotFoundError("授权不存在")
	}
	if grant.Status == types.UserSpaceGrantRevoked {
		return nil // idempotent
	}
	grant.Status = types.UserSpaceGrantRevoked
	if err := s.repo.Upsert(ctx, grant); err != nil {
		return err
	}
	s.dropGrantMembership(ctx, grant)
	logger.WarnWithFields(ctx, map[string]interface{}{
		"actor": actor, "id": id, "uum_user_id": grant.UUMUserID, "tenant_id": grant.TenantID,
	}, "UserSpaceGrant revoked; membership removal takes effect on next request")
	return nil
}

func (s *userSpaceGrantService) Get(ctx context.Context, id string) (*types.UserSpaceGrant, error) {
	grant, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, werrors.NewNotFoundError("授权不存在")
	}
	return grant, nil
}

func (s *userSpaceGrantService) List(ctx context.Context, filter types.UserSpaceGrantFilter) ([]*types.UserSpaceGrant, error) {
	return s.repo.List(ctx, filter)
}

// applyGrantMembership ensures the tenant_members row matches an effective
// grant (add when missing, sync role when different). Errors are logged,
// not propagated: the sweeper and the next login re-attempt (convergence).
func (s *userSpaceGrantService) applyGrantMembership(ctx context.Context, grant *types.UserSpaceGrant) {
	userID, ok, err := s.repo.ResolveUserIDByUUM(ctx, grant.UUMUserID)
	if err != nil || !ok {
		return // pre-authorized: membership materializes at first login
	}
	member, err := s.memberService.GetMembership(ctx, userID, grant.TenantID)
	if err != nil {
		logger.Warnf(ctx, "UserSpaceGrant apply: membership lookup failed uum=%s tenant=%d: %v", grant.UUMUserID, grant.TenantID, err)
		return
	}
	if member == nil {
		if _, err := s.memberService.AddMember(ctx, userID, grant.TenantID, grant.Role, nil); err != nil {
			logger.Warnf(ctx, "UserSpaceGrant apply: add member failed uum=%s tenant=%d: %v", grant.UUMUserID, grant.TenantID, err)
		}
		return
	}
	if member.Role != grant.Role {
		if err := s.memberService.UpdateRole(ctx, userID, grant.TenantID, grant.Role); err != nil {
			logger.Warnf(ctx, "UserSpaceGrant apply: role sync failed uum=%s tenant=%d: %v", grant.UUMUserID, grant.TenantID, err)
		}
	}
}

func (s *userSpaceGrantService) dropGrantMembership(ctx context.Context, grant *types.UserSpaceGrant) {
	userID, ok, err := s.repo.ResolveUserIDByUUM(ctx, grant.UUMUserID)
	if err != nil || !ok {
		return
	}
	if err := s.memberService.RemoveMember(ctx, userID, grant.TenantID); err != nil {
		logger.Warnf(ctx, "UserSpaceGrant drop: remove member failed uum=%s tenant=%d: %v", grant.UUMUserID, grant.TenantID, err)
	}
}

// ApplyForUser (re)materializes one user's effective grants at login time.
func (s *userSpaceGrantService) ApplyForUser(ctx context.Context, uumUserID, userID string) error {
	grants, err := s.repo.List(ctx, types.UserSpaceGrantFilter{UUMUser: uumUserID})
	if err != nil {
		return err
	}
	now := time.Now()
	applied := 0
	for _, grant := range grants {
		if grant.Effective(now) {
			member, err := s.memberService.GetMembership(ctx, userID, grant.TenantID)
			if err != nil {
				return err
			}
			if member == nil {
				if _, err := s.memberService.AddMember(ctx, userID, grant.TenantID, grant.Role, nil); err != nil {
					return fmt.Errorf("apply grant membership: %w", err)
				}
				applied++
			} else if member.Role != grant.Role {
				if err := s.memberService.UpdateRole(ctx, userID, grant.TenantID, grant.Role); err != nil {
					return fmt.Errorf("sync grant role: %w", err)
				}
				applied++
			}
		}
	}
	if applied > 0 {
		logger.Infof(ctx, "UserSpaceGrant login apply: uum=%s user=%s materialized=%d", uumUserID, userID, applied)
	}
	return nil
}

// Sweep expires overdue grants then reconciles grant-linked memberships.
// It is the convergence path for partial failures of earlier transitions.
func (s *userSpaceGrantService) Sweep(ctx context.Context, now time.Time) (interfaces.UserSpaceGrantSweepStats, error) {
	stats := interfaces.UserSpaceGrantSweepStats{}

	expired, err := s.repo.ExpireOverdue(ctx, now)
	if err != nil {
		return stats, err
	}
	stats.Expired = len(expired)
	for _, grant := range expired {
		s.dropGrantMembership(ctx, grant)
	}

	grants, err := s.repo.List(ctx, types.UserSpaceGrantFilter{})
	if err != nil {
		return stats, err
	}
	for _, grant := range grants {
		userID, ok, err := s.repo.ResolveUserIDByUUM(ctx, grant.UUMUserID)
		if err != nil || !ok {
			continue // pre-authorized user not provisioned yet
		}
		member, err := s.memberService.GetMembership(ctx, userID, grant.TenantID)
		if err != nil {
			continue
		}
		switch {
		case grant.Effective(now) && member == nil:
			if _, err := s.memberService.AddMember(ctx, userID, grant.TenantID, grant.Role, nil); err == nil {
				stats.MembershipAdded++
			}
		case grant.Effective(now) && member != nil && member.Role != grant.Role:
			if err := s.memberService.UpdateRole(ctx, userID, grant.TenantID, grant.Role); err == nil {
				stats.MembershipSynced++
			}
		case !grant.Effective(now) && grant.Status != types.UserSpaceGrantPending && member != nil:
			// pending grants never had a membership; only non-pending
			// non-effective grants (revoked/expired) must drop theirs
			if err := s.memberService.RemoveMember(ctx, userID, grant.TenantID); err == nil {
				stats.MembershipDropped++
			}
		}
	}
	if stats.Expired > 0 || stats.MembershipAdded > 0 || stats.MembershipDropped > 0 {
		logger.WarnWithFields(ctx, map[string]interface{}{"stats": fmt.Sprintf("%+v", stats)},
			"UserSpaceGrant sweep completed")
	}
	return stats, nil
}
