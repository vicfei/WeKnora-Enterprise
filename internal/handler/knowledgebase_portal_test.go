package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// --- stubs ---------------------------------------------------------------

type stubPortalKBService struct {
	interfaces.KnowledgeBaseService
	own       []*types.KnowledgeBase
	all       []*types.KnowledgeBase
	byTenant  []*types.KnowledgeBase
	allCalled bool
}

func (s *stubPortalKBService) ListKnowledgeBases(ctx context.Context) ([]*types.KnowledgeBase, error) {
	return s.own, nil
}

func (s *stubPortalKBService) ListAllKnowledgeBases(ctx context.Context) ([]*types.KnowledgeBase, error) {
	s.allCalled = true
	return s.all, nil
}

func (s *stubPortalKBService) ListKnowledgeBasesByTenantID(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBase, error) {
	return s.byTenant, nil
}

type stubPortalShareService struct {
	interfaces.KBShareService
	shared       []*types.SharedKnowledgeBaseInfo
	shareCounts  map[string]int64
	sharedCalled bool
}

func (s *stubPortalShareService) ListSharedKnowledgeBases(
	ctx context.Context, tenantID uint64, callerTenantRole types.TenantRole,
) ([]*types.SharedKnowledgeBaseInfo, error) {
	s.sharedCalled = true
	return s.shared, nil
}

func (s *stubPortalShareService) CountSharesByKnowledgeBaseIDs(ctx context.Context, kbIDs []string) (map[string]int64, error) {
	return s.shareCounts, nil
}

type stubPortalAgentShareService struct {
	interfaces.AgentShareService
}

func (s *stubPortalAgentShareService) GetSharedAgentForTenant(
	ctx context.Context, tenantID uint64, callerTenantRole types.TenantRole, agentID string, sourceTenantID ...uint64,
) (*types.CustomAgent, error) {
	return &types.CustomAgent{ID: agentID, TenantID: 7, Config: types.CustomAgentConfig{KBSelectionMode: "all"}}, nil
}

// --- scaffold ------------------------------------------------------------

func newPortalTestServer(
	t *testing.T,
	svc interfaces.KnowledgeBaseService,
	share interfaces.KBShareService,
	agentShare interfaces.AgentShareService,
	tenantID uint64,
	systemAdmin bool,
) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &KnowledgeBaseHandler{service: svc, kbShareService: share, agentShareService: agentShare}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), tenantID)
		c.Set(types.UserIDContextKey.String(), "user-1")
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, tenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, "user-1")
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, systemAdmin)
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.Use(middleware.ErrorHandler())
	r.GET("/api/v1/knowledge-bases", h.ListKnowledgeBases)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	return server
}

func getKBList(t *testing.T, url string) (int, []map[string]interface{}) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body struct {
		Success bool                     `json:"success"`
		Data    []map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.True(t, body.Success)
	return resp.StatusCode, body.Data
}

func portalKB(id string, tenantID uint64, temporary bool) *types.KnowledgeBase {
	return &types.KnowledgeBase{ID: id, Name: id, TenantID: tenantID, Type: types.KnowledgeBaseTypeDocument, IsTemporary: temporary}
}

// --- tests ---------------------------------------------------------------

func TestListKnowledgeBasesPortalSystemAdminSeesAll(t *testing.T) {
	// The stub mirrors the ListAllKnowledgeBases contract: temporary KBs
	// are already filtered out by the service (covered by the service test
	// TestListAllKnowledgeBasesExcludesTemporary).
	svc := &stubPortalKBService{all: []*types.KnowledgeBase{
		portalKB("kb-1", 1, false),
		portalKB("kb-2", 2, false),
	}}
	share := &stubPortalShareService{}
	server := newPortalTestServer(t, svc, share, nil, 1, true)

	status, data := getKBList(t, server.URL+"/api/v1/knowledge-bases?scope=portal")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, data, 2, "temporary KBs are excluded")
	assert.Equal(t, float64(1), data[0]["tenant_id"])
	assert.Equal(t, float64(2), data[1]["tenant_id"])
	assert.True(t, svc.allCalled, "system admin must use the all-tenant list")
	assert.False(t, share.sharedCalled)
}

