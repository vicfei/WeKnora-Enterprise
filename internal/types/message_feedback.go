package types

import (
	"encoding/json"
	"time"
)

// Message feedback（台账 T13 反馈闭环）. The local row is the authoritative
// record; the Langfuse score is an optional dual-write that never blocks the
// API on failure.
type MessageFeedback struct {
	ID        string `json:"id"                     gorm:"type:varchar(36);primaryKey"`
	TenantID  uint64 `json:"tenant_id"              gorm:"index"`
	SessionID string `json:"session_id"             gorm:"type:varchar(36)"`
	MessageID string `json:"message_id"             gorm:"type:varchar(36)"`
	RequestID string `json:"request_id"             gorm:"type:varchar(64)"`
	// Rating is "up" or "down".
	Rating string `json:"rating"                 gorm:"type:varchar(8)"`
	// ReasonCode is the quick-reason enum for down-votes.
	ReasonCode string `json:"reason_code"            gorm:"type:varchar(32)"`
	Comment    string `json:"comment"                gorm:"type:text"`
	// Snapshot captures question/answer excerpt and the generation config so
	// post-hoc triage does not depend on the session still existing.
	Snapshot  JSON      `json:"snapshot"               gorm:"type:jsonb"`
	CreatedBy string    `json:"created_by"             gorm:"type:varchar(64)"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Feedback ratings and down-vote reason codes (the quick-reason enum shown
// in the chat popover; "other" carries the free-text comment).
const (
	FeedbackRatingUp   = "up"
	FeedbackRatingDown = "down"

	FeedbackReasonWrongAnswer = "wrong_answer"
	FeedbackReasonBadCitation = "bad_citation"
	FeedbackReasonOutdated    = "outdated"
	FeedbackReasonIncomplete  = "incomplete"
	FeedbackReasonStyle       = "style"
	FeedbackReasonOther       = "other"
)

// FeedbackReasonCodes is the closed set accepted by the API.
var FeedbackReasonCodes = map[string]bool{
	FeedbackReasonWrongAnswer: true, FeedbackReasonBadCitation: true,
	FeedbackReasonOutdated: true, FeedbackReasonIncomplete: true,
	FeedbackReasonStyle: true, FeedbackReasonOther: true,
}

// FeedbackSnapshot is the JSON payload of MessageFeedback.Snapshot.
type FeedbackSnapshot struct {
	Question         string   `json:"question,omitempty"`
	AnswerExcerpt    string   `json:"answer_excerpt,omitempty"`
	AgentConfigHash  string   `json:"agent_config_hash,omitempty"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids,omitempty"`
	// LangfuseTraceparent lets the score dual-write correlate to the turn's
	// trace (W3C traceparent carries the trace id).
	LangfuseTraceparent string `json:"langfuse_traceparent,omitempty"`
}

// Value implements driver.Valuer.
func (s FeedbackSnapshot) Value() (json.RawMessage, error) { return json.Marshal(s) }

// MessageFeedbackFilter is the list-query projection.
type MessageFeedbackFilter struct {
	TenantID   uint64
	Rating     string
	ReasonCode string
	Page       int
	PageSize   int
}

// TableName pins the singular table name the migration creates (GORM's
// default pluralization would look for message_feedbacks).
func (MessageFeedback) TableName() string { return "message_feedback" }
