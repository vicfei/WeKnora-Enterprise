package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// IdentityBridgeHandler handles the UUM identity bridge endpoints.
//
// These are machine-to-machine endpoints guarded by a platform API key with
// the system_identity_bridge capability. The platform-key gate requires an
// X-Tenant-ID header on any request; that header only satisfies the gate —
// every endpoint's logic uses the tenant ids from the request body.
type IdentityBridgeHandler struct {
	bridgeService interfaces.IdentityBridgeService
}

// NewIdentityBridgeHandler creates a new IdentityBridgeHandler.
func NewIdentityBridgeHandler(bridgeService interfaces.IdentityBridgeService) *IdentityBridgeHandler {
	return &IdentityBridgeHandler{bridgeService: bridgeService}
}

// bridgeLoginResponse is the login shape plus the provisioning flag.
// No omitempty on IsNewUser: false must be rendered explicitly — omitting
// it made idempotent re-bridges look like error responses to jq consumers
// (documented in .context/research/permission_single_source_review.md §二).
type bridgeLoginResponse struct {
	dto.AuthLoginResponse
	IsNewUser bool `json:"is_new_user"`
}

// Bridge godoc
// @Summary      UUM 身份桥接（建号/换票）
// @Description  声明式接口：确保 UUM 用户持有目标部门租户的访问权并返回该租户的 JWT 登录响应（Web 门户通道）。已映射→登录；未映射→自愈补登或建号（tenantless + 部门成员，默认 viewer）。幂等。事件顺序约束：同一用户的 transfer/disable 必须先于新的 bridge 调用被处理。认证：platform key（system_identity_bridge capability）+ 必带 X-Tenant-ID（仅满足门禁，逻辑以 body 为准）
// @Tags         UUM身份桥接
// @Accept       json
// @Produce      json
// @Param        request  body   types.IdentityBridgeRequest  true  "桥接请求"
// @Success      200  {object}  map[string]interface{}  "登录响应（token/refresh_token/user/memberships + is_new_user）"
// @Failure      400  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      409  {object}  errors.AppError
// @Security     ApiKeyAuth
// @Router       /identity/bridge [post]
func (h *IdentityBridgeHandler) Bridge(c *gin.Context) {
	ctx := c.Request.Context()

	var req types.IdentityBridgeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Error(ctx, "Failed to bind identity bridge payload", err)
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}
	req.Username = secutils.SanitizeForLog(strings.TrimSpace(req.Username))
	req.Email = secutils.SanitizeForLog(strings.TrimSpace(req.Email))

	resp, isNew, err := h.bridgeService.Bridge(ctx, &req)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"uum_user_id": secutils.SanitizeForLog(req.UUMUserID),
			"tenant_id":   req.TenantID,
		})
		c.Error(err)
		return
	}

	login := dto.NewAuthLoginResponse(resp)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    bridgeLoginResponse{AuthLoginResponse: *login, IsNewUser: isNew},
	})
}

// Disable godoc
// @Summary      禁用 UUM 桥接用户
// @Description  两步组合禁用：置 is_active=false + 吊销该用户全部 access/refresh token（仅阻止登录会让已签发 JWT 继续有效至多 24h）
// @Tags         UUM身份桥接
// @Accept       json
// @Produce      json
// @Param        request  body   types.IdentityDisableRequest  true  "禁用请求"
// @Success      200  {object}  map[string]interface{}  "禁用成功"
// @Failure      400  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     ApiKeyAuth
// @Router       /identity/disable [post]
func (h *IdentityBridgeHandler) Disable(c *gin.Context) {
	ctx := c.Request.Context()

	var req types.IdentityDisableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Error(ctx, "Failed to bind identity disable payload", err)
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}

	if err := h.bridgeService.Disable(ctx, req.UUMUserID, req.TenantID); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"uum_user_id": secutils.SanitizeForLog(req.UUMUserID),
			"tenant_id":   req.TenantID,
		})
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"disabled": true}})
}

