package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterPortalRoutes mounts the v2 portal aggregation endpoints (fork-only,
// decision 019). JWT Viewer+ only: unlike the /knowledge-bases group there is
// no apiKeyGroup wrapper, so API-key principals are fail-closed here — the
// aggregation is user-scoped (memberships ∪ org shares) and a machine key has
// no user context to aggregate for.
func RegisterPortalRoutes(r *gin.RouterGroup, h *handler.PortalListHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	portal := r.Group("/portal")
	portal.GET("/knowledge-bases", g.Viewer(), h.ListPortalKnowledgeBases)
}
