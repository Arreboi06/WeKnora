//go:build cgo

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCitationProfileACLAuthorityF7OwnerWebUserUsesLiveDatabaseState(t *testing.T) {
	tests := []struct {
		name          string
		createKB      bool
		kbTenantID    uint64
		memberStatus  types.TenantMemberStatus
		memberDeleted bool
		want          types.CitationProfileACLDecision
	}{
		{
			name:         "source KB and active tenant member allow",
			createKB:     true,
			kbTenantID:   7,
			memberStatus: types.TenantMemberStatusActive,
			want:         types.CitationProfileACLDecisionAllow,
		},
		{
			name:         "suspended tenant member denies",
			createKB:     true,
			kbTenantID:   7,
			memberStatus: types.TenantMemberStatusSuspended,
			want:         types.CitationProfileACLDecisionDeny,
		},
		{
			name:          "soft deleted tenant member denies",
			createKB:      true,
			kbTenantID:    7,
			memberStatus:  types.TenantMemberStatusActive,
			memberDeleted: true,
			want:          types.CitationProfileACLDecisionDeny,
		},
		{
			name:         "KB in another source tenant denies",
			createKB:     true,
			kbTenantID:   8,
			memberStatus: types.TenantMemberStatusActive,
			want:         types.CitationProfileACLDecisionDeny,
		},
		{
			name:         "missing KB after successful query denies",
			createKB:     false,
			memberStatus: types.TenantMemberStatusActive,
			want:         types.CitationProfileACLDecisionDeny,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newCitationProfileACLAuthorityF7TestDB(t)
			now := time.Now().UTC()
			if tc.createKB {
				require.NoError(t, db.Create(&types.KnowledgeBase{
					ID:       "kb-owner-f7",
					Name:     "Owner authority fixture",
					TenantID: tc.kbTenantID,
				}).Error)
			}
			member := &types.TenantMember{
				UserID:   "user-owner-f7",
				TenantID: 7,
				Role:     types.TenantRoleViewer,
				Status:   tc.memberStatus,
				JoinedAt: now,
			}
			if tc.memberDeleted {
				member.DeletedAt = gorm.DeletedAt{Time: now, Valid: true}
			}
			require.NoError(t, db.Create(member).Error)

			authority := NewCitationProfileACLAuthority(db)
			decision, err := authority.CheckCitationProfileACL(
				context.Background(),
				citationProfileACLAuthorityF7WebScope(7, 7, "user-owner-f7", "kb-owner-f7", types.CitationProfileACLAccessPathOwner),
			)
			require.NoError(t, err)
			require.Equal(t, tc.want, decision.Decision)
		})
	}
}

func TestCitationProfileACLAuthorityF7DeletedWebUserDeniesWithActiveMembership(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	now := time.Now().UTC()
	const userID = "user-deleted-web-f7"
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID:       "kb-deleted-web-f7",
		Name:     "Deleted web-user authority fixture",
		TenantID: 7,
	}).Error)
	require.NoError(t, db.Create(&types.User{
		ID:           userID,
		Username:     userID,
		Email:        userID + "@example.com",
		PasswordHash: "hashed",
		TenantID:     7,
		IsActive:     true,
	}).Error)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID:   userID,
		TenantID: 7,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: now,
	}).Error)

	scope := citationProfileACLAuthorityF7WebScope(
		7,
		7,
		userID,
		"kb-deleted-web-f7",
		types.CitationProfileACLAccessPathOwner,
	)
	authority := NewCitationProfileACLAuthority(db)
	decision, err := authority.CheckCitationProfileACL(context.Background(), scope)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileACLDecisionAllow, decision.Decision)

	require.NoError(t, db.Delete(&types.User{}, "id = ?", userID).Error)
	var activeMemberships int64
	require.NoError(t, db.Model(&types.TenantMember{}).
		Where("user_id = ? AND tenant_id = ? AND status = ?", userID, 7, types.TenantMemberStatusActive).
		Count(&activeMemberships).Error)
	require.EqualValues(t, 1, activeMemberships, "the stale active membership must remain to exercise the user liveness check")

	decision, err = authority.CheckCitationProfileACL(context.Background(), scope)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileACLDecisionDeny, decision.Decision,
		"a soft-deleted web user must not be re-authorized by a surviving active membership")
}

