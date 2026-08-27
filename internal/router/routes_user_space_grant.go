package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterUserSpaceGrantRoutes mounts the v2 admin API (spaces + grants).
//
// Every route requires a JWT-authenticated system admin (g.SystemAdmin()):
// the "admin JWT passthrough" authentication mode chosen in the v2 plan
// (java_weknora_division_plan.md §3.1-A) — each mutation is attributed to
// the acting admin for the audit trail. No API-key capability is declared:
// machine callers are intentionally excluded until a separate decision.
func RegisterUserSpaceGrantRoutes(r *gin.RouterGroup, h *handler.UserSpaceGrantHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	admin := r.Group("/admin")
	{
		// spaces
		admin.GET("/spaces", g.SystemAdmin(), h.ListSpaces)
		admin.POST("/spaces", g.SystemAdmin(), h.CreateSpace)
		admin.PUT("/spaces/:tenant_id", g.SystemAdmin(), h.UpdateSpace)

		// grants
		admin.GET("/grants", g.SystemAdmin(), h.ListGrants)
		admin.POST("/grants", g.SystemAdmin(), h.CreateGrant)
		admin.GET("/grants/:id", g.SystemAdmin(), h.GetGrant)
		admin.PUT("/grants/:id", g.SystemAdmin(), h.UpdateGrant)
		admin.PUT("/grants/:id/approve", g.SystemAdmin(), h.ApproveGrant)
		admin.DELETE("/grants/:id", g.SystemAdmin(), h.RevokeGrant)
	}
}
