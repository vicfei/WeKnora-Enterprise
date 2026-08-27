package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// UserSpaceGrantHandler serves the v2 admin API: space (tenant) and grant
// management. JWT-authenticated system admins only (route guard
// g.SystemAdmin()); every mutation is attributed to the acting admin via
// types.UserIDFromContext for the audit trail.
type UserSpaceGrantHandler struct {
	grantService  interfaces.UserSpaceGrantService
	tenantService interfaces.TenantService
}

// NewUserSpaceGrantHandler creates the handler.
func NewUserSpaceGrantHandler(
	grantService interfaces.UserSpaceGrantService,
	tenantService interfaces.TenantService,
) *UserSpaceGrantHandler {
	return &UserSpaceGrantHandler{grantService: grantService, tenantService: tenantService}
}

// ── spaces ──────────────────────────────────────────────────────────

type adminSpaceResponse struct {
	ID          uint64 `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SpaceType   string `json:"space_type"`
	Status      string `json:"status"`
}

// ListSpaces godoc
// @Summary      空间清单（v2 管理端）
// @Description  列出全部空间（租户）及其 space_type。系统管理员专属。
// @Tags         空间授权管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/spaces [get]
func (h *UserSpaceGrantHandler) ListSpaces(c *gin.Context) {
	tenants, err := h.tenantService.ListAllTenants(c.Request.Context())
	if err != nil {
		c.Error(errors.NewInternalServerError("列出空间失败").WithDetails(err.Error()))
		return
	}
	out := make([]adminSpaceResponse, 0, len(tenants))
	for _, t := range tenants {
		if t == nil {
			continue
		}
		out = append(out, adminSpaceResponse{
			ID: t.ID, Name: t.Name, Description: t.Description,
			SpaceType: t.SpaceType, Status: t.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out})
}

type createSpaceRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	SpaceType   string `json:"space_type" binding:"required"`
}

// CreateSpace godoc
// @Summary      创建空间（v2 管理端）
// @Description  创建团队/全员空间（personal 由用户首登自动创建）。不 bootstrap Owner——成员关系只经 grants 流转（治理冻结）。
// @Tags         空间授权管理
// @Accept       json
// @Produce      json
// @Param        request body createSpaceRequest true "空间信息"
// @Success      201  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/spaces [post]
func (h *UserSpaceGrantHandler) CreateSpace(c *gin.Context) {
	var req createSpaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}
	if !types.IsValidSpaceType(req.SpaceType) || req.SpaceType == types.SpaceTypePersonal {
		c.Error(errors.NewBadRequestError("space_type 仅支持 team / all（personal 由首登自动创建）"))
		return
	}
	tenant, err := h.tenantService.CreateTenant(c.Request.Context(), &types.Tenant{
		Name: req.Name, Description: req.Description, SpaceType: req.SpaceType, Status: "active",
	})
	if err != nil {
		c.Error(errors.NewInternalServerError("创建空间失败").WithDetails(err.Error()))
		return
	}
	logger.WarnWithFields(c.Request.Context(), map[string]interface{}{
		"actor": actorID(c), "tenant_id": tenant.ID, "name": req.Name, "space_type": req.SpaceType,
	}, "v2 space created (no owner bootstrapped; membership flows via grants only)")
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": adminSpaceResponse{
		ID: tenant.ID, Name: tenant.Name, Description: tenant.Description,
		SpaceType: tenant.SpaceType, Status: tenant.Status,
	}})
}

type updateSpaceRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	SpaceType   *string `json:"space_type"`
}

// UpdateSpace godoc
// @Summary      更新空间（v2 管理端）
// @Description  改名 / 改描述 / 改 space_type。
// @Tags         空间授权管理
// @Accept       json
// @Produce      json
// @Param        tenant_id path int true "空间（租户）ID"
// @Param        request body updateSpaceRequest true "更新字段"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/spaces/{tenant_id} [put]
func (h *UserSpaceGrantHandler) UpdateSpace(c *gin.Context) {
	tenantID, err := strconv.ParseUint(c.Param("tenant_id"), 10, 64)
	if err != nil || tenantID == 0 {
		c.Error(errors.NewBadRequestError("tenant_id 不合法"))
		return
	}
	var req updateSpaceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}
	tenant, err := h.tenantService.GetTenantByID(c.Request.Context(), tenantID)
	if err != nil || tenant == nil {
		c.Error(errors.NewNotFoundError("空间不存在"))
		return
	}
	if req.Name != nil {
		tenant.Name = *req.Name
	}
	if req.Description != nil {
		tenant.Description = *req.Description
	}
	if req.SpaceType != nil {
		if !types.IsValidSpaceType(*req.SpaceType) {
			c.Error(errors.NewBadRequestError("space_type 非法"))
			return
		}
		tenant.SpaceType = *req.SpaceType
	}
	updated, err := h.tenantService.UpdateTenant(c.Request.Context(), tenant)
	if err != nil {
		c.Error(errors.NewInternalServerError("更新空间失败").WithDetails(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": adminSpaceResponse{
		ID: updated.ID, Name: updated.Name, Description: updated.Description,
		SpaceType: updated.SpaceType, Status: updated.Status,
	}})
}

// ── grants ──────────────────────────────────────────────────────────

func actorID(c *gin.Context) string {
	if id, ok := types.UserIDFromContext(c.Request.Context()); ok {
		return id
	}
	return "unknown"
}

func parseOptionalUint(c *gin.Context, name string) uint64 {
	raw := c.Query(name)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// ListGrants godoc
// @Summary      授权查询（v2 管理端）
// @Description  按 uum_user_id / tenant_id / status 过滤授权记录（含 pending 预授权与 revoked/expired 历史）。
// @Tags         空间授权管理
// @Produce      json
// @Param        uum_user_id query string false "按工号过滤"
// @Param        tenant_id   query int    false "按空间过滤"
// @Param        status      query string false "pending|active|expired|revoked"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /admin/grants [get]
func (h *UserSpaceGrantHandler) ListGrants(c *gin.Context) {
	filter := types.UserSpaceGrantFilter{
		UUMUser:  c.Query("uum_user_id"),
		TenantID: parseOptionalUint(c, "tenant_id"),
	}
	if status := c.Query("status"); status != "" {
		filter.Status = types.UserSpaceGrantStatus(status)
	}
	grants, err := h.grantService.List(c.Request.Context(), filter)
	if err != nil {
		c.Error(errors.NewInternalServerError("查询授权失败").WithDetails(err.Error()))
		return
	}
	if grants == nil {
		grants = []*types.UserSpaceGrant{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": grants})
}

// CreateGrant godoc
// @Summary      新建/更新授权（v2 管理端）
// @Description  upsert 语义（一人一空间一行）。activate_now=false 进 pending（预授权：工号无需已建号）；true 直接生效（未建号用户在首登时落成员关系）。
// @Tags         空间授权管理
// @Accept       json
// @Produce      json
// @Param        request body interfaces.UserSpaceGrantAdminRequest true "授权请求"
// @Success      201  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/grants [post]
func (h *UserSpaceGrantHandler) CreateGrant(c *gin.Context) {
	var req interfaces.UserSpaceGrantAdminRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}
	grant, err := h.grantService.Create(c.Request.Context(), &req, actorID(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"success": true, "data": grant})
}

// GetGrant godoc
// @Summary      授权详情（v2 管理端）
// @Tags         空间授权管理
// @Produce      json
// @Param        id path string true "授权ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/grants/{id} [get]
func (h *UserSpaceGrantHandler) GetGrant(c *gin.Context) {
	grant, err := h.grantService.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": grant})
}

type updateGrantRequest struct {
	Role       *types.TenantRole `json:"role"`
	ValidUntil *time.Time        `json:"valid_until"`
}

// UpdateGrant godoc
// @Summary      改角色/有效期（v2 管理端）
// @Description  role 变更同步成员角色；valid_until 延长可复活 expired 授权。
// @Tags         空间授权管理
// @Accept       json
// @Produce      json
// @Param        id path string true "授权ID"
// @Param        request body updateGrantRequest true "更新字段"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/grants/{id} [put]
func (h *UserSpaceGrantHandler) UpdateGrant(c *gin.Context) {
	var req updateGrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError("请求参数不合法").WithDetails(err.Error()))
		return
	}
	var grant *types.UserSpaceGrant
	var err error
	if req.Role != nil {
		grant, err = h.grantService.UpdateRole(c.Request.Context(), c.Param("id"), *req.Role, actorID(c))
		if err != nil {
			c.Error(err)
			return
		}
	}
	if req.ValidUntil != nil {
		grant, err = h.grantService.UpdateValidity(c.Request.Context(), c.Param("id"), req.ValidUntil, actorID(c))
		if err != nil {
			c.Error(err)
			return
		}
	}
	if grant == nil {
		c.Error(errors.NewBadRequestError("role 与 valid_until 至少提供一项"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": grant})
}

// ApproveGrant godoc
// @Summary      审批通过（v2 管理端）
// @Description  pending → active；未建号的预授权在用户首次登录时落成员关系。幂等。
// @Tags         空间授权管理
// @Produce      json
// @Param        id path string true "授权ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/grants/{id}/approve [put]
func (h *UserSpaceGrantHandler) ApproveGrant(c *gin.Context) {
	grant, err := h.grantService.Approve(c.Request.Context(), c.Param("id"), actorID(c))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": grant})
}

// RevokeGrant godoc
// @Summary      回收授权（v2 管理端）
// @Description  移除成员关系——用户下一请求即 403（逐请求成员校验），无需吊销 token。幂等。
// @Tags         空间授权管理
// @Produce      json
// @Param        id path string true "授权ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /admin/grants/{id} [delete]
func (h *UserSpaceGrantHandler) RevokeGrant(c *gin.Context) {
	if err := h.grantService.Revoke(c.Request.Context(), c.Param("id"), actorID(c)); err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"revoked": true}})
}
