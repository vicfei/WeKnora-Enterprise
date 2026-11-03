package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
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
	adminCreated    []*types.AdminCreateUserRequest
	provisioningOf  []types.TenantProvisioningMode
	generatedPass   []string
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

// AdminCreateUser mirrors the native one-time-password contract: absent
// password → generated and returned; the bridge passes no password.
func (s *stubBridgeUserService) AdminCreateUser(
	ctx context.Context, req *types.AdminCreateUserRequest, provisioning types.TenantProvisioningMode,
) (*types.User, string, error) {
	s.adminCreated = append(s.adminCreated, req)
	s.provisioningOf = append(s.provisioningOf, provisioning)
	generated := fmt.Sprintf("GenPass-%d", s.nextID+1)
	s.generatedPass = append(s.generatedPass, generated)
	s.nextID++
	user := &types.User{
		ID:       fmt.Sprintf("user-%d", s.nextID),
		Username: req.Username,
		Email:    req.Email,
		IsActive: true,
	}
	s.usersByUsername[user.Username] = user
	s.usersByID[user.ID] = user
	return user, generated, nil
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
	return newIdentityBridgeFixtureWithConfig(t, nil)
}

// newIdentityBridgeFixtureWithConfig lets a test pin auth config (e.g. the
// synthetic email domain); nil cfg exercises the default fallback.
func newIdentityBridgeFixtureWithConfig(t *testing.T, cfg *config.Config) *identityBridgeFixture {
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
	svc, err := NewIdentityBridgeService(bridgeRepo, users, memberService, tenants, tokens, nil, cfg)
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

	result, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	require.NoError(t, err)
	assert.True(t, result.IsNewUser)
	resp := result.Login
	assert.True(t, resp.Success)
	assert.Equal(t, "access-"+resp.User.ID, resp.Token)

	// Provisioning (v3): native AdminCreateUser, username = bare UM id,
	// synthetic email on the configured domain, viewer membership, mapping.
	require.Len(t, f.users.adminCreated, 1)
	assert.Equal(t, "EMP001", f.users.adminCreated[0].Username)
	assert.Equal(t, "EMP001@pingan.com.cn", f.users.adminCreated[0].Email)
	assert.Nil(t, f.users.adminCreated[0].Password, "bridge never supplies a password — the service generates one")
	assert.Equal(t, types.TenantProvisioningTenantless, f.users.provisioningOf[0])

	member := activeMembership(t, f.db, resp.User.ID, 10)
	require.NotNil(t, member)
	assert.Equal(t, types.TenantRoleViewer, member.Role)

	mapping, err := f.bridgeRepo.GetMappingByUUM(ctx, "EMP001", 10)
	require.NoError(t, err)
	assert.Equal(t, resp.User.ID, mapping.UserID)
}

func TestIdentityBridgeInitialPasswordOneTime(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	// The provisioning call reveals the initial password exactly once.
	first, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP010", TenantID: 10})
	require.NoError(t, err)
	assert.True(t, first.IsNewUser)
	assert.NotEmpty(t, first.InitialPassword, "provisioning call must return the generated password")
	assert.Equal(t, f.users.generatedPass[0], first.InitialPassword)

	// Idempotent re-bridge and existing-user login never re-reveal it.
	second, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP010", TenantID: 10})
	require.NoError(t, err)
	assert.False(t, second.IsNewUser)
	assert.Empty(t, second.InitialPassword, "re-bridge must not re-reveal the password")

	// Heal path (mapping lost, user alive) also stays silent.
	require.NoError(t, f.db.Where("uum_user_id = ?", "EMP010").Delete(&types.ExternalIdentity{}).Error)
	healed, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP010", TenantID: 10})
	require.NoError(t, err)
	assert.False(t, healed.IsNewUser)
	assert.Empty(t, healed.InitialPassword, "heal must not re-reveal the password")
}

func TestIdentityBridgeEmailDomainConfig(t *testing.T) {
	f := newIdentityBridgeFixtureWithConfig(t, &config.Config{
		Auth: &config.AuthConfig{BridgeEmailDomain: "example.corp"},
	})

	result, err := f.svc.Bridge(bridgeContext(), &types.IdentityBridgeRequest{UUMUserID: "EMP011", TenantID: 10})
	require.NoError(t, err)
	assert.True(t, result.IsNewUser)
	assert.True(t, strings.HasSuffix(f.users.adminCreated[0].Email, "@example.corp"),
		"synthetic email must honor the configured domain, got %s", f.users.adminCreated[0].Email)

	// Java-provided display name wins over the bare UM id.
	f2 := newIdentityBridgeFixture(t)
	res2, err := f2.svc.Bridge(bridgeContext(), &types.IdentityBridgeRequest{UUMUserID: "EMP012", Username: "陈飞", TenantID: 10})
	require.NoError(t, err)
	assert.True(t, res2.IsNewUser)
	assert.Equal(t, "陈飞", f2.users.adminCreated[0].Username)
	assert.Equal(t, "EMP012@pingan.com.cn", f2.users.adminCreated[0].Email, "email stays keyed to the UM id, not the display name")
}

