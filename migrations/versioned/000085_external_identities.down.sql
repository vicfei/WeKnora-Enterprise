DO $$ BEGIN RAISE NOTICE '[Migration 000085 down] Dropping external_identities and uum_departments'; END $$;

DROP TABLE IF EXISTS uum_departments;
DROP TABLE IF EXISTS external_identities;
