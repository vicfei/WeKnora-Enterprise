package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
)

// RegisterIdentityBridgeRoutes 注册 UUM 身份桥接路由。
//
// Machine-to-machine endpoints: every route requires a platform API key
// carrying the system_identity_bridge capability (default-deny gate). The
// platform-key gate additionally requires an X-Tenant-ID header on the
// request; that header only satisfies the gate — endpoint logic always
// uses the tenant ids from the request body.
func RegisterIdentityBridgeRoutes(r *gin.RouterGroup, identityBridgeHandler *handler.IdentityBridgeHandler, g *rbacGuards) {
	if identityBridgeHandler == nil {
		return
	}
	identity := r.Group("/identity")
	{
		g.apiKeyRoute(identity, http.MethodPost, "/bridge",
			apiKeyPlatform(types.APIKeyCapabilitySystemIdentityBridge), identityBridgeHandler.Bridge)
		g.apiKeyRoute(identity, http.MethodPost, "/disable",
			apiKeyPlatform(types.APIKeyCapabilitySystemIdentityBridge), identityBridgeHandler.Disable)
		g.apiKeyRoute(identity, http.MethodPost, "/transfer",
			apiKeyPlatform(types.APIKeyCapabilitySystemIdentityBridge), identityBridgeHandler.Transfer)
		g.apiKeyRoute(identity, http.MethodGet, "/mapping/:uum_user_id",
			apiKeyPlatform(types.APIKeyCapabilitySystemIdentityBridge), identityBridgeHandler.GetMapping)
		g.apiKeyRoute(identity, http.MethodGet, "/mapping/by-user/:user_id",
			apiKeyPlatform(types.APIKeyCapabilitySystemIdentityBridge), identityBridgeHandler.GetMappingsByUser)
		g.apiKeyRoute(identity, http.MethodPut, "/departments/:department_id",
			apiKeyPlatform(types.APIKeyCapabilitySystemIdentityBridge), identityBridgeHandler.UpsertDepartment)
	}
}
