package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequireKBAccessF7SharedKBPreservesAuthenticatedTenantAndAccessProof(t *testing.T) {
	const (
		authenticatedTenantID uint64 = 100
		sourceTenantID        uint64 = 200
		apiKeyID              uint64 = 42
		kbID                         = "kb-f7-shared"
	)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "id", Value: kbID}}
	req := httptest.NewRequest("GET", "/", nil)
	ctx := context.WithValue(req.Context(), types.TenantIDContextKey, authenticatedTenantID)
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalAPITenant, ID: "100"})
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: apiKeyID})
	c.Request = req.WithContext(ctx)

	share := &stubKBShareForGuard{
		permission: map[string]types.OrgMemberRole{kbID: types.OrgRoleViewer},
		shared:     map[string]bool{kbID: true},
		source:     map[string]uint64{kbID: sourceTenantID},
	}
	guard := RequireKBAccess(
		KBIDFromParam("id"),
		types.OrgRoleViewer,
		&stubKBLookup{kbs: map[string]*types.KnowledgeBase{
			kbID: {ID: kbID, TenantID: sourceTenantID},
		}},
		share,
		nil,
		cfgRBAC(true),
	)
	guard(c)

	require.False(t, c.IsAborted())
	effectiveTenantID, ok := types.TenantIDFromContext(c.Request.Context())
	require.True(t, ok)
	require.Equal(t, sourceTenantID, effectiveTenantID)
	storedAuthenticatedTenantID, ok := c.Request.Context().Value(types.AuthenticatedTenantIDContextKey).(uint64)
	require.True(t, ok, "guard must preserve the authenticated tenant before rewriting the effective tenant")
	require.Equal(t, authenticatedTenantID, storedAuthenticatedTenantID)

	access, ok := KBAccessFromContext(c)
	require.True(t, ok)
	require.Equal(t, authenticatedTenantID, access.AuthenticatedTenantID)
	require.Equal(t, "kb_share", access.AccessPath)
	require.Equal(t, kbID, access.AccessID)
	require.Equal(
		t,
		"api_tenant_key:100:42",
		types.SessionOwnerIDFromContext(c.Request.Context()),
		"API tenant-key subject identity must stay bound to the authenticated tenant",
	)
}

func TestRequireKBAccessF7ExplicitSharedAgentStoresValidatedAccessProof(t *testing.T) {
	const (
		authenticatedTenantID uint64 = 100
		sourceTenantID        uint64 = 200
		kbID                         = "kb-f7-agent-shared"
		agentID                      = "agent-f7-validated"
	)
	agentShare := &stubAgentShareForGuard{
		agents: map[string]*types.CustomAgent{
			agentID: {
				ID:       agentID,
				TenantID: sourceTenantID,
				Config: types.CustomAgentConfig{
					KBSelectionMode: "all",
				},
			},
		},
	}
	_, c := runGuard(
		t,
		authenticatedTenantID,
		kbID,
		types.OrgRoleViewer,
		&types.KnowledgeBase{ID: kbID, TenantID: sourceTenantID},
		nil,
		guardOpts{agentID: agentID, agentShare: agentShare},
	)

	require.False(t, c.IsAborted())
	access, ok := KBAccessFromContext(c)
	require.True(t, ok)
	require.Equal(t, authenticatedTenantID, access.AuthenticatedTenantID)
	require.Equal(t, "agent_share", access.AccessPath)
	require.Equal(t, agentID, access.AccessID)
	storedAuthenticatedTenantID, ok := c.Request.Context().Value(types.AuthenticatedTenantIDContextKey).(uint64)
	require.True(t, ok)
	require.Equal(t, authenticatedTenantID, storedAuthenticatedTenantID)
}

func TestRequireKBAccessF7OwnerStoresOwnerAccessPath(t *testing.T) {
	const (
		tenantID uint64 = 100
		kbID            = "kb-f7-owned"
	)
	_, c := runGuard(
		t,
		tenantID,
		kbID,
		types.OrgRoleViewer,
		&types.KnowledgeBase{ID: kbID, TenantID: tenantID},
		nil,
		guardOpts{},
	)

	require.False(t, c.IsAborted())
	access, ok := KBAccessFromContext(c)
	require.True(t, ok)
	require.Equal(t, tenantID, access.AuthenticatedTenantID)
	require.Equal(t, "owner", access.AccessPath)
	require.Empty(t, access.AccessID)
}

func TestRequireKBAccessF7ImplicitSharedAgentInjectsConcreteValidBinding(t *testing.T) {
	const (
		authenticatedTenantID uint64 = 100
		sourceTenantID        uint64 = 200
		apiKeyID              uint64 = 42
		kbID                         = "kb-f7-agent-implicit"
		agentID                      = "agent-f7-implicit-proof"
	)
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "id", Value: kbID}}
	req := httptest.NewRequest("GET", "/", nil)
	ctx := context.WithValue(req.Context(), types.TenantIDContextKey, authenticatedTenantID)
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalAPITenant, ID: "100"})
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: apiKeyID})
	c.Request = req.WithContext(ctx)

	guard := RequireKBAccess(
		KBIDFromParam("id"),
		types.OrgRoleViewer,
		&stubKBLookup{kbs: map[string]*types.KnowledgeBase{
			kbID: {ID: kbID, TenantID: sourceTenantID},
		}},
		nil,
		&stubAgentShareForGuard{agentsByKB: map[string]*types.CustomAgent{
			kbID: {ID: agentID, TenantID: sourceTenantID},
		}},
		cfgRBAC(true),
	)
	guard(c)

	require.False(t, c.IsAborted())
	access, ok := KBAccessFromContext(c)
	require.True(t, ok)
	require.Equal(t, KBAccessPathAgentShare, access.AccessPath)
	require.Equal(t, agentID, access.AccessID)
	binding, ok := types.CitationProfileACLBindingFromContext(c.Request.Context())
	require.True(t, ok, "the middleware must inject a persistable, auditable ACL binding")
	require.Equal(t, types.CitationProfileACLAccessPathAgentShare, binding.AccessPath)
	require.Equal(t, agentID, binding.AccessPathID)
	require.Equal(t, authenticatedTenantID, binding.AuthenticatedTenantID)
	require.Equal(t, apiKeyID, binding.APIKeyID)
	require.Equal(t, "api_tenant_key:100:42", types.SessionOwnerIDFromContext(c.Request.Context()))
}