func TestCitationProfileACLAuthorityF7TenantAPIKeyReloadsExactPersistedGrant(t *testing.T) {
	tests := []struct {
		name         string
		keyTenantID  uint64
		fullAccess   bool
		capabilities types.StringArray
		allowlist    types.StringArray
		revoked      bool
		expired      bool
		want         types.CitationProfileACLDecision
	}{
		{
			name:        "live full access key allows",
			keyTenantID: 7,
			fullAccess:  true,
			want:        types.CitationProfileACLDecisionAllow,
		},
		{
			name:         "live retrieve key with KB allowlist allows",
			keyTenantID:  7,
			capabilities: types.StringArray{string(types.APIKeyCapabilityRetrieve)},
			allowlist:    types.StringArray{"kb-api-f7"},
			want:         types.CitationProfileACLDecisionAllow,
		},
		{
			name:        "revoked exact key denies even with another live key",
			keyTenantID: 7,
			fullAccess:  true,
			revoked:     true,
			want:        types.CitationProfileACLDecisionDeny,
		},
		{
			name:        "expired key denies",
			keyTenantID: 7,
			fullAccess:  true,
			expired:     true,
			want:        types.CitationProfileACLDecisionDeny,
		},
		{
			name:        "key from wrong authenticated tenant denies",
			keyTenantID: 8,
			fullAccess:  true,
			want:        types.CitationProfileACLDecisionDeny,
		},
		{
			name:         "retrieve key outside KB allowlist denies",
			keyTenantID:  7,
			capabilities: types.StringArray{string(types.APIKeyCapabilityRetrieve)},
			allowlist:    types.StringArray{"kb-other-f7"},
			want:         types.CitationProfileACLDecisionDeny,
		},
		{
			name:         "allowlist without retrieve capability denies",
			keyTenantID:  7,
			capabilities: types.StringArray{"chat"},
			allowlist:    types.StringArray{"kb-api-f7"},
			want:         types.CitationProfileACLDecisionDeny,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newCitationProfileACLAuthorityF7TestDB(t)
			now := time.Now().UTC()
			require.NoError(t, db.Create(&types.KnowledgeBase{
				ID:       "kb-api-f7",
				Name:     "API authority fixture",
				TenantID: 7,
			}).Error)

			keyTenantID := tc.keyTenantID
			key := &types.TenantAPIKey{
				ID:               41,
				TenantID:         &keyTenantID,
				ScopeType:        types.APIKeyScopeTenant,
				Name:             "target authority key",
				KeyHash:          "hash-target-" + fmt.Sprint(tc.keyTenantID),
				APIKey:           "sk-target",
				FullAccess:       tc.fullAccess,
				KnowledgeBaseIDs: tc.allowlist,
				Capabilities:     tc.capabilities,
			}
			if tc.revoked {
				key.RevokedAt = &now
			}
			if tc.expired {
				expiresAt := now.Add(-time.Minute)
				key.ExpiresAt = &expiresAt
			}
			require.NoError(t, db.Create(key).Error)
			distractorTenantID := uint64(7)
			require.NoError(t, db.Create(&types.TenantAPIKey{
				ID:         42,
				TenantID:   &distractorTenantID,
				ScopeType:  types.APIKeyScopeTenant,
				Name:       "unbound live key",
				KeyHash:    "hash-distractor",
				APIKey:     "sk-distractor",
				FullAccess: true,
			}).Error)

			authority := NewCitationProfileACLAuthority(db)
			decision, err := authority.CheckCitationProfileACL(
				context.Background(),
				citationProfileACLAuthorityF7APIScope(7, 7, 41, "kb-api-f7"),
			)
			require.NoError(t, err)
			require.Equal(t, tc.want, decision.Decision)
		})
	}
}

