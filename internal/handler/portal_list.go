package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PortalListHandler serves the v2 portal aggregation endpoint (decision 019).
// It wraps the upstream KnowledgeBaseHandler to reuse its response builders
// (buildKBListResponse, envDefaultStoreView) and adds the membership/tenant
// services that drive the cross-space aggregation. Keeping this in a fork-only
// file leaves internal/handler/knowledgebase.go free of fork-owned fields so
// future upstream merges stay conflict-free.
type PortalListHandler struct {
	*KnowledgeBaseHandler
	// memberService/tenantService drive the aggregation (all active
	// memberships ∪ org shares); nil memberService falls back to
	// single-active-tenant semantics (legacy deployments / degraded mode).
	memberService interfaces.TenantMemberService
	tenantService interfaces.TenantService
}

// NewPortalListHandler creates the portal list handler.
func NewPortalListHandler(
	kb *KnowledgeBaseHandler,
	memberService interfaces.TenantMemberService,
	tenantService interfaces.TenantService,
) *PortalListHandler {
	return &PortalListHandler{
		KnowledgeBaseHandler: kb,
		memberService:        memberService,
		tenantService:        tenantService,
	}
}

// ListPortalKnowledgeBases serves the portal merged view (secondary
// development, decision 019). System admins see every non-temporary KB
// system-wide (each entry carries its tenant_id); everyone else sees the
// own-department tenant's KBs (stats/share counts like the default list)
// plus KBs shared into their organizations, deduped by KB id with
// own-tenant entries winning (补充1).
// ListPortalKnowledgeBases godoc
// @Summary      门户聚合知识库列表（二开，决策 019）
// @Description  GET /portal/knowledge-bases：全部活跃 memberships 的知识库 ∪ 经组织共享分发的内容（含 space_type=all 全员空间），注入 tenant_name/space_type/my_role 供门户分组。系统管理员返回全平台非临时库。
// @Tags         门户
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "聚合知识库列表"
// @Failure      401  {object}  apperrors.AppError      "未认证"
// @Failure      500  {object}  apperrors.AppError      "服务器错误"
// @Security     Bearer
// @Router       /portal/knowledge-bases [get]
func (h *PortalListHandler) ListPortalKnowledgeBases(c *gin.Context) {
	ctx := c.Request.Context()
	callerTenantID := c.GetUint64(types.TenantIDContextKey.String())
	if callerTenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("workspace ID not found"))
		return
	}

	if types.IsSystemAdminFromContext(ctx) {
		kbs, err := h.service.ListAllKnowledgeBases(ctx)
		if err != nil {
			logger.ErrorWithFields(ctx, err, nil)
			c.Error(apperrors.NewInternalServerError(err.Error()))
			return
		}
		kbs = filterKnowledgeBasesForAPIKeyScope(ctx, kbs)
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data":    h.buildKBListResponse(ctx, kbs, callerTenantID),
		})
		return
	}

	// v2 aggregation: every ACTIVE membership's own KBs plus everything
	// shared into those tenants, enriched with tenant_name / space_type /
	// my_role so the portal can group by space (plan §3.2-11). Falls back
	// to the previous single-active-tenant semantics when membership data
	// is unavailable (legacy deployments / degraded mode).
	userIDVal, _ := c.Get(types.UserIDContextKey.String())
	userID, _ := userIDVal.(string)
	var memberships []*types.TenantMember
	if h.memberService != nil && userID != "" {
		if ms, merr := h.memberService.ListByUser(ctx, userID); merr == nil {
			memberships = ms
		} else {
			logger.Warnf(ctx, "portal aggregation: membership listing failed, fallback to single-tenant: %v", merr)
		}
	}
	degraded := len(memberships) == 0
	if degraded {
		// degraded mode mirrors the pre-aggregation single-tenant semantics
		memberships = []*types.TenantMember{{
			TenantID: callerTenantID, Role: types.TenantRoleFromContext(ctx),
			Status: types.TenantMemberStatusActive,
		}}
	}

	// batch tenant metadata (name + space_type) for all membership tenants
	tenantIDs := make([]uint64, 0, len(memberships))
	for _, m := range memberships {
		if m != nil && m.TenantID != 0 {
			tenantIDs = append(tenantIDs, m.TenantID)
		}
	}
	tenantsByID := map[uint64]*types.Tenant{}
	if h.tenantService != nil && len(tenantIDs) > 0 {
		if found, terr := h.tenantService.GetTenantsByIDs(ctx, tenantIDs); terr == nil {
			tenantsByID = found
		}
	}

	data := make([]interface{}, 0, 16)
	seen := make(map[string]bool)
	defaultView := h.envDefaultStoreView(ctx)

	// 1) own KBs per membership tenant (active memberships first, deduped)
	for _, m := range memberships {
		if m == nil || m.TenantID == 0 || m.Status != types.TenantMemberStatusActive {
			continue
		}
		var kbs []*types.KnowledgeBase
		var err error
		if degraded {
			kbs, err = h.service.ListKnowledgeBases(ctx)
		} else {
			kbs, err = h.service.ListKnowledgeBasesByTenantID(ctx, m.TenantID)
		}
		if err != nil {
			logger.Warnf(ctx, "portal aggregation: list tenant %d failed: %v", m.TenantID, err)
			continue
		}
		kbs = filterKnowledgeBasesForAPIKeyScope(ctx, kbs)
		if len(kbs) > 0 && h.kbShareService != nil {
			kbIDs := make([]string, len(kbs))
			for i, kb := range kbs {
				kbIDs[i] = kb.ID
			}
			if shareCounts, err := h.kbShareService.CountSharesByKnowledgeBaseIDs(ctx, kbIDs); err == nil {
				for _, kb := range kbs {
					if count, ok := shareCounts[kb.ID]; ok {
						kb.ShareCount = count
					}
				}
			}
		}
		enrichKBCreatorNames(ctx, h.userService, kbs)

		views := h.buildKBListResponse(ctx, kbs, callerTenantID)
		for i, kb := range kbs {
			if seen[kb.ID] || i >= len(views) {
				continue
			}
			seen[kb.ID] = true
			if vm, ok := views[i].(map[string]interface{}); ok {
				vm["tenant_name"] = spaceMeta(tenantsByID, kb.TenantID).name
				vm["space_type"] = spaceMeta(tenantsByID, kb.TenantID).spaceType
				vm["my_role"] = m.Role
			}
			data = append(data, views[i])
		}
	}

	// 2) KBs shared into any of the membership tenants (org-share chain;
	// this is also how the all-staff space reaches every user, see plan §四-5)
	// First collect all shared candidates, then batch-load the metadata of
	// SOURCE tenants (they may be outside the caller's memberships, e.g.
	// the all-staff space tenant).
	type sharedItem = *types.SharedKnowledgeBaseInfo
	var sharedCandidates []sharedItem
	for _, m := range memberships {
		if m == nil || m.TenantID == 0 || m.Status != types.TenantMemberStatusActive {
			continue
		}
		shared, err := h.kbShareService.ListSharedKnowledgeBases(ctx, m.TenantID, m.Role)
		if err != nil {
			logger.Warnf(ctx, "portal aggregation: shared list tenant %d failed: %v", m.TenantID, err)
			continue
		}
		for _, info := range shared {
			if info == nil || info.KnowledgeBase == nil || seen[info.KnowledgeBase.ID] {
				continue // dedupe: own entries win over shared rows
			}
			seen[info.KnowledgeBase.ID] = true
			sharedCandidates = append(sharedCandidates, info)
		}
	}
	if len(sharedCandidates) > 0 && h.tenantService != nil {
		need := make([]uint64, 0, len(sharedCandidates))
		for _, info := range sharedCandidates {
			if _, ok := tenantsByID[info.SourceTenantID]; !ok {
				need = append(need, info.SourceTenantID)
			}
		}
		if len(need) > 0 {
			if found, terr := h.tenantService.GetTenantsByIDs(ctx, need); terr == nil {
				for id, t := range found {
					tenantsByID[id] = t
				}
			}
		}
	}
	for _, info := range sharedCandidates {
		view := defaultView
		if info.KnowledgeBase.HasVectorStore() {
			if info.KnowledgeBase.TenantID != callerTenantID {
				view = types.SharedStoreDisplay()
			} else {
				view = types.UnavailableStoreDisplay()
			}
		}
		data = append(data, buildKBResponse(info.KnowledgeBase, view, map[string]interface{}{
			"shared":           true,
			"share_id":         info.ShareID,
			"organization_id":  info.OrganizationID,
			"org_name":         info.OrgName,
			"permission":       info.Permission,
			"source_tenant_id": info.SourceTenantID,
			"shared_at":        info.SharedAt,
			"tenant_name":      spaceMeta(tenantsByID, info.SourceTenantID).name,
			"space_type":       spaceMeta(tenantsByID, info.SourceTenantID).spaceType,
		}))
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    data,
	})
}

type spaceMetaInfo struct {
	name      string
	spaceType string
}

var emptySpaceMeta = spaceMetaInfo{spaceType: types.SpaceTypeTeam}

// spaceMeta resolves a tenant's display name and space type; unknown
// tenants (e.g. shared sources outside the caller's memberships) degrade
// to empty name + default space_type.
func spaceMeta(byID map[uint64]*types.Tenant, id uint64) spaceMetaInfo {
	if t, ok := byID[id]; ok && t != nil {
		st := t.SpaceType
		if st == "" {
			st = types.SpaceTypeTeam
		}
		return spaceMetaInfo{name: t.Name, spaceType: st}
	}
	return emptySpaceMeta
}
