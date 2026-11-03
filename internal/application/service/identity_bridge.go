package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Identity bridge defaults (decision 020): least privilege — newly bridged
// accounts join the department tenant as viewer; department owners promote
// them to contributor on the portal side via native member management.
const (
	identityBridgeSource      = "uum"
	identityBridgeUsernameFmt = "uum-%s"
	identityBridgeEmailFmt    = "%s@uum.invalid"
)

// identityBridgeService implements the UUM identity bridge contract between
// the external portal backend and WeKnora (decision 020). Bridged accounts
// are real users with full native RBAC; the signed_token API channel is a
// separate concern that never goes through this service.
type identityBridgeService struct {
	repo          interfaces.IdentityBridgeRepository
	userService   interfaces.UserService
	memberService interfaces.TenantMemberService
	tenantService interfaces.TenantService
	tokenRepo     interfaces.AuthTokenRepository
	grantService  interfaces.UserSpaceGrantService
}

// NewIdentityBridgeService creates a new identity bridge service.
func NewIdentityBridgeService(
	repo interfaces.IdentityBridgeRepository,
	userService interfaces.UserService,
	memberService interfaces.TenantMemberService,
	tenantService interfaces.TenantService,
	tokenRepo interfaces.AuthTokenRepository,
	grantService interfaces.UserSpaceGrantService,
) (interfaces.IdentityBridgeService, error) {
	return &identityBridgeService{
		repo:          repo,
		userService:   userService,
		memberService: memberService,
		tenantService: tenantService,
		tokenRepo:     tokenRepo,
		grantService:  grantService,
	}, nil
}

// Bridge declaratively ensures the UUM user holds access to the target
// department tenant and returns a login response whose JWT carries the
// department tenant claim. Declarative semantics (decision 020): a repeated
// push with a stale department re-grants that department — the caller owns
// the event-ordering constraint (transfer/disable first).
func (s *identityBridgeService) Bridge(
	ctx context.Context,
	req *types.IdentityBridgeRequest,
) (*types.LoginResponse, bool, error) {
	if strings.TrimSpace(req.UUMUserID) == "" {
		return nil, false, werrors.NewBadRequestError("uum_user_id 不能为空")
	}
	if len(req.UUMUserID) > 128 {
		return nil, false, werrors.NewBadRequestError("uum_user_id 长度不能超过128")
	}
	req.UUMDepartmentID = strings.TrimSpace(req.UUMDepartmentID)

	// v2: tenant_id 省略 → 个人空间 bootstrap（已有映射/成员则登录既有空间，
	// 全无则创建个人空间并成为 owner），随后按活跃 grants 补成员关系。
	if req.TenantID == 0 {
		return s.bridgePersonalSpace(ctx, req)
	}
	if _, err := s.tenantService.GetTenantByID(ctx, req.TenantID); err != nil {
		return nil, false, werrors.NewNotFoundError("目标部门租户不存在")
	}

	operator := s.operatorForLog(ctx)
	existing, err := s.repo.GetMappingByUUM(ctx, req.UUMUserID, req.TenantID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	if existing != nil {
		// Login path: the mapping is alive only if the user exists, is
		// active and still holds an active membership — a stale mapping
		// (user transferred away) must not mint new tokens.
		user, err := s.loadActiveUser(ctx, existing.UserID)
		if err != nil {
			return nil, false, err
		}
		if err := s.requireMembership(ctx, user.ID, req.TenantID); err != nil {
			return nil, false, err
		}
		if req.UUMDepartmentID != "" && req.UUMDepartmentID != existing.UUMDepartmentID {
			existing.UUMDepartmentID = req.UUMDepartmentID
			if err := s.repo.UpdateMappingSnapshot(ctx, existing); err != nil {
				return nil, false, err
			}
		}
		s.applySpaceGrants(ctx, req.UUMUserID, user.ID)
		resp, err := s.issueLogin(ctx, user, req.TenantID)
		if err != nil {
			return nil, false, err
		}
		logger.Infof(ctx, "Identity bridge login, operator: %s, uum_user_id: %s, tenant_id: %d, user_id: %s",
			operator, req.UUMUserID, req.TenantID, user.ID)
		return resp, false, nil
	}

	// Heal-or-register path. Heal: the user was provisioned before but the
	// mapping row is missing (e.g. Java re-pushing after a lost event) —
	// re-attach membership and mapping instead of colliding on unique keys.
	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = fmt.Sprintf(identityBridgeUsernameFmt, req.UUMUserID)
	}
	email := strings.TrimSpace(req.Email)
	if email == "" {
		email = fmt.Sprintf(identityBridgeEmailFmt, req.UUMUserID)
	}

	user, err := s.userService.GetUserByUsername(ctx, username)
	if err != nil && !isUserLookupNotFound(err) {
		return nil, false, err
	}
	isNew := false
	if user == nil {
		password, perr := generateRandomString(32)
		if perr != nil {
			return nil, false, perr
		}
		// Tenantless provisioning: the department membership below (not a
		// personal workspace) becomes the user's tenant context.
		user, err = s.userService.Register(ctx, &types.RegisterRequest{
			Username:          username,
			Email:             email,
			Password:          password,
			TenantProvisioning: types.TenantProvisioningTenantless,
		})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return nil, false, werrors.NewConflictError("用户名或邮箱已被占用")
			}
			return nil, false, err
		}
		isNew = true
	}
	if !user.IsActive {
		return nil, false, werrors.NewForbiddenError("用户已被禁用")
	}
	if err := s.ensureMembership(ctx, user.ID, req.TenantID, req.Role); err != nil {
		return nil, false, err
	}

	s.applySpaceGrants(ctx, req.UUMUserID, user.ID)
	identity := &types.ExternalIdentity{
		ID:              uuid.New().String(),
		UUMUserID:       req.UUMUserID,
		UserID:          user.ID,
		TenantID:        req.TenantID,
		UUMDepartmentID: req.UUMDepartmentID,
		Source:          identityBridgeSource,
	}
	if err := s.repo.CreateMapping(ctx, identity); err != nil {
		// Lost a concurrent-create race: the winner's row is authoritative.
		if winner, rerr := s.repo.GetMappingByUUM(ctx, req.UUMUserID, req.TenantID); rerr == nil && winner != nil {
			identity = winner
		} else {
			return nil, false, err
		}
	}
	if req.UUMDepartmentID != "" {
		if _, err := s.upsertDepartment(ctx, req.UUMDepartmentID, req.TenantID, ""); err != nil {
			return nil, false, err
		}
	}

	resp, err := s.issueLogin(ctx, user, req.TenantID)
	if err != nil {
		return nil, false, err
	}
	logger.Infof(ctx, "Identity bridge provisioned, operator: %s, uum_user_id: %s, tenant_id: %d, user_id: %s, is_new: %t",
		operator, req.UUMUserID, req.TenantID, user.ID, isNew)
	return resp, isNew, nil
}

