//go:build cgo

package repository

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileACLF7EnrollmentAfterCommittedAPIKeyRevokeNeverCreatesCurrentScope(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	repo := &citationProfileRepository{db: db}
	enabledACL := newCitationProfileACLInvalidationGate(&types.CitationProfileConfig{Enabled: true})
	const (
		tenantID uint64 = 7
		apiKeyID uint64 = 41
		kbID            = "kb-api-revoke-before-create-f7"
	)

	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID:       kbID,
		Name:     "API revoke-before-create fixture",
		TenantID: tenantID,
	}).Error)
	require.NoError(t, (&tenantAPIKeyRepository{db: db, citationProfileACL: enabledACL}).CreateAPIKey(context.Background(), &types.TenantAPIKey{
		ID:         apiKeyID,
		TenantID:   citationProfileSecurityF7Uint64Pointer(tenantID),
		ScopeType:  types.APIKeyScopeTenant,
		Name:       "revoked before first profile scope",
		KeyHash:    "hash-api-revoke-before-create-f7",
		FullAccess: true,
	}))

	// Capture exactly the server-derived request context that was valid before
	// the revocation transaction committed. There is deliberately no scope for
	// the revoke transaction to invalidate yet.
	capturedContext := citationProfileSecurityF7APIContext(tenantID, apiKeyID, kbID)
	subjectID := types.SessionOwnerIDFromContext(capturedContext)
	require.Equal(t, "api_tenant_key:7:41", subjectID)
	var countBeforeRevoke int64
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", tenantID, subjectID, kbID).
		Count(&countBeforeRevoke).Error)
	require.Zero(t, countBeforeRevoke)

	require.NoError(t, (&tenantAPIKeyRepository{db: db, citationProfileACL: enabledACL}).RevokeAPIKey(context.Background(), tenantID, apiKeyID))
	var revoked types.TenantAPIKey
	require.NoError(t, db.First(&revoked, "id = ?", apiKeyID).Error)
	require.NotNil(t, revoked.RevokedAt, "the revoke transaction must be committed before enrollment resumes")

	zero := uint64(0)
	scope, enrollErr := repo.SetEnrollment(
		capturedContext,
		tenantID,
		subjectID,
		kbID,
		true,
		&zero,
		"f7-api-revoke-before-create",
	)
	if enrollErr != nil {
		assert.Nil(t, scope, "a rejected enrollment must not return a scope")
		var currentCount int64
		require.NoError(t, db.Model(&types.CitationProfileScope{}).
			Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND acl_check_state = ?", tenantID, subjectID, kbID, types.CitationProfileACLStateCurrent).
			Count(&currentCount).Error)
		assert.Zero(t, currentCount, "a pre-revoke binding must never publish CURRENT after revocation commits")
		return
	}

	require.NotNil(t, scope)
	assert.Equal(t, types.CitationProfileACLStateUnknown, scope.ACLCheckState,
		"a new scope created from a request that raced a committed revoke must await live authority refresh")
	if assert.NotNil(t, scope.NextACLCheckAt, "a suspended new scope must be immediately refreshable") {
		assert.False(t, scope.NextACLCheckAt.After(time.Now().UTC()),
			"a suspended new scope must be due now, not after an ALLOW cache window")
	}

	_, readErr := repo.ListNodes(capturedContext, tenantID, subjectID, kbID, nil, 20)
	assert.ErrorIs(t, readErr, types.ErrCitationProfileUnavailable,
		"the race-created scope must not serve profile reads before authority refresh")

	now := time.Now().UTC()
	event := citationProfileACLTestEvent(scope, "event-api-revoke-before-create-f7", now)
	outbox := citationProfileOutboxFixture(
		"outbox-api-revoke-before-create-f7",
		event.ID,
		types.CitationProfileOutboxStatusPending,
		now.Add(-time.Minute),
		nil,
		"",
		0,
	)
	outbox.TenantID = scope.TenantID
	outbox.SubjectID = scope.SubjectID
	outbox.KnowledgeBaseID = scope.KnowledgeBaseID
	outbox.SubjectEpoch = scope.SubjectEpoch
	outbox.ScopeID = scope.ID
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claims, claimErr := repo.ClaimCitationProfileEventOutbox(
		capturedContext,
		"worker-api-revoke-before-create-f7",
		1,
		now.Add(time.Minute),
		time.Minute,
	)
	assert.NoError(t, claimErr)
	assert.Empty(t, claims, "the race-created scope must not dispatch durable work before authority refresh")
	_, resolveErr := repo.ResolveEvidenceEvent(capturedContext, tenantID, subjectID, event.ID)
	assert.ErrorIs(t, resolveErr, types.ErrCitationProfileUnavailable,
		"the race-created scope must not publish resolver output before authority refresh")

	var currentCount int64
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND acl_check_state = ?", tenantID, subjectID, kbID, types.CitationProfileACLStateCurrent).
		Count(&currentCount).Error)
	assert.Zero(t, currentCount, "a pre-revoke binding must never persist CURRENT after revocation commits")
}

