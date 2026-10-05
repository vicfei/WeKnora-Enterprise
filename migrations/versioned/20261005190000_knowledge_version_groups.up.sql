-- fork migration 20261005190000_knowledge_version_groups (up)
-- 描述: 知识文档版本管理 MVP（台账 T11）——上传勾选"作为新版本"后按归一标题
-- 组成版本组，pending 待确认，确认生效时旧版自动下线（复用 enable_status 门控，
-- 检索层零改动）。四列全部可空：未启用版本特性的文档与存量行为逐位一致。
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS version_group VARCHAR(512) NULL;
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS version_number INTEGER NULL;
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS version_status VARCHAR(16) NULL;
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS superseded_by VARCHAR(36) NULL;
CREATE INDEX IF NOT EXISTS idx_knowledges_version_group
    ON knowledges (knowledge_base_id, version_group)
    WHERE version_group IS NOT NULL;
-- KB 级保留版本数覆盖（0/NULL = 默认 3）。
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS max_versions INTEGER NOT NULL DEFAULT 0;