func TestCitationProfileACLAuthorityF7KBShareRequiresExactLiveAccessChain(t *testing.T) {
	tests := []struct {
		name              string
		memberStatus      types.TenantMemberStatus
		memberDeleted     bool
		createOrgMember   bool
		organizationGone  bool
		shareRevoked      bool
		shareSourceTenant uint64
		want              types.CitationProfileACLDecision
	}{
		{
			name:              "active tenant member and live org share allow",
			memberStatus:      types.TenantMemberStatusActive,
			createOrgMember:   true,
			shareSourceTenant: 200,
			want:              types.CitationProfileACLDecisionAllow,
		},
		{
			name:              "suspended authenticated tenant member denies",
			memberStatus:      types.TenantMemberStatusSuspended,
			createOrgMember:   true,
			shareSourceTenant: 200,
			want:              types.CitationProfileACLDecisionDeny,
		},
		{
			name:              "deleted authenticated tenant member denies",
			memberStatus:      types.TenantMemberStatusActive,
			memberDeleted:     true,
			createOrgMember:   true,
			shareSourceTenant: 200,
			want:              types.CitationProfileACLDecisionDeny,
		},
		{
			name:              "removed organization tenant member denies",
			memberStatus:      types.TenantMemberStatusActive,
			createOrgMember:   false,
			shareSourceTenant: 200,
			want:              types.CitationProfileACLDecisionDeny,
		},
		{
			name:              "deleted organization denies",
			memberStatus:      types.TenantMemberStatusActive,
			createOrgMember:   true,
			organizationGone:  true,
			shareSourceTenant: 200,
			want:              types.CitationProfileACLDecisionDeny,
		},
		{
			name:              "exact share revocation denies",
			memberStatus:      types.TenantMemberStatusActive,
			createOrgMember:   true,
			shareRevoked:      true,
			shareSourceTenant: 200,
			want:              types.CitationProfileACLDecisionDeny,
		},
		{
			name:              "share with mismatched source tenant denies",
			memberStatus:      types.TenantMemberStatusActive,
			createOrgMember:   true,
			shareSourceTenant: 201,
			want:              types.CitationProfileACLDecisionDeny,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newCitationProfileACLAuthorityF7TestDB(t)
			now := time.Now().UTC()
			require.NoError(t, db.Create(&types.KnowledgeBase{
				ID:       "kb-shared-f7",
				Name:     "Shared authority fixture",
				TenantID: 200,
			}).Error)
			member := &types.TenantMember{
				UserID:   "user-shared-f7",
				TenantID: 100,
				Role:     types.TenantRoleViewer,
				Status:   tc.memberStatus,
				JoinedAt: now,
			}
			if tc.memberDeleted {
				member.DeletedAt = gorm.DeletedAt{Time: now, Valid: true}
			}
			require.NoError(t, db.Create(member).Error)

			organization := &types.Organization{
				ID:            "org-shared-f7",
				Name:          "Shared authority organization",
				OwnerID:       "source-owner-f7",
				OwnerTenantID: 200,
			}
			if tc.organizationGone {
				organization.DeletedAt = gorm.DeletedAt{Time: now, Valid: true}
			}
			require.NoError(t, db.Create(organization).Error)
			if tc.createOrgMember {
				require.NoError(t, db.Create(&types.OrganizationTenantMember{
					ID:                   "org-member-shared-f7",
					OrganizationID:       organization.ID,
					TenantID:             100,
					Role:                 types.OrgRoleViewer,
					RepresentativeUserID: "user-shared-f7",
					JoinedAt:             &now,
				}).Error)
			}
			share := &types.KnowledgeBaseShare{
				ID:              "share-shared-f7",
				KnowledgeBaseID: "kb-shared-f7",
				OrganizationID:  organization.ID,
				SharedByUserID:  "source-owner-f7",
				SourceTenantID:  tc.shareSourceTenant,
				Permission:      types.OrgRoleViewer,
			}
			if tc.shareRevoked {
				share.DeletedAt = gorm.DeletedAt{Time: now, Valid: true}
			}
			require.NoError(t, db.Create(share).Error)

			authority := NewCitationProfileACLAuthority(db)
			decision, err := authority.CheckCitationProfileACL(
				context.Background(),
				citationProfileACLAuthorityF7WebScope(200, 100, "user-shared-f7", "kb-shared-f7", types.CitationProfileACLAccessPathKBShare),
			)
			require.NoError(t, err)
			require.Equal(t, tc.want, decision.Decision)
		})
	}
}