func TestCitationProfileACLF7DeletedOrganizationResidualKBShareCannotAuthorizeRequestOrCurrentEnrollment(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	repo := &citationProfileRepository{db: db}
	enabledACL := newCitationProfileACLInvalidationGate(&types.CitationProfileConfig{Enabled: true})
	const (
		sourceTenantID        uint64 = 200
		authenticatedTenantID uint64 = 100
		userID                       = "user-deleted-org-residual-f7"
		organizationID               = "org-deleted-residual-f7"
		kbID                         = "kb-deleted-org-residual-f7"
	)
	now := time.Now().UTC()
	organization := &types.Organization{
		ID:            organizationID,
		Name:          "organization deleted with residual KB share",
		OwnerID:       "source-owner-deleted-org-f7",
		OwnerTenantID: sourceTenantID,
	}
	require.NoError(t, db.Create(organization).Error)
	require.NoError(t, db.Create(&types.OrganizationTenantMember{
		ID:                   "member-deleted-org-residual-f7",
		OrganizationID:       organizationID,
		TenantID:             authenticatedTenantID,
		Role:                 types.OrgRoleViewer,
		RepresentativeUserID: userID,
		JoinedAt:             &now,
	}).Error)
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID:       kbID,
		Name:     "deleted organization residual share fixture",
		TenantID: sourceTenantID,
	}).Error)
	require.NoError(t, db.Create(&types.KnowledgeBaseShare{
		ID:              "share-deleted-org-residual-f7",
		KnowledgeBaseID: kbID,
		OrganizationID:  organizationID,
		SharedByUserID:  "source-owner-deleted-org-f7",
		SourceTenantID:  sourceTenantID,
		Permission:      types.OrgRoleViewer,
	}).Error)

	// This binding models a request that passed the live guard immediately
	// before organization deletion committed.
	capturedContext := citationProfileSecurityF7WebContext(
		sourceTenantID,
		authenticatedTenantID,
		userID,
		types.CitationProfileACLAccessPathKBShare,
		kbID,
	)
	require.NoError(t, (&organizationRepository{db: db, citationProfileACL: enabledACL}).Delete(context.Background(), organizationID))

	var residualShareCount int64
	require.NoError(t, db.Model(&types.KnowledgeBaseShare{}).
		Where("knowledge_base_id = ? AND organization_id = ? AND deleted_at IS NULL", kbID, organizationID).
		Count(&residualShareCount).Error)
	require.Equal(t, int64(1), residualShareCount,
		"the fixture must preserve the service-cleanup-failure residual share")

	visibleShares, listErr := (&kbShareRepository{db: db, citationProfileACL: enabledACL}).ListByKnowledgeBase(context.Background(), kbID)
	assert.NoError(t, listErr)
	assert.Empty(t, visibleShares,
		"a request guard must not surface a residual share whose organization is deleted")

	zero := uint64(0)
	scope, enrollErr := repo.SetEnrollment(
		capturedContext,
		sourceTenantID,
		userID,
		kbID,
		true,
		&zero,
		"f7-deleted-org-residual-enrollment",
	)
	if enrollErr == nil {
		if assert.NotNil(t, scope) {
			assert.Equal(t, types.CitationProfileACLStateUnknown, scope.ACLCheckState,
				"a stale pre-delete request may only create a scope pending live authority refresh")
			if assert.NotNil(t, scope.NextACLCheckAt) {
				assert.False(t, scope.NextACLCheckAt.After(time.Now().UTC()))
			}
		}
	} else {
		assert.Nil(t, scope)
	}
	var currentCount int64
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND acl_check_state = ?", sourceTenantID, userID, kbID, types.CitationProfileACLStateCurrent).
		Count(&currentCount).Error)
	assert.Zero(t, currentCount,
		"a deleted organization residual share must never authorize a CURRENT profile scope")
}

