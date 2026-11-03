-- fork migration 20260918042145_knowledge_business_date (down)
DROP INDEX IF EXISTS idx_knowledges_kb_business_date;
ALTER TABLE knowledges DROP COLUMN IF EXISTS business_date;