// Disable deactivates the bridged user AND revokes every issued token.
// is_active alone would leave live JWTs valid for up to 24h (decision 020's
// two-step combination).
func (s *identityBridgeService) Disable(ctx context.Context, uumUserID string, tenantID uint64) error {
	if strings.TrimSpace(uumUserID) == "" || tenantID == 0 {
		return werrors.NewBadRequestError("uum_user_id 和 tenant_id 不能为空")
	}
	mapping, err := s.repo.GetMappingByUUM(ctx, uumUserID, tenantID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return werrors.NewNotFoundError("身份映射不存在")
		}
		return err
	}
	user, err := s.userService.GetUserByID(ctx, mapping.UserID)
	if err != nil || user == nil {
		return werrors.NewNotFoundError("映射用户不存在")
	}
	if user.IsActive {
		user.IsActive = false
		if err := s.userService.UpdateUser(ctx, user); err != nil {
			return err
		}
	}
	if err := s.tokenRepo.RevokeTokensByUserID(ctx, user.ID); err != nil {
		return err
	}
	logger.WarnWithFields(ctx, map[string]interface{}{
		"operator":    s.operatorForLog(ctx),
		"uum_user_id": uumUserID,
		"tenant_id":   tenantID,
		"user_id":     user.ID,
	}, "UUM bridged user disabled and all tokens revoked")
	return nil
}

