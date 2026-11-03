DROP INDEX IF EXISTS idx_tenants_space_type;
ALTER TABLE tenants DROP COLUMN IF EXISTS space_type;
DROP TABLE IF EXISTS user_space_grants;
