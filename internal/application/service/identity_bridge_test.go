package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- stubs ---------------------------------------------------------------

type stubBridgeUserService struct {
	interfaces.UserService
	usersByUsername map[string]*types.User
	usersByID       map[string]*types.User
	registered      []*types.RegisterRequest
	switchTenantFor []*types.User
	nextID          int
}

func (s *stubBridgeUserService) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	if user, ok := s.usersByUsername[username]; ok {
		return user, nil
	}
	return nil, repository.ErrUserNotFound
}

func (s *stubBridgeUserService) GetUserByID(ctx context.Context, id string) (*types.User, error) {
	if user, ok := s.usersByID[id]; ok {
		return user, nil
	}
	return nil, repository.ErrUserNotFound
}

func (s *stubBridgeUserService) Register(ctx context.Context, req *types.RegisterRequest) (*types.User, error) {
	s.registered = append(s.registered, req)
	s.nextID++
	user := &types.User{
		ID:       fmt.Sprintf("user-%d", s.nextID),
		Username: req.Username,
		Email:    req.Email,
		IsActive: true,
	}
	s.usersByUsername[user.Username] = user
	s.usersByID[user.ID] = user
	return user, nil
}

func (s *stubBridgeUserService) UpdateUser(ctx context.Context, user *types.User) error {
	s.usersByID[user.ID] = user
	s.usersByUsername[user.Username] = user
	return nil
}

func (s *stubBridgeUserService) SwitchTenant(
	ctx context.Context, user *types.User, targetTenantID uint64, currentRefreshToken string,
) (*types.LoginResponse, error) {
	s.switchTenantFor = append(s.switchTenantFor, user)
	return &types.LoginResponse{
		Success:      true,
		User:         user,
		ActiveTenant: &types.Tenant{ID: targetTenantID},
		Token:        "access-" + user.ID,
		RefreshToken: "refresh-" + user.ID,
	}, nil
}

type stubBridgeTenantService struct {
	interfaces.TenantService
	tenants      map[uint64]*types.Tenant
	nextTenantID int
}

func (s *stubBridgeTenantService) CreateTenant(ctx context.Context, tenant *types.Tenant) (*types.Tenant, error) {
	s.nextTenantID++
	tenant.ID = uint64(s.nextTenantID)
	tenant.SpaceType = types.SpaceTypePersonal
	s.tenants[tenant.ID] = tenant
	return tenant, nil
}

func (s *stubBridgeTenantService) GetTenantByID(ctx context.Context, id uint64) (*types.Tenant, error) {
	if tenant, ok := s.tenants[id]; ok {
		return tenant, nil
	}
	return nil, errors.New("tenant not found")
}

type stubBridgeTokenRepo struct {
	interfaces.AuthTokenRepository
	revoked []string
}

func (s *stubBridgeTokenRepo) RevokeTokensByUserID(ctx context.Context, userID string) error {
	s.revoked = append(s.revoked, userID)
	return nil
}

// --- scaffold ------------------------------------------------------------

type identityBridgeFixture struct {
	svc        interfaces.IdentityBridgeService
	users      *stubBridgeUserService
	tokens     *stubBridgeTokenRepo
	db         *gorm.DB
	bridgeRepo interfaces.IdentityBridgeRepository
}

func newIdentityBridgeFixture(t *testing.T) *identityBridgeFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.ExternalIdentity{},
		&types.UUMDepartment{},
		&types.User{},
		&types.TenantMember{},
	))

	users := &stubBridgeUserService{
		usersByUsername: map[string]*types.User{},
		usersByID:       map[string]*types.User{},
	}
	tenants := &stubBridgeTenantService{tenants: map[uint64]*types.Tenant{
		10: {ID: 10, Name: "研发部"},
		20: {ID: 20, Name: "市场部"},
	}}
	tokens := &stubBridgeTokenRepo{}
	bridgeRepo := repository.NewIdentityBridgeRepository(db)
	memberService := NewTenantMemberService(repository.NewTenantMemberRepository(db), nil, nil, nil)
	svc, err := NewIdentityBridgeService(bridgeRepo, users, memberService, tenants, tokens, nil)
	require.NoError(t, err)

	return &identityBridgeFixture{svc: svc, users: users, tokens: tokens, db: db, bridgeRepo: bridgeRepo}
}

func bridgeContext() context.Context {
	return context.WithValue(context.Background(), types.TenantIDContextKey, uint64(99))
}

func activeMembership(t *testing.T, db *gorm.DB, userID string, tenantID uint64) *types.TenantMember {
	t.Helper()
	var member types.TenantMember
	err := db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&member).Error
	if err != nil {
		return nil
	}
	return &member
}

