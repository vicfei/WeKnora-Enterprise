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
	UUMUserID       string `json:"uum_user_id"       binding:"required"`
	TenantID        uint64 `json:"tenant_id"         binding:"required"`
	Role            string `json:"role"`             // optional; default viewer (least privilege)
	Username        string `json:"username"`         // optional; default derived "uum-{uum_user_id}"
	Email           string `json:"email"`            // optional; default derived "{uum_user_id}@uum.invalid"
	UUMDepartmentID string `json:"uum_department_id"` // optional snapshot / mapping upsert
}

// IdentityTransferRequest moves a UUM user between department tenants.
// Resources (KBs, documents, sessions) stay in the source tenant; the user
// simply loses membership there. Concurrent membership in several tenants
// ("兼任") is NOT handled here — use native tenant member management.
type IdentityTransferRequest struct {
	UUMUserID    string `json:"uum_user_id"     binding:"required"`
	FromTenantID uint64 `json:"from_tenant_id" binding:"required"`
	ToTenantID   uint64 `json:"to_tenant_id"   binding:"required"`
	Role         string `json:"role"`           // optional; default preserves the current role
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
