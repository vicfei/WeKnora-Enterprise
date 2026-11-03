package types

import "time"

// ExternalIdentity maps a UUM employee ID to a WeKnora user within one
// department tenant. Rows are hard-deleted on transfer (decision 020):
// history is kept in logs and audit trails, not in stacked mapping rows.
type ExternalIdentity struct {
	ID              string    `json:"id"                gorm:"type:varchar(36);primaryKey"`
	UUMUserID       string    `json:"uum_user_id"       gorm:"type:varchar(128);not null;uniqueIndex:uq_external_identities_uum_tenant,priority:1"`
	UserID          string    `json:"user_id"           gorm:"type:varchar(36);not null;index"`
	TenantID        uint64    `json:"tenant_id"         gorm:"not null;uniqueIndex:uq_external_identities_uum_tenant,priority:2"`
	UUMDepartmentID string    `json:"uum_department_id" gorm:"type:varchar(128);index"`
	Source          string    `json:"source"            gorm:"type:varchar(20);not null;default:'uum'"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (ExternalIdentity) TableName() string { return "external_identities" }

// UUMDepartment is the authoritative 1:1 mapping between a UUM department
// and the WeKnora tenant modeling that department. tenant_id is unique: two
// departments must never map to the same tenant.
type UUMDepartment struct {
	ID              string    `json:"id"                gorm:"type:varchar(36);primaryKey"`
	UUMDepartmentID string    `json:"uum_department_id" gorm:"type:varchar(128);not null;uniqueIndex"`
	TenantID        uint64    `json:"tenant_id"         gorm:"not null;uniqueIndex"`
	DepartmentName  string    `json:"department_name"   gorm:"type:varchar(255)"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (UUMDepartment) TableName() string { return "uum_departments" }

// IdentityBridgeRequest is the declarative payload of the bridge endpoint:
// "ensure this UUM user holds access to the department tenant". Repeated
// calls are idempotent. The provisioning event-ordering constraint
// (transfer/disable must be applied before any newer bridge call) is a
// caller-side contract, see decision 020.
type IdentityBridgeRequest struct {
	UUMUserID string `json:"uum_user_id" binding:"required"`
	// TenantID is optional since v2: omitted means tenantless personal-space
	// bootstrap (see identityBridgeService.bridgePersonalSpace). The service
	// validates; a binding:"required" here would reject the omit-case first.
	TenantID uint64 `json:"tenant_id"`
	Role     string `json:"role"` // optional; default viewer (least privilege)
	// Username is optional since the auto-provisioning enhancement: Java may
	// pass the employee's real name so the UI shows it instead of the UM id.
	// Default is the bare UM id (was "uum-{id}" before v3).
	Username string `json:"username"`
	// Email is optional: default is the synthetic "{uum_user_id}@{bridge
	// email domain}" (config auth.bridge_email_domain, default
	// pingan.com.cn) — a placeholder that satisfies the native unique-email
	// constraint without implying a real mailbox.
	Email           string `json:"email"`
	UUMDepartmentID string `json:"uum_department_id"` // optional snapshot / mapping upsert
}

// IdentityBridgeResult carries everything the bridge caller needs beyond the
// login pair. InitialPassword is non-empty ONLY on the call that provisioned
// the account (one-time reveal, long-term valid until the user rotates it);
// idempotent re-bridges and existing-user logins return it empty. The caller
// (Java portal) MUST mask it in logs and show it to the end user exactly once
// — audit-contract item, see docs/v3.1.3-um-audit-interfaces.md.
type IdentityBridgeResult struct {
	Login           *LoginResponse
	IsNewUser       bool
	InitialPassword string
}

// IdentityTransferRequest moves a UUM user between department tenants.
// Resources (KBs, documents, sessions) stay in the source tenant; the user
// simply loses membership there. Concurrent membership in several tenants
// ("兼任") is NOT handled here — use native tenant member management.
type IdentityTransferRequest struct {
	UUMUserID    string `json:"uum_user_id"     binding:"required"`
	FromTenantID uint64 `json:"from_tenant_id" binding:"required"`
	ToTenantID   uint64 `json:"to_tenant_id"   binding:"required"`
	Role         string `json:"role"` // optional; default preserves the current role
}

// IdentityDisableRequest disables a bridged user and revokes all tokens.
type IdentityDisableRequest struct {
	UUMUserID string `json:"uum_user_id" binding:"required"`
	TenantID  uint64 `json:"tenant_id"   binding:"required"`
}

// UUMDepartmentRequest binds a UUM department to a WeKnora department tenant.
type UUMDepartmentRequest struct {
	TenantID       uint64 `json:"tenant_id"       binding:"required"`
	DepartmentName string `json:"department_name"`
}