// --- tests ---------------------------------------------------------------

func TestIdentityBridgeProvisionsNewUser(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	resp, isNew, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	require.NoError(t, err)
	assert.True(t, isNew)
	assert.True(t, resp.Success)
	assert.Equal(t, "access-"+resp.User.ID, resp.Token)

	// Provisioning: tenantless register, derived username/email, viewer
	// membership, mapping row.
	require.Len(t, f.users.registered, 1)
	assert.Equal(t, "uum-EMP001", f.users.registered[0].Username)
	assert.Equal(t, "EMP001@uum.invalid", f.users.registered[0].Email)
	assert.Equal(t, types.TenantProvisioningTenantless, f.users.registered[0].TenantProvisioning)

	member := activeMembership(t, f.db, resp.User.ID, 10)
	require.NotNil(t, member)
	assert.Equal(t, types.TenantRoleViewer, member.Role)

	mapping, err := f.bridgeRepo.GetMappingByUUM(ctx, "EMP001", 10)
	require.NoError(t, err)
	assert.Equal(t, resp.User.ID, mapping.UserID)
}

func TestIdentityBridgeIdempotentRepush(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()
	req := &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10}

	first, isNew, err := f.svc.Bridge(ctx, req)
	require.NoError(t, err)
	assert.True(t, isNew)

	second, isNew, err := f.svc.Bridge(ctx, req)
	require.NoError(t, err)
	assert.False(t, isNew, "re-push must not provision again")
	assert.Equal(t, first.User.ID, second.User.ID)

	var mappingCount int64
	f.db.Model(&types.ExternalIdentity{}).Where("uum_user_id = ?", "EMP001").Count(&mappingCount)
	assert.EqualValues(t, 1, mappingCount, "re-push must not duplicate mapping rows")
	require.Len(t, f.users.registered, 1)
}

func TestIdentityBridgeHealsMissingMapping(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	// User provisioned earlier (e.g. by a lost-event race) but the mapping
	// row is missing: bridge must re-attach instead of colliding.
	existing := &types.User{ID: "user-9", Username: "uum-EMP002", Email: "EMP002@uum.invalid", IsActive: true}
	f.users.usersByUsername[existing.Username] = existing
	f.users.usersByID[existing.ID] = existing

	resp, isNew, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP002", TenantID: 10})
	require.NoError(t, err)
	assert.False(t, isNew, "heal path must not report a new user")
	assert.Empty(t, f.users.registered, "heal path must not call Register")
	assert.Equal(t, existing.ID, resp.User.ID)

	assert.NotNil(t, activeMembership(t, f.db, existing.ID, 10), "heal must add the department membership")
	mapping, err := f.bridgeRepo.GetMappingByUUM(ctx, "EMP002", 10)
	require.NoError(t, err)
	assert.Equal(t, existing.ID, mapping.UserID)
}

func TestIdentityBridgeStaleMappingRejected(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	// Mapping exists but the user holds no active membership anymore
	// (transferred away, event not yet pushed by the caller).
	user := &types.User{ID: "user-7", Username: "uum-EMP003", Email: "EMP003@uum.invalid", IsActive: true}
	f.users.usersByUsername[user.Username] = user
	f.users.usersByID[user.ID] = user
	require.NoError(t, f.bridgeRepo.CreateMapping(ctx, &types.ExternalIdentity{
		ID: "map-1", UUMUserID: "EMP003", UserID: user.ID, TenantID: 10, Source: "uum",
	}))

	_, _, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP003", TenantID: 10})
	requireAppErrorCode(t, err, werrors.ErrForbidden)

	// Disabled users never receive tokens either.
	user2 := &types.User{ID: "user-8", Username: "uum-EMP004", Email: "EMP004@uum.invalid", IsActive: false}
	f.users.usersByUsername[user2.Username] = user2
	f.users.usersByID[user2.ID] = user2
	require.NoError(t, f.bridgeRepo.CreateMapping(ctx, &types.ExternalIdentity{
		ID: "map-2", UUMUserID: "EMP004", UserID: user2.ID, TenantID: 10, Source: "uum",
	}))
	_, _, err = f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP004", TenantID: 10})
	requireAppErrorCode(t, err, werrors.ErrForbidden)
}

func TestIdentityBridgeDisableRevokesTokens(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	resp, _, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	require.NoError(t, err)

	require.NoError(t, f.svc.Disable(ctx, "EMP001", 10))
	assert.False(t, f.users.usersByID[resp.User.ID].IsActive, "user must be deactivated")
	assert.Equal(t, []string{resp.User.ID}, f.tokens.revoked, "all tokens must be revoked")

	// Mapping is kept for audit/re-enable bookkeeping; further bridges fail.
	_, _, err = f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	requireAppErrorCode(t, err, werrors.ErrForbidden)
}