func TestIdentityBridgeIdempotentRepush(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()
	req := &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10}

	first, err := f.svc.Bridge(ctx, req)
	require.NoError(t, err)
	assert.True(t, first.IsNewUser)

	second, err := f.svc.Bridge(ctx, req)
	require.NoError(t, err)
	assert.False(t, second.IsNewUser, "re-push must not provision again")
	assert.Equal(t, first.Login.User.ID, second.Login.User.ID)

	var mappingCount int64
	f.db.Model(&types.ExternalIdentity{}).Where("uum_user_id = ?", "EMP001").Count(&mappingCount)
	assert.EqualValues(t, 1, mappingCount, "re-push must not duplicate mapping rows")
	require.Len(t, f.users.adminCreated, 1)
}

func TestIdentityBridgeHealsMissingMapping(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	// User provisioned earlier (e.g. by a lost-event race) but the mapping
	// row is missing: bridge must re-attach instead of colliding.
	existing := &types.User{ID: "user-9", Username: "EMP002", Email: "EMP002@pingan.com.cn", IsActive: true}
	f.users.usersByUsername[existing.Username] = existing
	f.users.usersByID[existing.ID] = existing

	result, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP002", TenantID: 10})
	require.NoError(t, err)
	assert.False(t, result.IsNewUser, "heal path must not report a new user")
	assert.Empty(t, f.users.adminCreated, "heal path must not create accounts")
	assert.Equal(t, existing.ID, result.Login.User.ID)

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
	user := &types.User{ID: "user-7", Username: "EMP003", Email: "EMP003@pingan.com.cn", IsActive: true}
	f.users.usersByUsername[user.Username] = user
	f.users.usersByID[user.ID] = user
	require.NoError(t, f.bridgeRepo.CreateMapping(ctx, &types.ExternalIdentity{
		ID: "map-1", UUMUserID: "EMP003", UserID: user.ID, TenantID: 10, Source: "uum",
	}))

	_, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP003", TenantID: 10})
	requireAppErrorCode(t, err, werrors.ErrForbidden)

	// Disabled users never receive tokens either.
	user2 := &types.User{ID: "user-8", Username: "EMP004", Email: "EMP004@pingan.com.cn", IsActive: false}
	f.users.usersByUsername[user2.Username] = user2
	f.users.usersByID[user2.ID] = user2
	require.NoError(t, f.bridgeRepo.CreateMapping(ctx, &types.ExternalIdentity{
		ID: "map-2", UUMUserID: "EMP004", UserID: user2.ID, TenantID: 10, Source: "uum",
	}))
	_, err = f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP004", TenantID: 10})
	requireAppErrorCode(t, err, werrors.ErrForbidden)
}

func TestIdentityBridgeDisableRevokesTokens(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	result, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	require.NoError(t, err)

	require.NoError(t, f.svc.Disable(ctx, "EMP001", 10))
	assert.False(t, f.users.usersByID[result.Login.User.ID].IsActive, "user must be deactivated")
	assert.Equal(t, []string{result.Login.User.ID}, f.tokens.revoked, "all tokens must be revoked")

	// Mapping is kept for audit/re-enable bookkeeping; further bridges fail.
	_, err = f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	requireAppErrorCode(t, err, werrors.ErrForbidden)
}

func TestIdentityBridgeTransferMovesMembershipAndMapping(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	result, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "EMP001", TenantID: 10})
	require.NoError(t, err)
	userID := result.Login.User.ID

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

// --- v2/v3 personal-space bootstrap (tenantless bridge) ------------------

func TestBridgePersonalSpaceBootstrap(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	ctx := bridgeContext()

	// brand new UUM user, no tenant_id: personal space created (native
	// create_personal), mapping row points at it, login issued for it, and
	// the initial password is revealed exactly once.
	result, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "V2-P1"})
	require.NoError(t, err)
	assert.True(t, result.IsNewUser)
	assert.NotEmpty(t, result.InitialPassword, "personal-space provisioning must reveal the initial password")
	resp := result.Login
	assert.NotEmpty(t, resp.Token)
	require.NotNil(t, resp.ActiveTenant)
	personalTenant := resp.ActiveTenant.ID
	assert.NotZero(t, personalTenant)
	assert.Equal(t, types.TenantProvisioningCreatePersonal, f.users.provisioningOf[0])
	assert.Equal(t, "V2-P1@pingan.com.cn", f.users.adminCreated[0].Email)

	mapping, err := f.bridgeRepo.GetAnyMappingByUUM(ctx, "V2-P1")
	require.NoError(t, err)
	assert.Equal(t, "V2-P1", mapping.UUMUserID)

	// idempotent re-bridge: login only, no duplicate account, no re-reveal.
	result2, err := f.svc.Bridge(ctx, &types.IdentityBridgeRequest{UUMUserID: "V2-P1"})
	require.NoError(t, err)
	assert.False(t, result2.IsNewUser)
	assert.Empty(t, result2.InitialPassword)
	assert.NotEmpty(t, result2.Login.Token)
}

func TestBridgeTenantlessBadPayload(t *testing.T) {
	f := newIdentityBridgeFixture(t)
	_, err := f.svc.Bridge(bridgeContext(), &types.IdentityBridgeRequest{UUMUserID: " "})
	assert.Error(t, err, "empty uum id must be rejected even in tenantless mode")
}