func TestCitationProfileACLAuthorityF7DeletedSourceOrAuthenticatedTenantDenies(t *testing.T) {
	for _, tc := range []struct {
		name            string
		deletedTenantID uint64
	}{
		{name: "deleted source tenant", deletedTenantID: 200},
		{name: "deleted authenticated tenant", deletedTenantID: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newCitationProfileACLAuthorityF7TestDB(t)
			now := time.Now().UTC()
			require.NoError(t, db.Create(&types.KnowledgeBase{
				ID:       "kb-deleted-tenant-f7",
				Name:     "Deleted tenant authority fixture",
				TenantID: 200,
			}).Error)
			require.NoError(t, db.Create(&types.TenantMember{
				UserID:   "user-deleted-tenant-f7",
				TenantID: 100,
				Role:     types.TenantRoleViewer,
				Status:   types.TenantMemberStatusActive,
				JoinedAt: now,
			}).Error)
			organization := &types.Organization{
				ID:            "org-deleted-tenant-f7",
				Name:          "Deleted tenant authority organization",
				OwnerID:       "source-owner-deleted-tenant-f7",
				OwnerTenantID: 200,
			}
			require.NoError(t, db.Create(organization).Error)
			require.NoError(t, db.Create(&types.OrganizationTenantMember{
				ID:                   "org-member-deleted-tenant-f7",
				OrganizationID:       organization.ID,
				TenantID:             100,
				Role:                 types.OrgRoleViewer,
				RepresentativeUserID: "user-deleted-tenant-f7",
				JoinedAt:             &now,
			}).Error)
			require.NoError(t, db.Create(&types.KnowledgeBaseShare{
				ID:              "share-deleted-tenant-f7",
				KnowledgeBaseID: "kb-deleted-tenant-f7",
				OrganizationID:  organization.ID,
				SharedByUserID:  "source-owner-deleted-tenant-f7",
				SourceTenantID:  200,
				Permission:      types.OrgRoleViewer,
			}).Error)
			scope := citationProfileACLAuthorityF7WebScope(
				200,
				100,
				"user-deleted-tenant-f7",
				"kb-deleted-tenant-f7",
				types.CitationProfileACLAccessPathKBShare,
			)
			authority := NewCitationProfileACLAuthority(db)
			decision, err := authority.CheckCitationProfileACL(context.Background(), scope)
			require.NoError(t, err)
			require.Equal(t, types.CitationProfileACLDecisionAllow, decision.Decision)

			require.NoError(t, db.Delete(&types.Tenant{ID: tc.deletedTenantID}).Error)
			decision, err = authority.CheckCitationProfileACL(context.Background(), scope)
			require.NoError(t, err)
			require.Equal(t, types.CitationProfileACLDecisionDeny, decision.Decision,
				"a deleted tenant must not be re-authorized from orphaned KB, key, membership, or share rows")
		})
	}
}

func TestCitationProfileACLAuthorityF7MissingOrUnsupportedBindingIsUnknown(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	authority := NewCitationProfileACLAuthority(db)
	tests := []struct {
		name  string
		scope *types.CitationProfileScope
	}{
		{name: "nil scope", scope: nil},
		{
			name: "missing persisted binding",
			scope: &types.CitationProfileScope{
				TenantID:        7,
				KnowledgeBaseID: "kb-unknown-f7",
			},
		},
		{
			name:  "unsupported principal",
			scope: citationProfileACLAuthorityF7WebScope(7, 7, "embed-f7", "kb-unknown-f7", types.CitationProfileACLAccessPathOwner),
		},
		{
			name:  "unsupported access path",
			scope: citationProfileACLAuthorityF7WebScope(7, 7, "user-unknown-f7", "kb-unknown-f7", "future_path"),
		},
	}
	tests[2].scope.ACLPrincipalType = types.PrincipalEmbedSession

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := authority.CheckCitationProfileACL(context.Background(), tc.scope)
			require.NoError(t, err)
			require.Equal(t, types.CitationProfileACLDecisionUnknown, decision.Decision)
		})
	}
}

