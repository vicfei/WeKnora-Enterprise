-- Description: Hierarchical tag tree for knowledge bases (secondary development).
-- Adjacency list (parent_id) + materialized path. tag_id links LEAF nodes to native
-- knowledge_tags rows (tag_id IS NULL <=> internal directory node). Rows are
-- HARD-deleted by design; the deleted_at column is kept unused so a future
-- soft-delete switch only needs model changes, not DDL.
DO $$ BEGIN RAISE NOTICE '[Migration 000084] Creating tag_tree'; END $$;

CREATE TABLE IF NOT EXISTS tag_tree (
    id                VARCHAR(36)   PRIMARY KEY,
    tenant_id         BIGINT        NOT NULL,
    knowledge_base_id VARCHAR(36)   NOT NULL,
    parent_id         VARCHAR(36),
    name              VARCHAR(255)  NOT NULL,
    path              VARCHAR(1000) NOT NULL,
    level             INT           NOT NULL DEFAULT 0,
    tag_id            VARCHAR(36),
    is_auto_extract   BOOLEAN       NOT NULL DEFAULT FALSE,
    sort_order        INT           NOT NULL DEFAULT 0,
    created_by        VARCHAR(36),
    created_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    deleted_at        TIMESTAMPTZ
);

COMMENT ON TABLE tag_tree IS 'Hierarchical tag tree per knowledge base; leaf nodes carry tag_id referencing knowledge_tags.id';
COMMENT ON COLUMN tag_tree.parent_id IS 'Parent node id; NULL = root node';
COMMENT ON COLUMN tag_tree.path IS 'Materialized path of node names, e.g. /行业研究/新能源/锂电';
COMMENT ON COLUMN tag_tree.level IS 'Depth of the node (root = 0)';
COMMENT ON COLUMN tag_tree.tag_id IS 'Native knowledge_tags.id for LEAF nodes only; NULL for internal directory nodes';

CREATE INDEX IF NOT EXISTS idx_tag_tree_tenant_kb ON tag_tree (tenant_id, knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_tag_tree_parent ON tag_tree (parent_id);
CREATE INDEX IF NOT EXISTS idx_tag_tree_path ON tag_tree (path);
CREATE INDEX IF NOT EXISTS idx_tag_tree_tag ON tag_tree (tag_id);
