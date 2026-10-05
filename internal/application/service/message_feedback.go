package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	werrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// Message feedback service（台账 T13 反馈闭环）. The local row is the
// source of truth; the Langfuse score is a best-effort dual-write that runs
// after the row is safe.
type MessageFeedbackService struct {
	repo        interfaces.MessageFeedbackRepository
	messageRepo interfaces.MessageRepository
}

// NewMessageFeedbackService builds the feedback service.
func NewMessageFeedbackService(
	repo interfaces.MessageFeedbackRepository,
	messageRepo interfaces.MessageRepository,
) *MessageFeedbackService {
	return &MessageFeedbackService{repo: repo, messageRepo: messageRepo}
}

// SubmitMessageFeedbackRequest is the API payload of a thumbs verdict.
type SubmitMessageFeedbackRequest struct {
	MessageID  string `json:"message_id" binding:"required"`
	SessionID  string `json:"session_id" binding:"required"`
	Rating     string `json:"rating" binding:"required"`
	ReasonCode string `json:"reason_code,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

// Submit records one verdict, snapshotting the turn so triage survives the
// session. Returns the stored row.
func (s *MessageFeedbackService) Submit(ctx context.Context, tenantID uint64, userID string, req SubmitMessageFeedbackRequest) (*types.MessageFeedback, error) {
	rating := strings.TrimSpace(req.Rating)
	if rating != types.FeedbackRatingUp && rating != types.FeedbackRatingDown {
		return nil, werrors.NewBadRequestError("rating 必须是 up 或 down")
	}
	reason := strings.TrimSpace(req.ReasonCode)
	if rating == types.FeedbackRatingDown && reason != "" && !types.FeedbackReasonCodes[reason] {
		return nil, werrors.NewBadRequestError(fmt.Sprintf("未知的 reason_code：%s", reason))
	}
	msg, err := s.messageRepo.GetMessage(ctx, req.SessionID, req.MessageID)
	if err != nil || msg == nil {
		return nil, werrors.NewBadRequestError(fmt.Sprintf("消息 %s 不存在", req.MessageID))
	}

	snapshot := s.buildSnapshot(ctx, msg)
	raw, _ := json.Marshal(snapshot)
	fb := &types.MessageFeedback{
		ID:         uuid.New().String(),
		TenantID:   tenantID,
		SessionID:  msg.SessionID,
		MessageID:  msg.ID,
		RequestID:  msg.RequestID,
		Rating:     rating,
		ReasonCode: reason,
		Comment:    strings.TrimSpace(req.Comment),
		Snapshot:   types.JSON(raw),
		CreatedBy:  userID,
	}
	if err := s.repo.Upsert(ctx, fb); err != nil {
		return nil, err
	}

	// Dual-write the Langfuse score AFTER the local row is safe; failures
	// only log (本地表权威，双写不阻塞).
	s.postScore(ctx, fb, snapshot)
	return fb, nil
}

// buildSnapshot captures the question (the closest previous user message in
// the same session) and the assistant answer excerpt plus the generation
// config recorded on the message.
func (s *MessageFeedbackService) buildSnapshot(ctx context.Context, msg *types.Message) types.FeedbackSnapshot {
	snap := types.FeedbackSnapshot{}
	if msg == nil {
		return snap
	}
	if msg.Role == "assistant" {
		snap.AnswerExcerpt = excerpt(msg.Content, 400)
		if hist, err := s.messageRepo.GetRecentMessagesBySession(ctx, msg.SessionID, 20); err == nil {
			for i := len(hist) - 1; i >= 0; i-- {
				if hist[i].Role == "user" {
					snap.Question = excerpt(hist[i].Content, 400)
					break
				}
			}
		}
	} else {
		snap.Question = excerpt(msg.Content, 400)
	}
	ec := msg.ExecutionContext
	snap.KnowledgeBaseIDs = ec.KnowledgeBaseIDs
	snap.LangfuseTraceparent = ec.LangfuseTraceparent
	snap.AgentConfigHash = ec.AgentConfigHash
	return snap
}

func excerpt(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// postScore is the fire-and-report Langfuse dual-write.
func (s *MessageFeedbackService) postScore(ctx context.Context, fb *types.MessageFeedback, snap types.FeedbackSnapshot) {
	mgr := langfuse.GetManager()
	if !mgr.Enabled() {
		return
	}
	traceID := langfuse.TraceIDFromTraceparent(snap.LangfuseTraceparent)
	if traceID == "" {
		return
	}
	value := 1.0
	if fb.Rating == types.FeedbackRatingDown {
		value = -1.0
	}
	comment := fb.ReasonCode
	if fb.Comment != "" {
		comment += " " + fb.Comment
	}
	scoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := mgr.CreateScore(scoreCtx, langfuse.Score{
		TraceID: traceID,
		Name:    "user_feedback",
		Value:   value,
		Comment: excerpt(comment, 280),
	}); err != nil {
		logger.Warnf(ctx, "feedback langfuse dual-write failed (local row is authoritative): %v", err)
	}
}

// List returns filtered feedback for the admin panel.
func (s *MessageFeedbackService) List(ctx context.Context, filter types.MessageFeedbackFilter) ([]*types.MessageFeedback, int64, error) {
	return s.repo.List(ctx, filter)
}
