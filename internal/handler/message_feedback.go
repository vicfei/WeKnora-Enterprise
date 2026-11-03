package handler

import (
	"net/http"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// MessageFeedbackHandler serves the chat thumbs feedback endpoints
// (台账 T13 反馈闭环).
type MessageFeedbackHandler struct {
	feedbackService *service.MessageFeedbackService
}

// NewMessageFeedbackHandler builds the handler.
func NewMessageFeedbackHandler(feedbackService *service.MessageFeedbackService) *MessageFeedbackHandler {
	return &MessageFeedbackHandler{feedbackService: feedbackService}
}

// Submit godoc
// @Summary      提交消息点赞/点踩
// @Description  聊天消息的点赞点踩反馈（本地表权威，Langfuse score 可选双写）；同一用户对同一消息的后提交覆盖前值
// @Tags         反馈
// @Accept       json
// @Produce      json
// @Param        request body object true "message_id + session_id + rating(up|down) + reason_code + comment"
// @Success      200 {object} map[string]interface{}
// @Security     Bearer
// @Router       /feedback [post]
func (h *MessageFeedbackHandler) Submit(c *gin.Context) {
	ctx := c.Request.Context()
	var req service.SubmitMessageFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("Invalid request parameters").WithDetails(err.Error()))
		return
	}
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	userID, _ := c.Get(types.UserIDContextKey.String())
	userIDStr, _ := userID.(string)
	fb, err := h.feedbackService.Submit(ctx, tenantID, userIDStr, req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": fb})
}

// List godoc
// @Summary      反馈管理列表
// @Description  按评分/理由过滤的分页反馈列表（管理面板用）
// @Tags         反馈
// @Produce      json
// @Param        rating query string false "up|down"
// @Param        reason_code query string false "快捷理由"
// @Param        page query int false "页码"
// @Param        page_size query int false "页大小（≤100）"
// @Success      200 {object} map[string]interface{}
// @Security     Bearer
// @Router       /feedback [get]
func (h *MessageFeedbackHandler) List(c *gin.Context) {
	ctx := c.Request.Context()
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	rows, total, err := h.feedbackService.List(ctx, types.MessageFeedbackFilter{
		TenantID:   c.GetUint64(types.TenantIDContextKey.String()),
		Rating:     c.Query("rating"),
		ReasonCode: c.Query("reason_code"),
		Page:       page,
		PageSize:   size,
	})
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(errors.NewInternalServerError("Failed to list feedback").WithDetails(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": total, "page": page, "page_size": size})
}
