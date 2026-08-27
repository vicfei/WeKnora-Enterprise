-- v2 空间权限（决策记录见 .context/design/java_weknora_division_plan.md v2）：
-- user_space_grants 是意图态+生效态单表；预授权支持（uum_user_id 不要求 WeKnora 用户已存在）。
-- 生效机制：active ⇒ 同事务 EnsureMembership；revoke/expire ⇒ RemoveMember（即时生效）。
-- 到期由 GrantService 的清扫器落库 status='expired'（读取端对 overdue valid_until 防御性判定）。

CREATE TABLE IF NOT EXISTS user_space_grants (
    id           VARCHAR(36) PRIMARY KEY,
    uum_user_id  VARCHAR(128) NOT NULL,
    tenant_id    BIGINT NOT NULL,
    role         VARCHAR(20) NOT NULL DEFAULT 'viewer',
    status       VARCHAR(20) NOT NULL DEFAULT 'pending',
    valid_until  TIMESTAMPTZ,
    requested_by VARCHAR(128) NOT NULL DEFAULT '',
    approved_by  VARCHAR(128),
    reason       VARCHAR(255),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 一人一空间一行；重复授权 = 更新同一条
CREATE UNIQUE INDEX IF NOT EXISTS uq_user_space_grants_uum_tenant
    ON user_space_grants (uum_user_id, tenant_id);
CREATE INDEX IF NOT EXISTS idx_user_space_grants_uum  ON user_space_grants (uum_user_id);
CREATE INDEX IF NOT EXISTS idx_user_space_grants_tenant ON user_space_grants (tenant_id);
CREATE INDEX IF NOT EXISTS idx_user_space_grants_status ON user_space_grants (status);
CREATE INDEX IF NOT EXISTS idx_user_space_grants_valid_until
    ON user_space_grants (valid_until) WHERE valid_until IS NOT NULL;

-- 空间类型：personal / team / all（全员公共空间）。默认 team 保持存量租户语义不变。
ALTER TABLE tenants ADD COLUMN IF NOT EXISTS space_type VARCHAR(16) NOT NULL DEFAULT 'team';
CREATE INDEX IF NOT EXISTS idx_tenants_space_type ON tenants (space_type);