func TestCitationProfileACLF7DenyThenLiveRegrantAllowsExplicitEnrollmentWithNewEpoch(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	repo := &citationProfileRepository{db: db}
	enabledACL := newCitationProfileACLInvalidationGate(&types.CitationProfileConfig{Enabled: true})
	now := time.Date(2026, 9, 10, 21, 0, 0, 0, time.UTC)
	const (
		tenantID uint64 = 7
		userID          = "user-deny-regrant-f7"
		kbID            = "kb-deny-regrant-f7"
	)
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID:       kbID,
		Name:     "DENY then regrant fixture",
		TenantID: tenantID,
	}).Error)

	oldScope := citationProfileACLTestScope("scope-deny-regrant-old-f7", now)
	oldScope.SubjectID = userID
	oldScope.KnowledgeBaseID = kbID
	oldScope.SubjectEpoch = "epoch-deny-regrant-old-f7"
	oldScope.ACLPrincipalID = userID
	oldScope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
	oldScope.ACLAccessPathID = ""
	require.NoError(t, db.Create(oldScope).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(context.Background(), "authority-deny-regrant-f7", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, oldScope.ID, claims[0].ScopeID)
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		context.Background(),
		&claims[0],
		types.CitationProfileACLDecisionDeny,
		now.Add(time.Second),
		time.Time{},
	))

	var terminatedOld types.CitationProfileScope
	require.NoError(t, db.Unscoped().First(&terminatedOld, "id = ?", oldScope.ID).Error)
	assert.False(t, terminatedOld.Enabled)
	assert.NotNil(t, terminatedOld.FencedAt)
	assert.Equal(t, types.CitationProfileFenceReasonACLDenied, terminatedOld.FenceReason)

	// A real grant mutation occurs after the old epoch is terminal.
	require.NoError(t, (&tenantMemberRepository{db: db, citationProfileACL: enabledACL}).Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: now.Add(2 * time.Second),
	}))

	freshContext := citationProfileSecurityF7WebContext(
		tenantID,
		tenantID,
		userID,
		types.CitationProfileACLAccessPathOwner,
		"",
	)
	zero := uint64(0)
	newScope, enrollErr := repo.SetEnrollment(
		freshContext,
		tenantID,
		userID,
		kbID,
		true,
		&zero,
		"f7-deny-regrant-new-epoch",
	)
	if !assert.NoError(t, enrollErr,
		"a live regrant plus explicit enrollment must not be permanently locked out by the denied epoch") ||
		!assert.NotNil(t, newScope) {
		return
	}
	assert.NotEqual(t, oldScope.ID, newScope.ID)
	assert.NotEqual(t, oldScope.SubjectEpoch, newScope.SubjectEpoch,
		"recovery must create a new epoch rather than resurrecting the denied epoch")
	assert.True(t, newScope.Enabled)
	assert.Nil(t, newScope.FencedAt)
	assert.Nil(t, newScope.DeletedAt)
	assert.NotEqual(t, types.CitationProfileACLStateDenied, newScope.ACLCheckState)

	var archivedOld types.CitationProfileScope
	require.NoError(t, db.Unscoped().First(&archivedOld, "id = ?", oldScope.ID).Error)
	assert.NotNil(t, archivedOld.DeletedAt,
		"explicit enrollment after a live regrant must permanently archive the denied old epoch")
	assert.False(t, archivedOld.Enabled)
	assert.NotNil(t, archivedOld.FencedAt)
	assert.Equal(t, oldScope.SubjectEpoch, archivedOld.SubjectEpoch,
		"the denied epoch must remain an immutable terminal audit record")

	var epochCount int64
	require.NoError(t, db.Unscoped().Model(&types.CitationProfileScope{}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", tenantID, userID, kbID).
		Count(&epochCount).Error)
	assert.Equal(t, int64(2), epochCount, "the terminal old epoch and fresh epoch must remain separately auditable")
}

