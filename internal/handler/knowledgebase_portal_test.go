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
	// real service semantics: only the tenant's own KBs
	out := make([]*types.KnowledgeBase, 0, len(s.byTenant))
	for _, kb := range s.byTenant {
		if kb.TenantID == tenantID {
			out = append(out, kb)
		}
	}
	return out, nil
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

// --- v2 aggregation ------------------------------------------------------

type stubPortalMemberService struct {
	interfaces.TenantMemberService
	members []*types.TenantMember
}

func (s *stubPortalMemberService) ListByUser(_ context.Context, _ string) ([]*types.TenantMember, error) {
	return s.members, nil
}

type stubPortalTenantService struct {
	interfaces.TenantService
	byID map[uint64]*types.Tenant
}

func (s *stubPortalTenantService) GetTenantsByIDs(_ context.Context, ids []uint64) (map[uint64]*types.Tenant, error) {
	out := map[uint64]*types.Tenant{}
	for _, id := range ids {
		if t, ok := s.byID[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

func TestListKnowledgeBasesPortalAggregatesAllMemberships(t *testing.T) {
	// user holds: personal space 1 (owner), team space 2 (contributor);
	// a shared KB from the all-staff space (tenant 3) arrives via org share.
	svc := &stubPortalKBService{
		byTenant: []*types.KnowledgeBase{portalKB("kb-personal", 1, false), portalKB("kb-team", 2, false)},
	}
	share := &stubPortalShareService{
		shared: []*types.SharedKnowledgeBaseInfo{{
			KnowledgeBase:  portalKB("kb-public", 3, false),
			ShareID:        "share-9",
			OrganizationID: "org-9",
			OrgName:        "全员共享组",
			Permission:     types.OrgRoleViewer,
			SourceTenantID: 3,
		}},
	}
	h := &KnowledgeBaseHandler{
		service: svc, kbShareService: share,
		memberService: &stubPortalMemberService{members: []*types.TenantMember{
			{TenantID: 1, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive},
			{TenantID: 2, Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
		}},
		tenantService: &stubPortalTenantService{byID: map[uint64]*types.Tenant{
			1: {ID: 1, Name: "张三的空间", SpaceType: types.SpaceTypePersonal},
			2: {ID: 2, Name: "投研团队", SpaceType: types.SpaceTypeTeam},
			3: {ID: 3, Name: "公司公共库", SpaceType: types.SpaceTypeAll},
		}},
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Set(types.UserIDContextKey.String(), "user-1")
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(1))
		ctx = context.WithValue(ctx, types.UserIDContextKey, "user-1")
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleOwner)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.Use(middleware.ErrorHandler())
	r.GET("/api/v1/knowledge-bases", h.ListKnowledgeBases)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	status, data := getKBList(t, server.URL+"/api/v1/knowledge-bases?scope=portal")
	require.Equal(t, http.StatusOK, status)
	require.Len(t, data, 3)

	byID := map[string]map[string]interface{}{}
	for _, item := range data {
		byID[item["id"].(string)] = item
	}
	require.Contains(t, byID, "kb-personal")
	assert.Equal(t, "张三的空间", byID["kb-personal"]["tenant_name"])
	assert.Equal(t, types.SpaceTypePersonal, byID["kb-personal"]["space_type"])
	assert.Equal(t, string(types.TenantRoleOwner), byID["kb-personal"]["my_role"])
	assert.Equal(t, "投研团队", byID["kb-team"]["tenant_name"])
	assert.Equal(t, types.SpaceTypeTeam, byID["kb-team"]["space_type"])
	assert.Equal(t, string(types.TenantRoleContributor), byID["kb-team"]["my_role"])
	assert.Equal(t, true, byID["kb-public"]["shared"])
	assert.Equal(t, "公司公共库", byID["kb-public"]["tenant_name"])
	assert.Equal(t, types.SpaceTypeAll, byID["kb-public"]["space_type"])
}
