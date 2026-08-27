package types

import "time"

// UserSpaceGrantStatus enumerates the lifecycle of a grant row.
//
//	pending  — recorded but not effective yet (pre-authorization /
//	           awaiting approval). No membership is created.
//	active   — effective; the linked tenant_members row exists.
//	expired  — validity window passed (materialized by the sweeper;
//	           readers also treat overdue valid_until defensively).
//	revoked  — manually withdrawn by an admin.
type UserSpaceGrantStatus string

const (
	UserSpaceGrantPending UserSpaceGrantStatus = "pending"
	UserSpaceGrantActive  UserSpaceGrantStatus = "active"
	UserSpaceGrantExpired UserSpaceGrantStatus = "expired"
	UserSpaceGrantRevoked UserSpaceGrantStatus = "revoked"
)

func (s UserSpaceGrantStatus) IsValid() bool {
	switch s {
	case UserSpaceGrantPending, UserSpaceGrantActive, UserSpaceGrantExpired, UserSpaceGrantRevoked:
		return true
	}
	return false
}

// UserSpaceGrant is the v2 space-permission record: who (by UUM id — the
// row may exist BEFORE the WeKnora user is provisioned, which is exactly
// the pre-authorization gap the review identified, assertion 8) gets
// which role in which space (tenant). Effective state is projected into
// the native tenant_members table within the same transaction as the
// status transition (see service.UserSpaceGrantService).
type UserSpaceGrant struct {
	ID          string               `json:"id"                   gorm:"type:varchar(36);primaryKey"`
	UUMUserID   string               `json:"uum_user_id"          gorm:"type:varchar(128);not null;uniqueIndex:uq_user_space_grants_uum_tenant,priority:1"`
	TenantID    uint64               `json:"tenant_id"            gorm:"not null;uniqueIndex:uq_user_space_grants_uum_tenant,priority:2"`
	Role        TenantRole           `json:"role"                 gorm:"type:varchar(20);not null;default:'viewer'"`
	Status      UserSpaceGrantStatus `json:"status"               gorm:"type:varchar(20);not null;default:'pending'"`
	ValidUntil  *time.Time           `json:"valid_until,omitempty"`
	RequestedBy string               `json:"requested_by"         gorm:"type:varchar(128);not null;default:''"`
	ApprovedBy  string               `json:"approved_by,omitempty" gorm:"type:varchar(128)"`
	Reason      string               `json:"reason,omitempty"     gorm:"type:varchar(255)"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

func (UserSpaceGrant) TableName() string { return "user_space_grants" }

// Effective reports whether the grant currently grants access. A row that
// is active but past its validity window is defensively treated as not
// effective even before the sweeper materializes the expired status.
func (g *UserSpaceGrant) Effective(now time.Time) bool {
	return g.Status == UserSpaceGrantActive &&
		(g.ValidUntil == nil || g.ValidUntil.After(now))
}

// Grant-space role policy (v2 plan §五): real persons never receive owner
// on team/public spaces — member changes flow exclusively through grants.
func ValidGrantRoles() []TenantRole {
	return []TenantRole{TenantRoleAdmin, TenantRoleContributor, TenantRoleViewer}
}

// SpaceType classifies a tenant for the portal aggregation (v2 plan §二):
// personal (one per user), team, all (company-wide public space).
const (
	SpaceTypePersonal = "personal"
	SpaceTypeTeam     = "team"
	SpaceTypeAll      = "all"
)

func IsValidSpaceType(s string) bool {
	return s == SpaceTypePersonal || s == SpaceTypeTeam || s == SpaceTypeAll
}

// UserSpaceGrantFilter narrows grant list queries; zero values mean "no filter".
type UserSpaceGrantFilter struct {
	TenantID uint64
	UUMUser  string
	Status   UserSpaceGrantStatus
}