func TestCitationProfileACLAuthorityF7DatabaseErrorNeverBecomesDeny(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	authority := NewCitationProfileACLAuthority(db)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	decision, checkErr := authority.CheckCitationProfileACL(
		context.Background(),
		citationProfileACLAuthorityF7WebScope(7, 7, "user-db-error-f7", "kb-db-error-f7", types.CitationProfileACLAccessPathOwner),
	)
	require.Error(t, checkErr)
	require.NotEqual(t, types.CitationProfileACLDecisionDeny, decision.Decision, "an authority read failure is not proof of revocation")
}

func newCitationProfileACLAuthorityF7TestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newCitationProfileRepositoryTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.User{},
		&types.KnowledgeBase{},
		&types.TenantMember{},
		&types.TenantAPIKey{},
		&types.Organization{},
		&types.OrganizationTenantMember{},
		&types.KnowledgeBaseShare{},
	))
	require.NoError(t, db.Create(&[]types.Tenant{
		{ID: 7, Name: "Tenant 7", Status: "active"},
		{ID: 8, Name: "Tenant 8", Status: "active"},
		{ID: 100, Name: "Tenant 100", Status: "active"},
		{ID: 200, Name: "Tenant 200", Status: "active"},
		{ID: 201, Name: "Tenant 201", Status: "active"},
	}).Error)
	require.NoError(t, db.Create(&[]types.User{
		{ID: "user-owner-f7", Username: "user-owner-f7", Email: "user-owner-f7@example.com", PasswordHash: "hashed", TenantID: 7, IsActive: true},
		{ID: "user-shared-f7", Username: "user-shared-f7", Email: "user-shared-f7@example.com", PasswordHash: "hashed", TenantID: 100, IsActive: true},
		{ID: "user-deleted-tenant-f7", Username: "user-deleted-tenant-f7", Email: "user-deleted-tenant-f7@example.com", PasswordHash: "hashed", TenantID: 100, IsActive: true},
	}).Error)
	return db
}

func citationProfileACLAuthorityF7WebScope(
	sourceTenantID uint64,
	authenticatedTenantID uint64,
	userID string,
	kbID string,
	accessPath string,
) *types.CitationProfileScope {
	accessPathID := ""
	if accessPath == types.CitationProfileACLAccessPathKBShare {
		accessPathID = kbID
	}
	return &types.CitationProfileScope{
		ID:                       "scope-authority-f7",
		TenantID:                 sourceTenantID,
		SubjectID:                userID,
		KnowledgeBaseID:          kbID,
		SubjectEpoch:             "epoch-authority-f7",
		Enabled:                  true,
		ACLPrincipalType:         types.PrincipalWebUser,
		ACLPrincipalID:           userID,
		ACLAuthenticatedTenantID: authenticatedTenantID,
		ACLAccessPath:            accessPath,
		ACLAccessPathID:          accessPathID,
		ACLGeneration:            1,
	}
}

func citationProfileACLAuthorityF7APIScope(
	sourceTenantID uint64,
	authenticatedTenantID uint64,
	apiKeyID uint64,
	kbID string,
) *types.CitationProfileScope {
	return &types.CitationProfileScope{
		ID:                       "scope-api-authority-f7",
		TenantID:                 sourceTenantID,
		SubjectID:                fmt.Sprintf("%s%d:%d", types.SessionOwnerAPITenantKeyPrefix, authenticatedTenantID, apiKeyID),
		KnowledgeBaseID:          kbID,
		SubjectEpoch:             "epoch-api-authority-f7",
		Enabled:                  true,
		ACLPrincipalType:         types.PrincipalAPITenant,
		ACLPrincipalID:           fmt.Sprint(authenticatedTenantID),
		ACLAuthenticatedTenantID: authenticatedTenantID,
		ACLAPIKeyID:              apiKeyID,
		ACLAccessPath:            types.CitationProfileACLAccessPathOwner,
		ACLGeneration:            1,
	}
}