func TestCitationProfileACLF7StaleDenyCannotOverrideLiveRegrant(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	repo := &citationProfileRepository{db: db}
	enabledACL := newCitationProfileACLInvalidationGate(&types.CitationProfileConfig{Enabled: true})
	now := time.Now().UTC()
	const (
		tenantID uint64 = 7
		userID          = "user-stale-deny-regrant-f7"
		kbID            = "kb-stale-deny-regrant-f7"
	)
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID:       kbID,
		Name:     "stale DENY after regrant fixture",
		TenantID: tenantID,
	}).Error)

	scope := citationProfileACLTestScope("scope-stale-deny-regrant-f7", now)
	scope.SubjectID = userID
	scope.KnowledgeBaseID = kbID
	scope.SubjectEpoch = "epoch-stale-deny-regrant-f7"
	scope.ACLPrincipalID = userID
	scope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
	scope.ACLAccessPathID = ""
	require.NoError(t, db.Create(scope).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(context.Background(), "authority-stale-deny-regrant-f7", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	staleDeny := claims[0]

	// The membership grant commits after authority observed absence but before
	// that old DENY result is applied. Its invalidation must revoke the lease.
	require.NoError(t, (&tenantMemberRepository{db: db, citationProfileACL: enabledACL}).Create(context.Background(), &types.TenantMember{
		UserID:   userID,
		TenantID: tenantID,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: now.Add(time.Second),
	}))
	require.ErrorIs(t, repo.ApplyCitationProfileACLResult(
		context.Background(),
		&staleDeny,
		types.CitationProfileACLDecisionDeny,
		now.Add(2*time.Second),
		time.Time{},
	), types.ErrCitationProfileACLLeaseLost,
		"a stale DENY must not override a grant mutation that committed later")

	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
	assert.True(t, stored.Enabled)
	assert.Nil(t, stored.FencedAt)
	assert.Nil(t, stored.DeletedAt)
	assert.NotEqual(t, types.CitationProfileACLStateDenied, stored.ACLCheckState)
	assert.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)
	assert.Equal(t, scope.ACLGeneration+1, stored.ACLGeneration)
	assert.Empty(t, stored.ACLCheckLeaseToken)
}

func citationProfileSecurityF7APIContext(tenantID uint64, apiKeyID uint64, kbID string) context.Context {
	principal := types.Principal{Type: types.PrincipalAPITenant, ID: strconv.FormatUint(tenantID, 10)}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	ctx = types.WithPrincipal(ctx, principal)
	ctx = types.WithAuthenticatedTenantID(ctx, tenantID)
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{
		KeyID:      apiKeyID,
		ScopeType:  types.APIKeyScopeTenant,
		FullAccess: true,
	})
	return types.WithCitationProfileACLBinding(ctx, types.CitationProfileACLBinding{
		PrincipalType:         principal.Type,
		PrincipalID:           principal.ID,
		AuthenticatedTenantID: tenantID,
		APIKeyID:              apiKeyID,
		AccessPath:            types.CitationProfileACLAccessPathOwner,
		AccessPathID:          "",
	})
}

func citationProfileSecurityF7WebContext(
	sourceTenantID uint64,
	authenticatedTenantID uint64,
	userID string,
	accessPath string,
	accessPathID string,
) context.Context {
	principal := types.Principal{Type: types.PrincipalWebUser, ID: userID}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, sourceTenantID)
	ctx = context.WithValue(ctx, types.UserIDContextKey, userID)
	ctx = types.WithPrincipal(ctx, principal)
	ctx = types.WithAuthenticatedTenantID(ctx, authenticatedTenantID)
	return types.WithCitationProfileACLBinding(ctx, types.CitationProfileACLBinding{
		PrincipalType:         principal.Type,
		PrincipalID:           principal.ID,
		AuthenticatedTenantID: authenticatedTenantID,
		AccessPath:            accessPath,
		AccessPathID:          accessPathID,
	})
}

func citationProfileSecurityF7Uint64Pointer(value uint64) *uint64 {
	return &value
}
