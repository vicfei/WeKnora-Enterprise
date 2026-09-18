-- fork migration 20260918042145_knowledge_business_date (up)
-- 描述: 时效感知检索（time-aware retrieval）数据层——knowledges 增加业务时间点
-- business_date（TIMESTAMPTZ NULL）。不回填：存量行保持 NULL，系统时间不冒充
-- 业务时间（设计 docs/design/time-aware-retrieval-design.md §4.1 决策 D1）。
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS business_date TIMESTAMPTZ NULL;

-- 复合索引覆盖"KB 内按业务时间范围圈定文档"的解析路径（where kb_id = ? and
-- business_date between ? and ?）。单列不建：没有 kb 维度的裸时间查询不存在。
CREATE INDEX IF NOT EXISTS idx_knowledges_kb_business_date
    ON knowledges (knowledge_base_id, business_date)
    WHERE business_date IS NOT NULL;
