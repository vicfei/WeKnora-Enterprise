package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterTagTreeRoutes 注册层级标签树路由。
//
// The hierarchical tag tree is KB metadata like flat tags: Viewer reads,
// owner-or-Admin writes. KBAccessRead/Write resolve own/shared access from
// the :id path parameter and rewrite the request's tenant context to the
// effective tenant for the duration of the handler.
func RegisterTagTreeRoutes(r *gin.RouterGroup, tagTreeHandler *handler.TagTreeHandler, g *rbacGuards) {
	if tagTreeHandler == nil {
		return
	}
	tagTree := g.apiKeyGroup(r.Group("/knowledge-bases/:id/tag-tree"), apiKeyIngest(apiKeyFullAccess()))
	tagTreeRead := tagTree.With(apiKeyRetrieve(apiKeyFullAccess()))
	{
		tagTreeRead.GET("", g.Viewer(), g.KBAccessRead("id"), tagTreeHandler.GetTree)
		tagTreeRead.GET("/nodes", g.Viewer(), g.KBAccessRead("id"), tagTreeHandler.ListNodes)
		tagTreeRead.GET("/nodes/:node_id", g.Viewer(), g.KBAccessRead("id"), tagTreeHandler.GetNode)
		tagTreeRead.GET("/nodes/:node_id/leaves", g.Viewer(), g.KBAccessRead("id"), tagTreeHandler.GetLeaves)
		tagTree.POST("/nodes", g.OwnedKBOrAdmin(), g.KBAccessWrite("id"), tagTreeHandler.CreateNode)
		tagTree.PUT("/nodes/:node_id", g.OwnedKBOrAdmin(), g.KBAccessWrite("id"), tagTreeHandler.UpdateNode)
		tagTree.DELETE("/nodes/:node_id", g.OwnedKBOrAdmin(), g.KBAccessWrite("id"), tagTreeHandler.DeleteNode)
		tagTree.POST("/nodes/:node_id/attach-tag", g.OwnedKBOrAdmin(), g.KBAccessWrite("id"), tagTreeHandler.AttachTagToNode)
		tagTree.POST("/nodes/:node_id/move", g.OwnedKBOrAdmin(), g.KBAccessWrite("id"), tagTreeHandler.MoveNode)
	}
}