// Transfer moves the user between department tenants: membership moves, the
// old mapping row is DELETED (history lives in logs, decision 020), tokens
// are revoked (the old JWT's tenant claim is stale anyway), and resources
// stay in the source tenant. Concurrent membership in several departments
// ("兼任") is NOT handled here — use native tenant member management.
func (s *identityBridgeService) Transfer(
	ctx context.Context,
	req *types.IdentityTransferRequest,
) (*types.ExternalIdentity, error) {
	if strings.TrimSpace(req.UUMUserID) == "" || req.FromTenantID == 0 || req.ToTenantID == 0 {
		return nil, werrors.NewBadRequestError("uum_user_id、from_tenant_id 和 to_tenant_id 不能为空")
	}
	if req.FromTenantID == req.ToTenantID {
		return nil, werrors.NewBadRequestError("源部门与目标部门不能相同")
	}

	mapping, err := s.repo.GetMappingByUUM(ctx, req.UUMUserID, req.FromTenantID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Idempotent re-push after a completed transfer.
			if current, cerr := s.repo.GetMappingByUUM(ctx, req.UUMUserID, req.ToTenantID); cerr == nil && current != nil {
				return current, nil
			}
			return nil, werrors.NewNotFoundError("源部门身份映射不存在")
		}
		return nil, err
	}
	user, err := s.userService.GetUserByID(ctx, mapping.UserID)
	if err != nil || user == nil {
		return nil, werrors.NewNotFoundError("映射用户不存在")
	}
	if _, err := s.tenantService.GetTenantByID(ctx, req.ToTenantID); err != nil {
		return nil, werrors.NewNotFoundError("目标部门租户不存在")
	}

	// Preserve the current role unless explicitly overridden.
	role := strings.TrimSpace(req.Role)
	if role == "" {
		if member, merr := s.memberService.GetMembership(ctx, user.ID, req.FromTenantID); merr == nil && member != nil && member.Role.IsValid() {
			role = string(member.Role)
		}
	}
	if role == "" {
		role = string(types.TenantRoleViewer)
	}

	// Membership move. A failure between remove and add leaves the user
	// membership-less; the next declarative bridge push heals it.
	if err := s.memberService.RemoveMember(ctx, user.ID, req.FromTenantID); err != nil {
		return nil, werrors.NewConflictError("移出源部门失败").WithDetails(err.Error())
	}
	if err := s.ensureMembership(ctx, user.ID, req.ToTenantID, role); err != nil {
		return nil, err
	}

	if err := s.repo.DeleteMapping(ctx, req.UUMUserID, req.FromTenantID); err != nil {
		return nil, err
	}
	newMapping := &types.ExternalIdentity{
		ID:              uuid.New().String(),
		UUMUserID:       req.UUMUserID,
		UserID:          user.ID,
		TenantID:        req.ToTenantID,
		UUMDepartmentID: mapping.UUMDepartmentID,
		Source:          mapping.Source,
	}
	if err := s.repo.CreateMapping(ctx, newMapping); err != nil {
		if winner, rerr := s.repo.GetMappingByUUM(ctx, req.UUMUserID, req.ToTenantID); rerr == nil && winner != nil {
			newMapping = winner
		} else {
			return nil, err
		}
	}
	if err := s.tokenRepo.RevokeTokensByUserID(ctx, user.ID); err != nil {
		return nil, err
	}
	logger.WarnWithFields(ctx, map[string]interface{}{
		"operator":        s.operatorForLog(ctx),
		"uum_user_id":     req.UUMUserID,
		"from_tenant_id":  req.FromTenantID,
		"to_tenant_id":    req.ToTenantID,
		"user_id":         user.ID,
		"role":            role,
	}, "UUM bridged user transferred between department tenants; resources stay in the source tenant")
	return newMapping, nil
}

func (s *identityBridgeService) GetMapping(ctx context.Context, uumUserID string, tenantID uint64) (*types.ExternalIdentity, error) {
	mapping, err := s.repo.GetMappingByUUM(ctx, uumUserID, tenantID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, werrors.NewNotFoundError("身份映射不存在")
		}
		return nil, err
	}
	return mapping, nil
}

func (s *identityBridgeService) GetMappingsByUser(ctx context.Context, userID string) ([]*types.ExternalIdentity, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, werrors.NewBadRequestError("user_id 不能为空")
	}
	return s.repo.GetMappingsByUser(ctx, userID)
}

func (s *identityBridgeService) UpsertDepartment(
	ctx context.Context,
	uumDepartmentID string,
	req *types.UUMDepartmentRequest,
) (*types.UUMDepartment, error) {
	if strings.TrimSpace(uumDepartmentID) == "" || req.TenantID == 0 {
		return nil, werrors.NewBadRequestError("department_id 和 tenant_id 不能为空")
	}
	return s.upsertDepartment(ctx, uumDepartmentID, req.TenantID, req.DepartmentName)
}

