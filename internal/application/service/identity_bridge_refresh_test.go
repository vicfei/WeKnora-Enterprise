package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestIdentityBridgeReBridgeKeepsExistingTokensAlive pins the security
// contract that an idempotent re-bridge (e.g. the portal backend re-pushing
// a user while they are online) never logs the user out:
//
//  1. SwitchTenant only revokes the refresh token PRESENTED in the request
//     (user.go:1043 — empty means nothing to revoke) and the bridge always
//     presents an empty one;
//  2. RevokeToken is per-token by full value (user.go:1240), never
//     per-user.
//
// The test runs the REAL user service over sqlite to prove the full chain
// (JWT signing, auth_tokens ledger, refresh rotation) instead of stubbing
// it away.
func TestIdentityBridgeReBridgeKeepsExistingTokensAlive(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.User{},
		&types.AuthToken{},
		&types.TenantMember{},
		&types.ExternalIdentity{},
		&types.UUMDepartment{},
	))

	tenants := &stubBridgeTenantService{tenants: map[uint64]*types.Tenant{
		10: {ID: 10, Name: "研发部"},
	}}
	userService := NewUserService(
		&config.Config{},
		repository.NewUserRepository(db),
		repository.NewAuthTokenRepository(db),
		tenants,
		NewTenantMemberService(repository.NewTenantMemberRepository(db), nil, nil, nil),
		nil, // systemSettingSvc: complex-password policy falls back to config when nil
	)
	bridgeSvc, err := NewIdentityBridgeService(
		repository.NewIdentityBridgeRepository(db),
		userService,
		NewTenantMemberService(repository.NewTenantMemberRepository(db), nil, nil, nil),
		tenants,
		repository.NewAuthTokenRepository(db),
		nil,
		&config.Config{},
	)
	require.NoError(t, err)

	ctx := bridgeContext()
	req := &types.IdentityBridgeRequest{UUMUserID: "EMP100", TenantID: 10}

	first, err := bridgeSvc.Bridge(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, first.Login.Token)
	require.NotEmpty(t, first.Login.RefreshToken)

	// v3 自动供给：首启调用下发初始密码（一次性），后续不再下发。
	require.True(t, first.IsNewUser)
	require.NotEmpty(t, first.InitialPassword, "provisioning call must reveal the initial password")

	// 双通道验收：bridge 建的号 = 原生账号，初始密码可走前端账密登录。
	nativeLogin, err := userService.Login(ctx, &types.LoginRequest{
		Email:    "EMP100@pingan.com.cn",
		Password: first.InitialPassword,
	})
	require.NoError(t, err, "the bridge-issued initial password must work for native email login (双通道)")
	require.NotEmpty(t, nativeLogin.Token)

	time.Sleep(1100 * time.Millisecond)
	second, err := bridgeSvc.Bridge(ctx, req)
	require.NoError(t, err)
	require.False(t, second.IsNewUser)
	require.Empty(t, second.InitialPassword, "re-bridge must not re-reveal the password")
	require.NotEmpty(t, second.Login.RefreshToken)
	require.NotEqual(t, first.Login.RefreshToken, second.Login.RefreshToken, "a fresh pair must be minted")

	// Ledger immediately after the re-bridge: two bridge logins + the native
	// email login (双通道验收) minted three pairs (6 rows) and the re-bridge
	// itself revoked NOTHING.
	var total, revoked int64
	db.Model(&types.AuthToken{}).Count(&total)
	db.Model(&types.AuthToken{}).Where("is_revoked = ?", true).Count(&revoked)
	assert.EqualValues(t, 6, total, "two bridge logins + one native login = 3 access + 3 refresh rows")
	assert.EqualValues(t, 0, revoked, "no token may be revoked by a re-bridge")

	// The first pair must survive: its refresh token still rotates and its
	// access token still validates. (Rotation revokes first.Login.RefreshToken
	// afterwards — that is native refresh semantics, not bridge damage.)
	rotatedAccess, rotatedRefresh, err := userService.RefreshToken(ctx, first.Login.RefreshToken)
	require.NoError(t, err, "re-bridge must not revoke the refresh token in use")
	assert.NotEmpty(t, rotatedAccess)
	assert.NotEmpty(t, rotatedRefresh)

	validatedUser, _, err := userService.ValidateToken(ctx, first.Login.Token)
	require.NoError(t, err, "re-bridge must not invalidate the access token in use")
	assert.Equal(t, first.Login.User.ID, validatedUser.ID)
}
