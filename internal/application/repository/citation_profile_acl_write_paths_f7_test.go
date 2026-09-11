//go:build cgo

package repository

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCitationProfileACLWritePathsF7InvalidateAuthoritativeMutations(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	enabledACL := newCitationProfileACLInvalidationGate(&types.CitationProfileConfig{Enabled: true})
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.TenantAPIKey{},
		&types.TenantMember{},
		&types.KnowledgeBaseShare{},
		&types.OrganizationTenantMember{},
		&types.AgentShare{},
		&types.CustomAgent{},
		&types.KnowledgeBase{},
	))
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 21, 0, 0, 0, time.UTC)

	t.Run("tenant API key revoke", func(t *testing.T) {
		tenantID := uint64(101)
		key := &types.TenantAPIKey{
			ID:         4101,
			TenantID:   &tenantID,
			ScopeType:  types.APIKeyScopeTenant,
			Name:       "f7-key",
			KeyHash:    "f7-key-hash",
			FullAccess: true,
		}
		require.NoError(t, db.Create(key).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-api-key", 101, 101, "api-key-subject", "kb-api-key", types.CitationProfileACLAccessPathOwner, "", key.ID, now)
		scope.ACLPrincipalType = types.PrincipalAPITenant
		scope.ACLPrincipalID = "101"
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantAPIKeyRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.RevokeAPIKey(ctx, tenantID, key.ID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("tenant member removal", func(t *testing.T) {
		member := &types.TenantMember{UserID: "user-member-f7", TenantID: 102, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive, JoinedAt: now}
		require.NoError(t, db.Create(member).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-member", 202, 102, member.UserID, "kb-member", types.CitationProfileACLAccessPathKBShare, "kb-member", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantMemberRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.SoftDelete(ctx, member.UserID, member.TenantID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("knowledge base share removal", func(t *testing.T) {
		share := &types.KnowledgeBaseShare{ID: "share-kb-f7", KnowledgeBaseID: "kb-share-f7", OrganizationID: "org-kb-f7", SharedByUserID: "owner", SourceTenantID: 203, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-kb-share", 203, 103, "user-kb-share", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &kbShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Delete(ctx, share.ID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("organization tenant member removal", func(t *testing.T) {
		member := &types.OrganizationTenantMember{ID: "org-member-f7", OrganizationID: "org-member-scope-f7", TenantID: 104, Role: types.OrgRoleViewer}
		require.NoError(t, db.Create(member).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-org-member", 204, member.TenantID, "user-org-member", "kb-org-member", types.CitationProfileACLAccessPathKBShare, "kb-org-member", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &organizationRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.RemoveTenantMember(ctx, member.OrganizationID, member.TenantID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("agent share removal", func(t *testing.T) {
		share := &types.AgentShare{ID: "share-agent-f7", AgentID: "agent-share-f7", OrganizationID: "org-agent-f7", SharedByUserID: "owner", SourceTenantID: 205, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-agent-share", 205, 105, "user-agent-share", "kb-agent-share", types.CitationProfileACLAccessPathAgentShare, share.AgentID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &agentShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Delete(ctx, share.ID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("custom agent removal", func(t *testing.T) {
		agent := &types.CustomAgent{ID: "agent-config-f7", TenantID: 206, Name: "f7 agent", Config: types.CustomAgentConfig{KBSelectionMode: "selected", KnowledgeBases: []string{"kb-agent-config"}}}
		require.NoError(t, db.Create(agent).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-agent-config", agent.TenantID, 106, "user-agent-config", "kb-agent-config", types.CitationProfileACLAccessPathAgentShare, agent.ID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &customAgentRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DeleteAgent(ctx, agent.ID, agent.TenantID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("knowledge base removal", func(t *testing.T) {
		kb := &types.KnowledgeBase{ID: "kb-delete-f7", TenantID: 207, Name: "f7 kb"}
		require.NoError(t, db.Create(kb).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-kb-delete", kb.TenantID, kb.TenantID, "user-kb-delete", kb.ID, types.CitationProfileACLAccessPathOwner, "", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &knowledgeBaseRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DeleteKnowledgeBase(ctx, kb.ID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})
}

func TestCitationProfileACLWritePathsF7InvalidateRemainingAuthoritativeMutations(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	enabledACL := newCitationProfileACLInvalidationGate(&types.CitationProfileConfig{Enabled: true})
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.TenantAPIKey{},
		&types.TenantMember{},
		&types.KnowledgeBaseShare{},
		&types.OrganizationTenantMember{},
		&types.AgentShare{},
		&types.CustomAgent{},
		&types.KnowledgeBase{},
	))
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 22, 0, 0, 0, time.UTC)

	t.Run("tenant API key permission update", func(t *testing.T) {
		tenantID := uint64(301)
		key := &types.TenantAPIKey{
			ID:               4301,
			TenantID:         &tenantID,
			ScopeType:        types.APIKeyScopeTenant,
			Name:             "f7-update-key",
			KeyHash:          "f7-update-key-hash",
			FullAccess:       true,
			KnowledgeBaseIDs: types.StringArray{"kb-api-update"},
		}
		require.NoError(t, db.Create(key).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-api-key-update", tenantID, tenantID, "api-key-update-subject", "kb-api-update", types.CitationProfileACLAccessPathOwner, "", key.ID, now)
		scope.ACLPrincipalType = types.PrincipalAPITenant
		scope.ACLPrincipalID = "301"
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantAPIKeyRepository{db: db, citationProfileACL: enabledACL}
		_, err := repo.UpdateAPIKey(ctx, tenantID, key.ID, &types.TenantAPIKey{
			Name:             key.Name,
			FullAccess:       false,
			KnowledgeBaseIDs: types.StringArray{"kb-no-longer-authorized"},
		})
		require.NoError(t, err)
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("tenant member create", func(t *testing.T) {
		member := &types.TenantMember{UserID: "user-member-create-f7", TenantID: 302, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive, JoinedAt: now}
		scope := citationProfileACLWritePathF7Scope("scope-write-member-create", 402, member.TenantID, member.UserID, "kb-member-create", types.CitationProfileACLAccessPathKBShare, "kb-member-create", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantMemberRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Create(ctx, member))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("tenant member role update", func(t *testing.T) {
		member := &types.TenantMember{UserID: "user-member-role-f7", TenantID: 303, Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive, JoinedAt: now}
		require.NoError(t, db.Create(member).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-member-role", 403, member.TenantID, member.UserID, "kb-member-role", types.CitationProfileACLAccessPathKBShare, "kb-member-role", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantMemberRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.UpdateRole(ctx, member.UserID, member.TenantID, types.TenantRoleViewer))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("tenant owner atomic demotion", func(t *testing.T) {
		target := &types.TenantMember{UserID: "user-owner-demote-f7", TenantID: 304, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive, JoinedAt: now}
		other := &types.TenantMember{UserID: "user-owner-demote-other-f7", TenantID: target.TenantID, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive, JoinedAt: now}
		require.NoError(t, db.Create(target).Error)
		require.NoError(t, db.Create(other).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-owner-demote", target.TenantID, target.TenantID, target.UserID, "kb-owner-demote", types.CitationProfileACLAccessPathOwner, "", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantMemberRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DemoteOwnerAtomically(ctx, target.UserID, target.TenantID, types.TenantRoleViewer))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("tenant owner atomic removal", func(t *testing.T) {
		target := &types.TenantMember{UserID: "user-owner-remove-f7", TenantID: 305, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive, JoinedAt: now}
		other := &types.TenantMember{UserID: "user-owner-remove-other-f7", TenantID: target.TenantID, Role: types.TenantRoleOwner, Status: types.TenantMemberStatusActive, JoinedAt: now}
		require.NoError(t, db.Create(target).Error)
		require.NoError(t, db.Create(other).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-owner-remove", target.TenantID, target.TenantID, target.UserID, "kb-owner-remove", types.CitationProfileACLAccessPathOwner, "", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantMemberRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.RemoveOwnerAtomically(ctx, target.UserID, target.TenantID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("knowledge base share create", func(t *testing.T) {
		share := &types.KnowledgeBaseShare{ID: "share-kb-create-f7", KnowledgeBaseID: "kb-share-create-f7", OrganizationID: "org-kb-create-f7", SharedByUserID: "owner", SourceTenantID: 406, Permission: types.OrgRoleViewer}
		scope := citationProfileACLWritePathF7Scope("scope-write-kb-share-create", share.SourceTenantID, 306, "user-kb-share-create", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &kbShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Create(ctx, share))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("knowledge base share permission update", func(t *testing.T) {
		share := &types.KnowledgeBaseShare{ID: "share-kb-update-f7", KnowledgeBaseID: "kb-share-update-f7", OrganizationID: "org-kb-update-f7", SharedByUserID: "owner", SourceTenantID: 407, Permission: types.OrgRoleEditor}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-kb-share-update", share.SourceTenantID, 307, "user-kb-share-update", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)
		share.Permission = types.OrgRoleViewer

		repo := &kbShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Update(ctx, share))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("knowledge base shares removal by knowledge base", func(t *testing.T) {
		share := &types.KnowledgeBaseShare{ID: "share-kb-delete-by-kb-f7", KnowledgeBaseID: "kb-share-delete-by-kb-f7", OrganizationID: "org-kb-delete-by-kb-f7", SharedByUserID: "owner", SourceTenantID: 408, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-kb-share-delete-by-kb", share.SourceTenantID, 308, "user-kb-share-delete-by-kb", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &kbShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DeleteByKnowledgeBaseID(ctx, share.KnowledgeBaseID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("knowledge base shares removal by organization", func(t *testing.T) {
		share := &types.KnowledgeBaseShare{ID: "share-kb-delete-by-org-f7", KnowledgeBaseID: "kb-share-delete-by-org-f7", OrganizationID: "org-kb-delete-by-org-f7", SharedByUserID: "owner", SourceTenantID: 409, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-kb-share-delete-by-org", share.SourceTenantID, 309, "user-kb-share-delete-by-org", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &kbShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DeleteByOrganizationID(ctx, share.OrganizationID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("agent share create", func(t *testing.T) {
		share := &types.AgentShare{ID: "share-agent-create-f7", AgentID: "agent-share-create-f7", OrganizationID: "org-agent-create-f7", SharedByUserID: "owner", SourceTenantID: 413, Permission: types.OrgRoleViewer}
		scope := citationProfileACLWritePathF7Scope("scope-write-agent-share-create", share.SourceTenantID, 313, "user-agent-share-create", "kb-agent-share-create", types.CitationProfileACLAccessPathAgentShare, share.AgentID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &agentShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Create(ctx, share))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("agent share permission update", func(t *testing.T) {
		share := &types.AgentShare{ID: "share-agent-update-f7", AgentID: "agent-share-update-f7", OrganizationID: "org-agent-update-f7", SharedByUserID: "owner", SourceTenantID: 414, Permission: types.OrgRoleEditor}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-agent-share-update", share.SourceTenantID, 314, "user-agent-share-update", "kb-agent-share-update", types.CitationProfileACLAccessPathAgentShare, share.AgentID, 0, now)
		require.NoError(t, db.Create(scope).Error)
		share.Permission = types.OrgRoleViewer

		repo := &agentShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Update(ctx, share))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("agent shares removal by agent", func(t *testing.T) {
		share := &types.AgentShare{ID: "share-agent-delete-by-agent-f7", AgentID: "agent-share-delete-by-agent-f7", OrganizationID: "org-agent-delete-by-agent-f7", SharedByUserID: "owner", SourceTenantID: 415, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-agent-share-delete-by-agent", share.SourceTenantID, 315, "user-agent-share-delete-by-agent", "kb-agent-share-delete-by-agent", types.CitationProfileACLAccessPathAgentShare, share.AgentID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &agentShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DeleteByAgentIDAndSourceTenant(ctx, share.AgentID, share.SourceTenantID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("agent shares removal by organization", func(t *testing.T) {
		share := &types.AgentShare{ID: "share-agent-delete-by-org-f7", AgentID: "agent-share-delete-by-org-f7", OrganizationID: "org-agent-delete-by-org-f7", SharedByUserID: "owner", SourceTenantID: 416, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-agent-share-delete-by-org", share.SourceTenantID, 316, "user-agent-share-delete-by-org", "kb-agent-share-delete-by-org", types.CitationProfileACLAccessPathAgentShare, share.AgentID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &agentShareRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DeleteByOrganizationID(ctx, share.OrganizationID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("custom agent config update", func(t *testing.T) {
		agent := &types.CustomAgent{ID: "agent-config-update-f7", TenantID: 417, Name: "f7 update agent", Config: types.CustomAgentConfig{KBSelectionMode: "selected", KnowledgeBases: []string{"kb-agent-config-update"}}}
		require.NoError(t, db.Create(agent).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-agent-config-update", agent.TenantID, 317, "user-agent-config-update", "kb-agent-config-update", types.CitationProfileACLAccessPathAgentShare, agent.ID, 0, now)
		require.NoError(t, db.Create(scope).Error)
		agent.Config.KBSelectionMode = "none"
		agent.Config.KnowledgeBases = nil

		repo := &customAgentRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.UpdateAgent(ctx, agent))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("organization tenant member add", func(t *testing.T) {
		member := &types.OrganizationTenantMember{ID: "org-member-add-f7", OrganizationID: "org-member-add-scope-f7", TenantID: 310, Role: types.OrgRoleViewer}
		share := &types.KnowledgeBaseShare{ID: "share-org-member-add-f7", KnowledgeBaseID: "kb-org-member-add-f7", OrganizationID: member.OrganizationID, SharedByUserID: "owner", SourceTenantID: 410, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-org-member-add", share.SourceTenantID, member.TenantID, "user-org-member-add", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &organizationRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.AddTenantMember(ctx, member))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("organization tenant member role update", func(t *testing.T) {
		member := &types.OrganizationTenantMember{ID: "org-member-role-f7", OrganizationID: "org-member-role-scope-f7", TenantID: 311, Role: types.OrgRoleEditor}
		share := &types.KnowledgeBaseShare{ID: "share-org-member-role-f7", KnowledgeBaseID: "kb-org-member-role-f7", OrganizationID: member.OrganizationID, SharedByUserID: "owner", SourceTenantID: 411, Permission: types.OrgRoleEditor}
		require.NoError(t, db.Create(member).Error)
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-org-member-role", share.SourceTenantID, member.TenantID, "user-org-member-role", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &organizationRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.UpdateTenantMemberRole(ctx, member.OrganizationID, member.TenantID, types.OrgRoleViewer))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("organization removal", func(t *testing.T) {
		org := &types.Organization{ID: "org-delete-f7", Name: "f7 org", OwnerID: "org-owner-f7", OwnerTenantID: 312}
		member := &types.OrganizationTenantMember{ID: "org-delete-member-f7", OrganizationID: org.ID, TenantID: 312, Role: types.OrgRoleViewer}
		share := &types.KnowledgeBaseShare{ID: "share-org-delete-f7", KnowledgeBaseID: "kb-org-delete-f7", OrganizationID: org.ID, SharedByUserID: "owner", SourceTenantID: 412, Permission: types.OrgRoleViewer}
		require.NoError(t, db.Create(org).Error)
		require.NoError(t, db.Create(member).Error)
		require.NoError(t, db.Create(share).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-org-delete", share.SourceTenantID, member.TenantID, "user-org-delete", share.KnowledgeBaseID, types.CitationProfileACLAccessPathKBShare, share.KnowledgeBaseID, 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &organizationRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.Delete(ctx, org.ID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("tenant removal", func(t *testing.T) {
		const tenantID uint64 = 418
		tenant := &types.Tenant{ID: tenantID, Name: "f7 tenant"}
		require.NoError(t, db.Create(tenant).Error)
		member := &types.TenantMember{UserID: "user-tenant-delete-f7", TenantID: tenantID, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive, JoinedAt: now}
		require.NoError(t, db.Create(member).Error)
		scope := citationProfileACLWritePathF7Scope("scope-write-tenant-delete", tenantID, tenantID, member.UserID, "kb-tenant-delete", types.CitationProfileACLAccessPathOwner, "", 0, now)
		require.NoError(t, db.Create(scope).Error)

		repo := &tenantRepository{db: db, citationProfileACL: enabledACL}
		require.NoError(t, repo.DeleteTenant(ctx, tenantID))
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
	})

	t.Run("web user removal", func(t *testing.T) {
		user := &types.User{
			ID:           "user-delete-f7",
			Username:     "user-delete-f7",
			Email:        "user-delete-f7@example.com",
			PasswordHash: "hashed",
			IsActive:     true,
		}
		require.NoError(t, db.Omit("tenant_id").Create(user).Error)
		scope := citationProfileACLWritePathF7Scope(
			"scope-write-user-delete", 519, 519, "profile-subject-user-delete", "kb-user-delete",
			types.CitationProfileACLAccessPathOwner, "", 0, now,
		)
		scope.ACLPrincipalID = user.ID
		require.NoError(t, db.Create(scope).Error)
		secondScope := citationProfileACLWritePathF7Scope(
			"scope-write-user-delete-second", 521, 521, "second-profile-subject-user-delete", "kb-user-delete-second",
			types.CitationProfileACLAccessPathOwner, "", 0, now,
		)
		secondScope.ACLPrincipalID = user.ID
		require.NoError(t, db.Create(secondScope).Error)
		differentPrincipalType := citationProfileACLWritePathF7Scope(
			"scope-write-user-delete-api-external", 522, 522, "api-external-profile-subject", "kb-user-delete-api-external",
			types.CitationProfileACLAccessPathOwner, "", 52201, now,
		)
		differentPrincipalType.ACLPrincipalType = types.PrincipalAPIExternalUser
		differentPrincipalType.ACLPrincipalID = user.ID
		require.NoError(t, db.Create(differentPrincipalType).Error)
		lockedAt := now.Add(-time.Minute)
		outbox := citationProfileACLWritePathF7Outbox(scope, "outbox-write-user-delete", "event-write-user-delete", now)
		outbox.Status = types.CitationProfileOutboxStatusDelivering
		outbox.LockedAt = &lockedAt
		outbox.LockedBy = "stale-user-delete-worker"
		require.NoError(t, db.Create(&outbox).Error)

		repo := citationProfileACLWritePathF7ConfiguredRepository(
			t, NewUserRepository, db, &types.CitationProfileConfig{Enabled: true},
		).(interfaces.UserRepository)
		require.NoError(t, repo.DeleteUser(ctx, user.ID))

		var deleted types.User
		require.NoError(t, db.Unscoped().First(&deleted, "id = ?", user.ID).Error)
		require.True(t, deleted.DeletedAt.Valid)
		requireCitationProfileACLWritePathF7Invalidated(t, db, scope)
		requireCitationProfileACLWritePathF7Invalidated(t, db, secondScope)
		var untouched types.CitationProfileScope
		require.NoError(t, db.First(&untouched, "id = ?", differentPrincipalType.ID).Error)
		require.Equal(t, types.CitationProfileACLStateCurrent, untouched.ACLCheckState)
		require.Equal(t, differentPrincipalType.ACLGeneration, untouched.ACLGeneration)
		require.Equal(t, differentPrincipalType.ProfileReadVersion, untouched.ProfileReadVersion)
		var paused types.CitationProfileEventOutbox
		require.NoError(t, db.First(&paused, "id = ?", outbox.ID).Error)
		require.Equal(t, types.CitationProfileOutboxStatusPending, paused.Status)
		require.NotNil(t, paused.RetryBudgetPausedAt)
		require.Nil(t, paused.LockedAt)
		require.Empty(t, paused.LockedBy)
	})
}

func TestCitationProfileACLWritePathsF7DeleteUserRollsBackWhenInvalidationFails(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.User{}))
	ctx := context.Background()
	now := time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC)
	user := &types.User{
		ID:           "user-delete-rollback-f7",
		Username:     "user-delete-rollback-f7",
		Email:        "user-delete-rollback-f7@example.com",
		PasswordHash: "hashed",
		IsActive:     true,
	}
	require.NoError(t, db.Omit("tenant_id").Create(user).Error)
	scope := citationProfileACLWritePathF7Scope(
		"scope-write-user-delete-rollback", 520, 520, "profile-subject-user-delete-rollback", "kb-user-delete-rollback",
		types.CitationProfileACLAccessPathOwner, "", 0, now,
	)
	scope.ACLPrincipalID = user.ID
	require.NoError(t, db.Create(scope).Error)
	outbox := citationProfileACLWritePathF7Outbox(
		scope, "outbox-write-user-delete-rollback", "event-write-user-delete-rollback", now,
	)
	lockedAt := now.Add(-time.Minute)
	outbox.Status = types.CitationProfileOutboxStatusDelivering
	outbox.LockedAt = &lockedAt
	outbox.LockedBy = "rollback-user-delete-worker"
	require.NoError(t, db.Create(&outbox).Error)
	require.NoError(t, db.Exec(`
		CREATE TRIGGER f7_fail_user_delete_outbox_pause
		BEFORE UPDATE ON citation_profile_event_outbox
		BEGIN
			SELECT RAISE(ABORT, 'forced user-delete outbox pause failure');
		END
	`).Error)

	repo := citationProfileACLWritePathF7ConfiguredRepository(
		t, NewUserRepository, db, &types.CitationProfileConfig{Enabled: true},
	).(interfaces.UserRepository)
	err := repo.DeleteUser(ctx, user.ID)
	require.ErrorContains(t, err, "forced user-delete outbox pause failure")

	var liveUser types.User
	require.NoError(t, db.First(&liveUser, "id = ?", user.ID).Error,
		"the user soft delete must roll back when ACL invalidation cannot pause its outbox")
	var storedScope types.CitationProfileScope
	require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateCurrent, storedScope.ACLCheckState)
	require.Equal(t, scope.ACLGeneration, storedScope.ACLGeneration)
	require.Equal(t, scope.ProfileReadVersion, storedScope.ProfileReadVersion)
	var storedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusDelivering, storedOutbox.Status)
	require.Nil(t, storedOutbox.RetryBudgetPausedAt)
	require.NotNil(t, storedOutbox.LockedAt)
	require.Equal(t, lockedAt.UTC(), storedOutbox.LockedAt.UTC())
	require.Equal(t, outbox.LockedBy, storedOutbox.LockedBy)
}

func TestCitationProfileACLWritePathsF7DefaultOffReadsOnlyDisabledRuntimeMarker(t *testing.T) {
	configs := []struct {
		name   string
		config *types.CitationProfileConfig
	}{
		{name: "nil config", config: nil},
		{name: "missing block zero value", config: &types.CitationProfileConfig{}},
		{name: "explicit false", config: &types.CitationProfileConfig{Enabled: false}},
	}
	mutations := []struct {
		name string
		run  func(*testing.T, *gorm.DB, *types.CitationProfileConfig) error
	}{
		{
			name: "web user",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				user := &types.User{
					ID: "default-off-user", Username: "default-off-user", Email: "default-off-user@example.com",
					PasswordHash: "hashed", IsActive: true,
				}
				require.NoError(t, db.Omit("tenant_id").Create(user).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewUserRepository, db, config,
				).(interfaces.UserRepository)
				return repo.DeleteUser(context.Background(), user.ID)
			},
		},
		{
			name: "tenant API key",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				tenantID := uint64(95101)
				key := &types.TenantAPIKey{
					ID: 9510101, TenantID: &tenantID, ScopeType: types.APIKeyScopeTenant,
					Name: "default-off-key", KeyHash: "default-off-key-hash", FullAccess: true,
				}
				require.NoError(t, db.Create(key).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewTenantAPIKeyRepository, db, config,
				).(interfaces.TenantAPIKeyRepository)
				return repo.RevokeAPIKey(context.Background(), tenantID, key.ID)
			},
		},
		{
			name: "tenant member",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				member := &types.TenantMember{
					UserID: "default-off-member", TenantID: 95102, Role: types.TenantRoleViewer,
					Status: types.TenantMemberStatusActive, JoinedAt: time.Now().UTC(),
				}
				require.NoError(t, db.Create(member).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewTenantMemberRepository, db, config,
				).(interfaces.TenantMemberRepository)
				return repo.SoftDelete(context.Background(), member.UserID, member.TenantID)
			},
		},
		{
			name: "knowledge base share",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				share := &types.KnowledgeBaseShare{
					ID: "default-off-kb-share", KnowledgeBaseID: "default-off-kb",
					OrganizationID: "default-off-org-kb", SharedByUserID: "owner",
					SourceTenantID: 95103, Permission: types.OrgRoleViewer,
				}
				require.NoError(t, db.Create(share).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewKBShareRepository, db, config,
				).(interfaces.KBShareRepository)
				return repo.Delete(context.Background(), share.ID)
			},
		},
		{
			name: "organization membership",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				member := &types.OrganizationTenantMember{
					ID: "default-off-org-member", OrganizationID: "default-off-org", TenantID: 95104,
					Role: types.OrgRoleViewer,
				}
				require.NoError(t, db.Create(member).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewOrganizationRepository, db, config,
				).(interfaces.OrganizationRepository)
				return repo.RemoveTenantMember(context.Background(), member.OrganizationID, member.TenantID)
			},
		},
		{
			name: "agent share",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				share := &types.AgentShare{
					ID: "default-off-agent-share", AgentID: "default-off-agent",
					OrganizationID: "default-off-org-agent", SharedByUserID: "owner",
					SourceTenantID: 95105, Permission: types.OrgRoleViewer,
				}
				require.NoError(t, db.Create(share).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewAgentShareRepository, db, config,
				).(interfaces.AgentShareRepository)
				return repo.Delete(context.Background(), share.ID)
			},
		},
		{
			name: "custom agent",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				agent := &types.CustomAgent{ID: "default-off-custom-agent", TenantID: 95106, Name: "default off"}
				require.NoError(t, db.Create(agent).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewCustomAgentRepository, db, config,
				).(interfaces.CustomAgentRepository)
				return repo.DeleteAgent(context.Background(), agent.ID, agent.TenantID)
			},
		},
		{
			name: "knowledge base",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				kb := &types.KnowledgeBase{ID: "default-off-delete-kb", TenantID: 95107, Name: "default off"}
				require.NoError(t, db.Create(kb).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewKnowledgeBaseRepository, db, config,
				).(interfaces.KnowledgeBaseRepository)
				return repo.DeleteKnowledgeBase(context.Background(), kb.ID)
			},
		},
		{
			name: "tenant",
			run: func(t *testing.T, db *gorm.DB, config *types.CitationProfileConfig) error {
				tenant := &types.Tenant{ID: 95108, Name: "default off"}
				require.NoError(t, db.Create(tenant).Error)
				repo := citationProfileACLWritePathF7ConfiguredRepository(
					t, NewTenantRepository, db, config,
				).(interfaces.TenantRepository)
				return repo.DeleteTenant(context.Background(), tenant.ID)
			},
		},
	}

	for _, configCase := range configs {
		t.Run(configCase.name, func(t *testing.T) {
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					db, profileSQL := newCitationProfileACLWritePathF7DefaultOffDB(t)

					err := mutation.run(t, db, configCase.config)
					if err != nil || profileSQL.Count() != 1 ||
						!strings.Contains(profileSQL.Statements(), "citation_profile_acl_runtime_state") {
						t.Fatalf(
							"stable default-off mutation must read only the runtime marker: err=%v calls=%d statements=%s",
							err, profileSQL.Count(), profileSQL.Statements(),
						)
					}
				})
			}
		})
	}
}

func citationProfileACLWritePathF7Scope(
	id string,
	sourceTenantID uint64,
	authenticatedTenantID uint64,
	subjectID string,
	kbID string,
	accessPath string,
	accessPathID string,
	apiKeyID uint64,
	now time.Time,
) *types.CitationProfileScope {
	checkedAt := now.Add(-time.Minute)
	nextCheckAt := now.Add(time.Hour)
	return &types.CitationProfileScope{
		ID:                       id,
		TenantID:                 sourceTenantID,
		SubjectID:                subjectID,
		KnowledgeBaseID:          kbID,
		SubjectEpoch:             "epoch-" + id,
		ProfileReadVersion:       11,
		ProfilePolicyVersion:     types.CitationProfilePolicyVersion,
		RetentionPolicyVersion:   types.CitationProfileRetentionPolicyVersion,
		Enabled:                  true,
		ACLCheckState:            types.CitationProfileACLStateCurrent,
		ACLCheckedAt:             &checkedAt,
		NextACLCheckAt:           &nextCheckAt,
		ACLPrincipalType:         types.PrincipalWebUser,
		ACLPrincipalID:           subjectID,
		ACLAuthenticatedTenantID: authenticatedTenantID,
		ACLAPIKeyID:              apiKeyID,
		ACLAccessPath:            accessPath,
		ACLAccessPathID:          accessPathID,
		ACLGeneration:            7,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
}

func requireCitationProfileACLWritePathF7Invalidated(t *testing.T, db *gorm.DB, original *types.CitationProfileScope) {
	t.Helper()
	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", original.ID).Error)
	require.Equal(t, original.ACLGeneration+1, stored.ACLGeneration)
	require.Equal(t, original.ProfileReadVersion+1, stored.ProfileReadVersion)
	require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)
	require.Empty(t, stored.ACLCheckLeaseToken)
	require.Nil(t, stored.ACLCheckLeaseUntil)
	require.NotNil(t, stored.NextACLCheckAt)
}

func citationProfileACLWritePathF7ConfiguredRepository(
	t *testing.T,
	constructor interface{},
	db *gorm.DB,
	config *types.CitationProfileConfig,
) interface{} {
	t.Helper()
	fn := reflect.ValueOf(constructor)
	require.Equal(t, reflect.Func, fn.Kind())
	require.GreaterOrEqual(t, fn.Type().NumIn(), 1)
	require.LessOrEqual(t, fn.Type().NumIn(), 2,
		"permission repository constructors may add only the citation-profile configuration dependency")

	args := []reflect.Value{reflect.ValueOf(db)}
	if fn.Type().NumIn() == 2 {
		configType := fn.Type().In(1)
		switch {
		case configType == reflect.TypeOf((*types.CitationProfileConfig)(nil)):
			if config == nil {
				args = append(args, reflect.Zero(configType))
			} else {
				args = append(args, reflect.ValueOf(config))
			}
		case configType == reflect.TypeOf(types.CitationProfileConfig{}):
			if config == nil {
				args = append(args, reflect.Zero(configType))
			} else {
				args = append(args, reflect.ValueOf(*config))
			}
		default:
			require.FailNow(t, "unexpected permission repository configuration dependency", configType.String())
		}
	}
	results := fn.Call(args)
	require.Len(t, results, 1)
	return results[0].Interface()
}

type citationProfileACLWritePathF7SQLCounter struct {
	mu         sync.Mutex
	statements []string
}

func (c *citationProfileACLWritePathF7SQLCounter) observe(tx *gorm.DB) {
	if c == nil || tx == nil || tx.Statement == nil {
		return
	}
	table := strings.ToLower(strings.TrimSpace(tx.Statement.Table))
	statement := strings.ToLower(strings.TrimSpace(tx.Statement.SQL.String()))
	if !strings.HasPrefix(table, "citation_profile_") &&
		!strings.Contains(statement, "citation_profile_") {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = append(c.statements, strings.TrimSpace(table+" "+statement))
}

func (c *citationProfileACLWritePathF7SQLCounter) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.statements)
}

func (c *citationProfileACLWritePathF7SQLCounter) Statements() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.statements, " | ")
}

func newCitationProfileACLWritePathF7DefaultOffDB(
	t *testing.T,
) (*gorm.DB, *citationProfileACLWritePathF7SQLCounter) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s-default-off?mode=memory&cache=shared", urlSafeTestName(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.TenantAPIKey{},
		&types.TenantMember{},
		&types.KnowledgeBaseShare{},
		&types.Organization{},
		&types.OrganizationTenantMember{},
		&types.AgentShare{},
		&types.CustomAgent{},
		&types.KnowledgeBase{},
		&types.CitationProfileACLRuntimeState{},
	))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&types.CitationProfileACLRuntimeState{
		ID: 1, Enabled: false, TransitionGeneration: 0, ChangedAt: now, UpdatedAt: now,
	}).Error)
	require.False(t, db.Migrator().HasTable(&types.CitationProfileScope{}))
	require.False(t, db.Migrator().HasTable(&types.CitationProfileEventOutbox{}))

	counter := &citationProfileACLWritePathF7SQLCounter{}
	require.NoError(t, db.Callback().Create().Before("gorm:create").
		Register("f7:default-off-topic4-create", counter.observe))
	require.NoError(t, db.Callback().Query().Before("gorm:query").
		Register("f7:default-off-topic4-query", counter.observe))
	require.NoError(t, db.Callback().Update().Before("gorm:update").
		Register("f7:default-off-topic4-update", counter.observe))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").
		Register("f7:default-off-topic4-delete", counter.observe))
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").
		Register("f7:default-off-topic4-raw", counter.observe))
	require.NoError(t, db.Callback().Row().Before("gorm:row").
		Register("f7:default-off-topic4-row", counter.observe))
	return db, counter
}

func citationProfileACLWritePathF7Outbox(
	scope *types.CitationProfileScope,
	id string,
	eventID string,
	now time.Time,
) types.CitationProfileEventOutbox {
	return types.CitationProfileEventOutbox{
		ID:              id,
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		SubjectEpoch:    scope.SubjectEpoch,
		ScopeID:         scope.ID,
		EventID:         eventID,
		Status:          types.CitationProfileOutboxStatusPending,
		NextAttemptAt:   now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