func (s *identityBridgeService) upsertDepartment(
	ctx context.Context,
	uumDepartmentID string,
	tenantID uint64,
	departmentName string,
) (*types.UUMDepartment, error) {
	if _, err := s.tenantService.GetTenantByID(ctx, tenantID); err != nil {
		return nil, werrors.NewNotFoundError("目标部门租户不存在")
	}
	dept, err := s.repo.UpsertDepartment(ctx, &types.UUMDepartment{
		ID:              uuid.New().String(),
		UUMDepartmentID: uumDepartmentID,
		TenantID:        tenantID,
		DepartmentName:  strings.TrimSpace(departmentName),
	})
	if err != nil {
		if errors.Is(err, repository.ErrUUMDepartmentConflict) {
			return nil, werrors.NewConflictError("部门或租户已被其他映射占用（部门↔租户为 1:1）")
		}
		return nil, err
	}
	return dept, nil
}

// issueLogin mints a login response scoped to the department tenant.
// SwitchTenant with an empty refresh token is exactly "mint tokens for a
// membership-verified explicit tenant" — it reuses the native issuance path
// and re-validates membership once more before signing.
func (s *identityBridgeService) issueLogin(ctx context.Context, user *types.User, tenantID uint64) (*types.LoginResponse, error) {
	resp, err := s.userService.SwitchTenant(ctx, user, tenantID, "")
	if err != nil {
		if errors.Is(err, ErrMembershipNotFound) {
			return nil, werrors.NewForbiddenError("用户不是目标部门的成员")
		}
		return nil, err
	}
	return resp, nil
}

// loadActiveUser loads a bridged user and refuses disabled accounts.
func (s *identityBridgeService) loadActiveUser(ctx context.Context, userID string) (*types.User, error) {
	user, err := s.userService.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return nil, werrors.NewNotFoundError("映射用户不存在")
	}
	if !user.IsActive {
		return nil, werrors.NewForbiddenError("用户已被禁用")
	}
	return user, nil
}

// requireMembership rejects stale mappings: the user must still hold an
// active membership in the department tenant.
func (s *identityBridgeService) requireMembership(ctx context.Context, userID string, tenantID uint64) error {
	member, err := s.memberService.GetMembership(ctx, userID, tenantID)
	if err != nil || member == nil || member.Status != types.TenantMemberStatusActive {
		return werrors.NewForbiddenError("用户已不是该部门的成员，请先处理调岗/离职事件")
	}
	return nil
}

// ensureMembership is the declarative half of Bridge: an existing active
// membership is a no-op regardless of role; a missing one is created with
// the requested (or default viewer) role.
func (s *identityBridgeService) ensureMembership(ctx context.Context, userID string, tenantID uint64, roleStr string) error {
	role := types.TenantRole(strings.TrimSpace(roleStr))
	if role == "" {
		role = types.TenantRoleViewer
	}
	if !role.IsValid() {
		return werrors.NewBadRequestError(fmt.Sprintf("无效的角色: %q", roleStr))
	}
	if member, err := s.memberService.GetMembership(ctx, userID, tenantID); err == nil && member != nil &&
		member.Status == types.TenantMemberStatusActive {
		return nil
	}
	if _, err := s.memberService.AddMember(ctx, userID, tenantID, role, nil); err != nil {
		if errors.Is(err, ErrMembershipAlreadyExists) {
			return nil
		}
		return err
	}
	return nil
}

