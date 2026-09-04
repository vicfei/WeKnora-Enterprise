-- Description: UUM identity bridge for secondary development (phase 2).
-- external_identities maps UUM employee IDs to WeKnora users per department
-- tenant; uum_departments is the authoritative mapping between a UUM
-- department and the WeKnora tenant modeling that department. One department
-- = one tenant is a bidirectional 1:1 assumption: tenant_id is UNIQUE so two
-- departments can never map to the same tenant. Mapping rows are
-- hard-deleted on transfer (decision 020): history lives in logs, not in
-- stacked rows. No foreign keys by design (decision 015); existence is
-- validated in the service layer.
DO $$ BEGIN RAISE NOTICE '[Migration 000085] Creating external_identities and uum_departments'; END $$;

CREATE TABLE IF NOT EXISTS external_identities (
    id                VARCHAR(36)  PRIMARY KEY,
    uum_user_id       VARCHAR(128) NOT NULL,
    user_id           VARCHAR(36)  NOT NULL,
    tenant_id         BIGINT       NOT NULL,
    uum_department_id VARCHAR(128),
    source            VARCHAR(20)  NOT NULL DEFAULT 'uum',
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE external_identities IS 'UUM identity bridge: uum_user_id -> WeKnora user, scoped per department tenant';
COMMENT ON COLUMN external_identities.tenant_id IS 'Department tenant the mapping was provisioned for';
COMMENT ON COLUMN external_identities.uum_department_id IS 'Snapshot of the UUM department ID at provisioning time (reconciliation aid; authoritative mapping lives in uum_departments)';

CREATE UNIQUE INDEX IF NOT EXISTS uq_external_identities_uum_tenant
    ON external_identities (uum_user_id, tenant_id);
CREATE INDEX IF NOT EXISTS idx_external_identities_user
    ON external_identities (user_id);
CREATE INDEX IF NOT EXISTS idx_external_identities_department
    ON external_identities (uum_department_id);

CREATE TABLE IF NOT EXISTS uum_departments (
    id                VARCHAR(36)  PRIMARY KEY,
    uum_department_id VARCHAR(128) NOT NULL,
    tenant_id         BIGINT       NOT NULL,
    department_name   VARCHAR(255),
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE uum_departments IS 'Authoritative 1:1 mapping between a UUM department and the WeKnora department tenant';

CREATE UNIQUE INDEX IF NOT EXISTS uq_uum_departments_uum_id
    ON uum_departments (uum_department_id);
-- One WeKnora tenant models exactly one department; a second department
-- mapped to the same tenant would make the permission model ambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS uq_uum_departments_tenant
    ON uum_departments (tenant_id);