func TestListKnowledgeBasesPortalOwnPlusShared(t *testing.T) {
	svc := &stubPortalKBService{own: []*types.KnowledgeBase{portalKB("kb-own", 1, false)}}
	share := &stubPortalShareService{
		shared: []*types.SharedKnowledgeBaseInfo{{
			KnowledgeBase:  portalKB("kb-peer", 2, false),
			ShareID:        "share-1",
			OrganizationID: "org-1",
			OrgName:        "跨部门协作组",
			Permission:     types.OrgRoleEditor,
			SourceTenantID: 2,
		}},
		shareCounts: map[string]int64{"kb-own": 1},
	}
	server := newPortalTestServer(t, svc, share, nil, 1, false)

	status, data := getKBList(t, server.URL+"/api/v1/knowledge-bases?scope=portal")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, data, 2)

	assert.Equal(t, "kb-own", data[0]["id"])
	assert.Equal(t, float64(1), data[0]["share_count"])
	assert.Nil(t, data[0]["shared"], "own-tenant entries carry no shared marker")

	assert.Equal(t, "kb-peer", data[1]["id"])
	assert.Equal(t, true, data[1]["shared"])
	assert.Equal(t, "share-1", data[1]["share_id"])
	assert.Equal(t, "跨部门协作组", data[1]["org_name"])
	assert.Equal(t, string(types.OrgRoleEditor), data[1]["permission"])
	assert.Equal(t, float64(2), data[1]["source_tenant_id"])
}

func TestListKnowledgeBasesPortalDedupesOwnTenantSharedRows(t *testing.T) {
	// A colleague shared an own-tenant KB into an organization our tenant
	// belongs to: the shared row must not duplicate the own-tenant entry.
	svc := &stubPortalKBService{own: []*types.KnowledgeBase{portalKB("kb-dup", 1, false)}}
	share := &stubPortalShareService{shared: []*types.SharedKnowledgeBaseInfo{
		{KnowledgeBase: portalKB("kb-dup", 1, false), ShareID: "share-dup", OrgName: "org", Permission: types.OrgRoleViewer},
		{KnowledgeBase: portalKB("kb-peer", 2, false), ShareID: "share-2", OrgName: "org", Permission: types.OrgRoleViewer},
	}}
	server := newPortalTestServer(t, svc, share, nil, 1, false)

	status, data := getKBList(t, server.URL+"/api/v1/knowledge-bases?scope=portal")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, data, 2)
	assert.Equal(t, "kb-dup", data[0]["id"])
	assert.Nil(t, data[0]["shared"], "own-tenant entry wins over its shared row")
	assert.Equal(t, "kb-peer", data[1]["id"])
}

func TestListKnowledgeBasesDefaultScopeUnchanged(t *testing.T) {
	svc := &stubPortalKBService{own: []*types.KnowledgeBase{portalKB("kb-own", 1, false)}}
	share := &stubPortalShareService{shared: []*types.SharedKnowledgeBaseInfo{
		{KnowledgeBase: portalKB("kb-peer", 2, false), ShareID: "share-1"},
	}}
	server := newPortalTestServer(t, svc, share, nil, 1, false)

	status, data := getKBList(t, server.URL+"/api/v1/knowledge-bases")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, data, 1, "default list stays tenant-only")
	assert.Equal(t, "kb-own", data[0]["id"])
	assert.False(t, svc.allCalled)
	assert.False(t, share.sharedCalled)
}

func TestListKnowledgeBasesAgentIDWinsOverPortalScope(t *testing.T) {
	svc := &stubPortalKBService{byTenant: []*types.KnowledgeBase{portalKB("kb-agent", 7, false)}}
	share := &stubPortalShareService{}
	server := newPortalTestServer(t, svc, share, &stubPortalAgentShareService{}, 1, false)

	status, data := getKBList(t, server.URL+"/api/v1/knowledge-bases?scope=portal&agent_id=agent-1")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, data, 1, "agent_id branch wins over scope=portal")
	assert.Equal(t, "kb-agent", data[0]["id"])
	assert.False(t, svc.allCalled)
	assert.False(t, share.sharedCalled)
}