// bridgePersonalSpace is the v2 tenantless entry: "SSO gives just a user id".
// Resolution order: existing mapping → login into an active membership or the
// home tenant; no mapping but a derivable user → attach personal space if they
// have none; brand new → Register(create_personal). Active grants are applied
// before issuing tokens so pre-authorized team spaces materialize at login.
func (s *identityBridgeService) bridgePersonalSpace(
	ctx context.Context,
	req *types.IdentityBridgeRequest,
) (*types.LoginResponse, bool, error) {
	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = fmt.Sprintf(identityBridgeUsernameFmt, req.UUMUserID)
	}

	if mapping, err := s.repo.GetAnyMappingByUUM(ctx, req.UUMUserID); err == nil && mapping != nil {
		user, err := s.loadActiveUser(ctx, mapping.UserID)
		if err != nil {
			return nil, false, err
		}
		tenantID, err := s.ensurePersonalSpace(ctx, user, username)
		if err != nil {
			return nil, false, err
		}
		s.applySpaceGrants(ctx, req.UUMUserID, user.ID)
		resp, err := s.issueLogin(ctx, user, tenantID)
		if err != nil {
			return nil, false, err
		}
		logger.Infof(ctx, "Identity bridge personal-space login, uum_user_id: %s, user_id: %s, tenant_id: %d",
			req.UUMUserID, user.ID, tenantID)
		return resp, false, nil
	}

	email := strings.TrimSpace(req.Email)
	if email == "" {
		email = fmt.Sprintf(identityBridgeEmailFmt, req.UUMUserID)
	}
	user, err := s.userService.GetUserByUsername(ctx, username)
	if err != nil && !isUserLookupNotFound(err) {
		return nil, false, err
	}
	isNew := false
	if user == nil {
		password, perr := generateRandomString(32)
		if perr != nil {
			return nil, false, perr
		}
		// create_personal: one dedicated workspace per user, owner membership
		// bootstrapped by Register (user.go Register/EnsureOwner).
		user, err = s.userService.Register(ctx, &types.RegisterRequest{
			Username:          username,
			Email:             email,
			Password:          password,
			TenantProvisioning: types.TenantProvisioningCreatePersonal,
		})
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "already exists") {
				return nil, false, werrors.NewConflictError("用户名或邮箱已被占用")
			}
			return nil, false, err
		}
		isNew = true
	}
	if !user.IsActive {
		return nil, false, werrors.NewForbiddenError("用户已被禁用")
	}
	tenantID, err := s.ensurePersonalSpace(ctx, user, username)
	if err != nil {
		return nil, false, err
	}
	identity := &types.ExternalIdentity{
		ID:              uuid.New().String(),
		UUMUserID:       req.UUMUserID,
		UserID:          user.ID,
		TenantID:        tenantID,
		UUMDepartmentID: req.UUMDepartmentID,
		Source:          identityBridgeSource,
	}
	if err := s.repo.CreateMapping(ctx, identity); err != nil {
		if winner, rerr := s.repo.GetAnyMappingByUUM(ctx, req.UUMUserID); rerr == nil && winner != nil {
			identity = winner
		} else {
			return nil, false, err
		}
	}
	s.applySpaceGrants(ctx, req.UUMUserID, user.ID)
	resp, err := s.issueLogin(ctx, user, tenantID)
	if err != nil {
		return nil, false, err
	}
	logger.Infof(ctx, "Identity bridge personal-space provisioned, uum_user_id: %s, user_id: %s, tenant_id: %d, is_new: %t",
		req.UUMUserID, user.ID, tenantID, isNew)
	return resp, isNew, nil
}

// ensurePersonalSpace returns a tenant the user may log into, creating a
// personal space when they hold no active membership anywhere.
func (s *identityBridgeService) ensurePersonalSpace(ctx context.Context, user *types.User, username string) (uint64, error) {
	if user.TenantID != 0 {
		if member, err := s.memberService.GetMembership(ctx, user.ID, user.TenantID); err == nil &&
			member != nil && member.Status == types.TenantMemberStatusActive {
			return user.TenantID, nil
		}
	}
	if members, err := s.memberService.ListByUser(ctx, user.ID); err == nil {
		for _, m := range members {
			if m != nil && m.Status == types.TenantMemberStatusActive {
				return m.TenantID, nil
			}
		}
	}
	tenant, err := s.tenantService.CreateTenant(ctx, &types.Tenant{
		Name:        fmt.Sprintf("%s's Space", username),
		Description: "Personal space (auto-created at first portal login)",
		Status:      "active",
		SpaceType:   types.SpaceTypePersonal,
	})
	if err != nil {
		return 0, werrors.NewInternalServerError("创建个人空间失败").WithDetails(err.Error())
	}
	if _, err := s.memberService.EnsureOwner(ctx, user.ID, tenant.ID); err != nil {
		return 0, werrors.NewInternalServerError("个人空间归属失败").WithDetails(err.Error())
	}
	user.TenantID = tenant.ID
	if err := s.userService.UpdateUser(ctx, user); err != nil {
		logger.Warnf(ctx, "persist home tenant failed user=%s tenant=%d: %v", user.ID, tenant.ID, err)
	}
	logger.WarnWithFields(ctx, map[string]interface{}{
		"operator":  s.operatorForLog(ctx),
		"uum":       username,
		"user_id":   user.ID,
		"tenant_id": tenant.ID,
	}, "Personal space bootstrapped at bridge login")
	return tenant.ID, nil
}

// applySpaceGrants materializes effective v2 grants into memberships at
// login. Failures are non-fatal: the sweeper and next login re-attempt.
func (s *identityBridgeService) applySpaceGrants(ctx context.Context, uumUserID, userID string) {
	if s.grantService == nil {
		return
	}
	if err := s.grantService.ApplyForUser(ctx, uumUserID, userID); err != nil {
		logger.Warnf(ctx, "apply space grants failed uum=%s user=%s: %v", uumUserID, userID, err)
	}
}

func (s *identityBridgeService) operatorForLog(ctx context.Context) string {
	if userID, ok := types.UserIDFromContext(ctx); ok {
		return userID
	}
	return "api-principal"
}