// Transfer godoc
// @Summary      UUM 用户调岗（跨部门租户迁移）
// @Description  成员关系迁移到新部门租户（默认保留原角色）；旧映射行删除、全部 token 吊销（强制重新 bridge）；历史 KB/文档/会话留在原部门租户。兼任场景不走此接口（用原生成员管理直接加人）。幂等：源映射不存在且目标映射已存在时直接返回现有映射
// @Tags         UUM身份桥接
// @Accept       json
// @Produce      json
// @Param        request  body   types.IdentityTransferRequest  true  "调岗请求"
// @Success      200  {object}  map[string]interface{}  "迁移后的映射"
// @Failure      400  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      409  {object}  errors.AppError
// @Security     ApiKeyAuth
// @Router       /identity/transfer [post]
func (h *IdentityBridgeHandler) Transfer(c *gin.Context) {
	ctx := c.Request.Context()

	var req types.IdentityTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Error(ctx, "Failed to bind identity transfer payload", err)
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}

	mapping, err := h.bridgeService.Transfer(ctx, &req)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"uum_user_id":    secutils.SanitizeForLog(req.UUMUserID),
			"from_tenant_id": req.FromTenantID,
			"to_tenant_id":   req.ToTenantID,
		})
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": mapping})
}

// GetMapping godoc
// @Summary      查询 UUM 用户映射
// @Description  按 uum_user_id + tenant_id 查询映射（tenant_id 为 query 参数，必填）
// @Tags         UUM身份桥接
// @Produce      json
// @Param        uum_user_id  path   string  true  "UUM 用户ID"
// @Param        tenant_id    query  int     true  "部门租户ID"
// @Success      200  {object}  map[string]interface{}  "映射详情"
// @Failure      400  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     ApiKeyAuth
// @Router       /identity/mapping/{uum_user_id} [get]
func (h *IdentityBridgeHandler) GetMapping(c *gin.Context) {
	ctx := c.Request.Context()
	uumUserID := secutils.SanitizeForLog(c.Param("uum_user_id"))
	tenantID, err := parseUintQuery(c, "tenant_id")
	if err != nil {
		c.Error(err)
		return
	}

	mapping, err := h.bridgeService.GetMapping(ctx, uumUserID, tenantID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": mapping})
}

// GetMappingsByUser godoc
// @Summary      查询 WeKnora 用户的 UUM 映射列表
// @Description  按 user_id 查询其全部映射行
// @Tags         UUM身份桥接
// @Produce      json
// @Param        user_id  path   string  true  "WeKnora 用户ID"
// @Success      200  {object}  map[string]interface{}  "映射列表"
// @Failure      400  {object}  errors.AppError
// @Security     ApiKeyAuth
// @Router       /identity/mapping/by-user/{user_id} [get]
func (h *IdentityBridgeHandler) GetMappingsByUser(c *gin.Context) {
	ctx := c.Request.Context()
	userID := secutils.SanitizeForLog(c.Param("user_id"))

	mappings, err := h.bridgeService.GetMappingsByUser(ctx, userID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": mappings})
}

// UpsertDepartment godoc
// @Summary      绑定/更新 UUM 部门映射
// @Description  UUM 部门 ↔ WeKnora 部门租户的权威 1:1 映射（双向唯一，冲突返回 409）；同对重复调用更新部门名
// @Tags         UUM身份桥接
// @Accept       json
// @Produce      json
// @Param        department_id  path   string                      true  "UUM 部门ID"
// @Param        request        body   types.UUMDepartmentRequest  true  "绑定信息"
// @Success      200  {object}  map[string]interface{}  "映射详情"
// @Failure      400  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      409  {object}  errors.AppError
// @Security     ApiKeyAuth
// @Router       /identity/departments/{department_id} [put]
func (h *IdentityBridgeHandler) UpsertDepartment(c *gin.Context) {
	ctx := c.Request.Context()
	departmentID := secutils.SanitizeForLog(c.Param("department_id"))

	var req types.UUMDepartmentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Error(ctx, "Failed to bind uum department payload", err)
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}

	dept, err := h.bridgeService.UpsertDepartment(ctx, departmentID, &req)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"department_id": departmentID,
			"tenant_id":     req.TenantID,
		})
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": dept})
}

// parseUintQuery parses a required uint64 query parameter.
func parseUintQuery(c *gin.Context, name string) (uint64, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return 0, errors.NewBadRequestError(name + " 不能为空")
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, errors.NewBadRequestError(name + " 不合法")
	}
	return value, nil
}
