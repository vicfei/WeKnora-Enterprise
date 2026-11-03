-- fork migration 20261005190000_knowledge_version_groups (down)
DROP INDEX IF EXISTS idx_knowledges_version_group;
ALTER TABLE knowledges DROP COLUMN IF EXISTS superseded_by;
ALTER TABLE knowledges DROP COLUMN IF EXISTS version_status;
ALTER TABLE knowledges DROP COLUMN IF EXISTS version_number;
ALTER TABLE knowledges DROP COLUMN IF EXISTS version_group;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS max_versions;
