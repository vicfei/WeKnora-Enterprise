package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// --- stubs ---------------------------------------------------------------

type stubGrantMemberService struct {
	interfaces.TenantMemberService
	members map[string]*types.TenantMember // key: userID|tenantID
	nextID  int
}

func (s *stubGrantMemberService) key(userID string, tenantID uint64) string {
	return fmt.Sprintf("%s|%d", userID, tenantID)
}

func (s *stubGrantMemberService) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	if m, ok := s.members[s.key(userID, tenantID)]; ok {
		return m, nil
	}
	return nil, nil
}

func (s *stubGrantMemberService) AddMember(_ context.Context, userID string, tenantID uint64, role types.TenantRole, _ *string) (*types.TenantMember, error) {
	s.nextID++
	m := &types.TenantMember{ID: uint64(s.nextID), UserID: userID, TenantID: tenantID, Role: role, Status: types.TenantMemberStatusActive}
	s.members[s.key(userID, tenantID)] = m
	return m, nil
}

func (s *stubGrantMemberService) UpdateRole(_ context.Context, userID string, tenantID uint64, role types.TenantRole) error {
	if m, ok := s.members[s.key(userID, tenantID)]; ok {
		m.Role = role
		return nil
	}
	return fmt.Errorf("no member")
}

func (s *stubGrantMemberService) RemoveMember(_ context.Context, userID string, tenantID uint64) error {
	delete(s.members, s.key(userID, tenantID))
	return nil
}

type stubGrantTenantService struct {
	interfaces.TenantService
	tenants map[uint64]*types.Tenant
}

func (s *stubGrantTenantService) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if t, ok := s.tenants[id]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("not found")
}

// --- fixture -------------------------------------------------------------

func newGrantService(t *testing.T) (interfaces.UserSpaceGrantService, *stubGrantMemberService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys=1"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.UserSpaceGrant{}, &types.ExternalIdentity{}))

	// clear shared-cache leftovers from other tests in this package
	db.Exec("DELETE FROM user_space_grants")
	db.Exec("DELETE FROM external_identities")

	repo := repository.NewUserSpaceGrantRepository(db)
	members := &stubGrantMemberService{members: map[string]*types.TenantMember{}}
	tenants := &stubGrantTenantService{tenants: map[uint64]*types.Tenant{
		100: {ID: 100, Name: "投研团队"},
		200: {ID: 200, Name: "公共空间"},
	}}
	svc, err := NewUserSpaceGrantService(repo, members, tenants)
	require.NoError(t, err)
	// expose the repo-backed resolver by seeding an identity mapping row
	t.Cleanup(func() { db.Exec("DELETE FROM user_space_grants"); db.Exec("DELETE FROM external_identities") })
	return svc, members
}

func mapUser(t *testing.T, uum, userID string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Create(&types.ExternalIdentity{
		ID: "id-" + uum, UUMUserID: uum, UserID: userID, TenantID: 100, Source: "uum",
	}).Error)
}

// --- tests ---------------------------------------------------------------

func TestGrantLifecyclePendingApproveRevoke(t *testing.T) {
	svc, members := newGrantService(t)
	ctx := context.Background()
	mapUser(t, "U1", "user-1")

	// create as pending: no membership yet
	grant, err := svc.Create(ctx, &interfaces.UserSpaceGrantAdminRequest{
		UUMUserID: "U1", TenantID: 100, Role: types.TenantRoleContributor,
	}, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, types.UserSpaceGrantPending, grant.Status)
	assert.Empty(t, members.members)

	// approve: membership materialized with the grant role
	grant, err = svc.Approve(ctx, grant.ID, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, types.UserSpaceGrantActive, grant.Status)
	m := members.members["user-1|100"]
	require.NotNil(t, m, "membership must exist after approve")
	assert.Equal(t, types.TenantRoleContributor, m.Role)

	// role update propagates to membership
	_, err = svc.UpdateRole(ctx, grant.ID, types.TenantRoleViewer, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, types.TenantRoleViewer, members.members["user-1|100"].Role)

	// revoke: membership dropped immediately (next request 403s)
	require.NoError(t, svc.Revoke(ctx, grant.ID, "admin-1"))
	assert.Empty(t, members.members, "membership must be dropped on revoke")
	got, err := svc.Get(ctx, grant.ID)
	require.NoError(t, err)
	assert.Equal(t, types.UserSpaceGrantRevoked, got.Status)
}

func TestGrantPreAuthorization(t *testing.T) {
	svc, members := newGrantService(t)
	ctx := context.Background()
	// NOTE: no mapUser — the WeKnora user does not exist yet

	grant, err := svc.Create(ctx, &interfaces.UserSpaceGrantAdminRequest{
		UUMUserID: "U2", TenantID: 200, Role: types.TenantRoleViewer, ActivateNow: true,
	}, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, types.UserSpaceGrantActive, grant.Status)
	assert.Empty(t, members.members, "no membership before user exists")

	// first login: mapping appears, ApplyForUser materializes
	mapUser(t, "U2", "user-2")
	require.NoError(t, svc.ApplyForUser(ctx, "U2", "user-2"))
	assert.NotNil(t, members.members["user-2|200"])
}

func TestGrantExpirySweep(t *testing.T) {
	svc, members := newGrantService(t)
	ctx := context.Background()
	mapUser(t, "U3", "user-3")
	past := time.Now().Add(-time.Hour)

	grant, err := svc.Create(ctx, &interfaces.UserSpaceGrantAdminRequest{
		UUMUserID: "U3", TenantID: 100, Role: types.TenantRoleViewer,
		ValidUntil: &past, ActivateNow: true,
	}, "admin-1")
	require.NoError(t, err)
	assert.NotNil(t, members.members["user-3|100"], "not yet swept: membership kept")

	stats, err := svc.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, stats.Expired)
	assert.Equal(t, 1, stats.MembershipDropped)
	assert.Empty(t, members.members)
	got, _ := svc.Get(ctx, grant.ID)
	assert.Equal(t, types.UserSpaceGrantExpired, got.Status)
}

func TestGrantSweepReconcilesDivergence(t *testing.T) {
	svc, members := newGrantService(t)
	ctx := context.Background()
	mapUser(t, "U4", "user-4")

	// active grant but membership lost (simulated partial failure)
	grant, err := svc.Create(ctx, &interfaces.UserSpaceGrantAdminRequest{
		UUMUserID: "U4", TenantID: 100, Role: types.TenantRoleAdmin, ActivateNow: true,
	}, "admin-1")
	require.NoError(t, err)
	require.NotNil(t, members.members["user-4|100"])
	delete(members.members, "user-4|100")

	stats, err := svc.Sweep(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, stats.MembershipAdded, "sweep must re-add lost membership")
	assert.NotNil(t, members.members["user-4|100"])
	_ = grant
}

func TestGrantRejectsOwnerRoleAndMissingTenant(t *testing.T) {
	svc, _ := newGrantService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, &interfaces.UserSpaceGrantAdminRequest{
		UUMUserID: "U5", TenantID: 100, Role: types.TenantRoleOwner,
	}, "admin-1")
	assert.Error(t, err, "owner must never be granted to persons (governance freeze)")

	_, err = svc.Create(ctx, &interfaces.UserSpaceGrantAdminRequest{
		UUMUserID: "U5", TenantID: 999, Role: types.TenantRoleViewer,
	}, "admin-1")
	assert.Error(t, err, "unknown tenant must be rejected")
}
