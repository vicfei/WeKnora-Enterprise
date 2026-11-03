-- fork migration 20261005183000_require_business_date (up)
-- 描述: 时效感知检索的 KB 级治理开关（设计 docs/design/time-aware-retrieval-design.md
-- §4.1"入库必填 business_date，缺失时按 KB 级配置决定拒绝"）。默认 FALSE：存量 KB
-- 的入库路径零行为变更；开启后，缺业务时间的知识创建被拒绝而非静默 NULL。
ALTER TABLE knowledge_bases ADD COLUMN IF NOT EXISTS require_business_date BOOLEAN NOT NULL DEFAULT FALSE;
