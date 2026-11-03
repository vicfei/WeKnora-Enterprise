-- fork migration 20261005193000_message_feedback (up)
-- 描述: 反馈闭环（台账 T13）——聊天消息点赞/点踩落库。本地表为权威记录；
-- Langfuse score 为可选双写（失败仅记日志）。快照字段（问题、回答节选、
-- agent/模型/知识库配置）让事后归因不依赖会话存活。
CREATE TABLE message_feedback (
    id            VARCHAR(36) PRIMARY KEY,
    tenant_id     INTEGER NOT NULL,
    session_id    VARCHAR(36) NOT NULL,
    message_id    VARCHAR(36) NOT NULL,
    request_id    VARCHAR(64) NOT NULL DEFAULT '',
    rating        VARCHAR(8)  NOT NULL,               -- 'up' | 'down'
    reason_code   VARCHAR(32) NOT NULL DEFAULT '',   -- down 的快捷理由枚举
    comment       TEXT NOT NULL DEFAULT '',
    snapshot      JSONB NOT NULL DEFAULT '{}',        -- 问题/回答节选/配置快照
    created_by    VARCHAR(64) NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_message_feedback_once ON message_feedback (message_id, created_by);
CREATE INDEX idx_message_feedback_tenant ON message_feedback (tenant_id, rating, created_at DESC);