func TestIdentityBridgeTransferMovesMembershipAndMapping(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	resp, _, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	require.NoError(t, err)
	userID := resp.User.ID

	mapping, err := f.svc.Transfer(ctx, &types.IdentityTransferRequest{
		UUMUserID: "EMP001", FromTenantID: 10, ToTenantID: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(20), mapping.TenantID)
	assert.Equal(t, userID, mapping.UserID)

	// Old mapping row deleted (never stacked as history), new row written.
	var fromRows, toRows int64
	f.db.Model(&types.ExternalIdentity{}).Where("uum_user_id = ? AND tenant_id = ?", "EMP001", 10).Count(&fromRows)
	f.db.Model(&types.ExternalIdentity{}).Where("uum_user_id = ? AND tenant_id = ?", "EMP001", 20).Count(&toRows)
	assert.EqualValues(t, 0, fromRows)
	assert.EqualValues(t, 1, toRows)

	// Membership moved with the role preserved (viewer default here).
	assert.Nil(t, activeMembership(t, f.db, userID, 10))
	member := activeMembership(t, f.db, userID, 20)
	require.NotNil(t, member)
	assert.Equal(t, types.TenantRoleViewer, member.Role)

	// Tokens revoked (old JWT carries the stale department claim).
	assert.Equal(t, []string{userID}, f.tokens.revoked)

	// Idempotent re-push of the same transfer returns the current mapping.
	again, err := f.svc.Transfer(ctx, &types.IdentityTransferRequest{
		UUMUserID: "EMP001", FromTenantID: 10, ToTenantID: 20,
	})
	require.NoError(t, err)
	assert.Equal(t, mapping.ID, again.ID)
}

func TestIdentityBridgeDepartmentUpsertAndConflict(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	dept, err := f.svc.UpsertDepartment(ctx, "DEPT-A", &types.UUMDepartmentRequest{TenantID: 10, DepartmentName: "研发部"})
	require.NoError(t, err)
	assert.Equal(t, uint64(10), dept.TenantID)

	// Same pair refreshes the name.
	dept, err = f.svc.UpsertDepartment(ctx, "DEPT-A", &types.UUMDepartmentRequest{TenantID: 10, DepartmentName: "研发一部"})
	require.NoError(t, err)
	assert.Equal(t, "研发一部", dept.DepartmentName)

	// A second department may not bind to the same tenant (1:1 both ways).
	_, err = f.svc.UpsertDepartment(ctx, "DEPT-B", &types.UUMDepartmentRequest{TenantID: 10})
	requireAppErrorCode(t, err, werrors.ErrConflict)

	// The same department may not rebind to another tenant.
	_, err = f.svc.UpsertDepartment(ctx, "DEPT-A", &types.UUMDepartmentRequest{TenantID: 20})
	requireAppErrorCode(t, err, werrors.ErrConflict)

	// Unknown tenant is rejected.
	_, err = f.svc.UpsertDepartment(ctx, "DEPT-C", &types.UUMDepartmentRequest{TenantID: 404})
	requireAppErrorCode(t, err, werrors.ErrNotFound)
}

// --- v2 personal-space bootstrap (tenantless bridge) ---------------------

func TestBridgePersonalSpaceBootstrap(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	// brand new UUM user, no tenant_id: personal space created, mapping row
	// points at it, login response issued for that tenant.
	resp, isNew, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "V2-P1"})
	require.NoError(t, err)
	assert.True(t, isNew)
	assert.NotEmpty(t, resp.Token)
	require.NotNil(t, resp.ActiveTenant)
	personalTenant := resp.ActiveTenant.ID
	assert.NotZero(t, personalTenant)

	mapping, err := f.bridgeRepo.GetAnyMappingByUUM(ctx, "V2-P1")
	require.NoError(t, err)
	assert.Equal(t, "V2-P1", mapping.UUMUserID)

	// idempotent re-bridge: login only, no duplicate account
	resp2, isNew2, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "V2-P1"})
	require.NoError(t, err)
	assert.False(t, isNew2)
	assert.NotEmpty(t, resp2.Token)
}

func TestBridgeTenantlessBadPayload(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	_, _, err := f.svc.Bridge(bridgeContext(), &types.IdentityBridgeRequest{UUMUserID: " "})
	assert.Error(t, err, "empty uum id must be rejected even in tenantless mode")
}
