//go:build cgo

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileACLMutationF7InvalidatesExactScopeAndRejectsStaleAllow(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)

	target := citationProfileACLMutationF7WebScope("scope-00-target", 200, 100, "user-a", "kb-a", now)
	otherKB := citationProfileACLMutationF7WebScope("scope-10-other-kb", 200, 100, "user-a", "kb-b", now)
	otherSubject := citationProfileACLMutationF7WebScope("scope-20-other-subject", 200, 100, "user-b", "kb-a", now)
	otherAPIKey := citationProfileACLMutationF7APIScope("scope-30-other-api-key", 200, 100, 42, "kb-a", now)
	scopes := []*types.CitationProfileScope{target, otherKB, otherSubject, otherAPIKey}
	for _, scope := range scopes {
		require.NoError(t, db.Create(scope).Error)
	}

	lockedAt := now.Add(-time.Minute)
	outboxes := make([]types.CitationProfileEventOutbox, 0, len(scopes))
	for i, scope := range scopes {
		outbox := citationProfileOutboxFixture(
			fmt.Sprintf("outbox-mutation-f7-%d", i),
			fmt.Sprintf("event-mutation-f7-%d", i),
			types.CitationProfileOutboxStatusDelivering,
			now.Add(-time.Minute),
			&lockedAt,
			"old-resolution-worker",
			2,
		)
		outbox.TenantID = scope.TenantID
		outbox.SubjectID = scope.SubjectID
		outbox.KnowledgeBaseID = scope.KnowledgeBaseID
		outbox.SubjectEpoch = scope.SubjectEpoch
		outbox.ScopeID = scope.ID
		outboxes = append(outboxes, outbox)
	}
	require.NoError(t, db.Create(&outboxes).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(context.Background(), "authority-before-mutation", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, target.ID, claims[0].ScopeID)
	staleClaim := claims[0]
	require.NotEmpty(t, staleClaim.LeaseToken)

	err = repo.InvalidateCitationProfileACL(context.Background(), types.CitationProfileACLMutation{
		PrincipalType:         types.PrincipalWebUser,
		PrincipalID:           "user-a",
		AuthenticatedTenantID: 100,
		SourceTenantID:        200,
		KnowledgeBaseID:       "kb-a",
		AccessPath:            types.CitationProfileACLAccessPathKBShare,
		AccessPathID:          "kb-a",
	})
	require.NoError(t, err)

	err = repo.ApplyCitationProfileACLResult(
		context.Background(),
		&staleClaim,
		types.CitationProfileACLDecisionAllow,
		now.Add(time.Second),
		now.Add(time.Hour),
	)
	require.ErrorIs(t, err, types.ErrCitationProfileACLLeaseLost, "an ALLOW read before the grant mutation must be fenced")

	var storedTarget types.CitationProfileScope
	require.NoError(t, db.First(&storedTarget, "id = ?", target.ID).Error)
	require.Equal(t, target.ACLGeneration+1, storedTarget.ACLGeneration)
	require.Equal(t, target.ProfileReadVersion+2, storedTarget.ProfileReadVersion)
	require.Equal(t, types.CitationProfileACLStateUnknown, storedTarget.ACLCheckState)
	require.Empty(t, storedTarget.ACLCheckLeaseToken)
	require.Nil(t, storedTarget.ACLCheckLeaseUntil)
	require.NotNil(t, storedTarget.NextACLCheckAt)
	require.True(t, storedTarget.Enabled)
	require.Nil(t, storedTarget.FencedAt)

	var storedTargetOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedTargetOutbox, "id = ?", outboxes[0].ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, storedTargetOutbox.Status)
	require.Nil(t, storedTargetOutbox.LockedAt)
	require.Empty(t, storedTargetOutbox.LockedBy)
	require.Equal(t, 2, storedTargetOutbox.AttemptCount, "ACL invalidation must not spend evidence attempts")
	pausedAt, _ := citationProfileACLRetryBudget(t, db, storedTargetOutbox.ID)
	require.True(t, pausedAt.Valid)

	for i, original := range scopes[1:] {
		var stored types.CitationProfileScope
		require.NoError(t, db.First(&stored, "id = ?", original.ID).Error)
		require.Equal(t, original.ACLGeneration, stored.ACLGeneration, original.ID)
		require.Equal(t, original.ProfileReadVersion, stored.ProfileReadVersion, original.ID)
		require.Equal(t, types.CitationProfileACLStateCurrent, stored.ACLCheckState, original.ID)

		var storedOutbox types.CitationProfileEventOutbox
		require.NoError(t, db.First(&storedOutbox, "id = ?", outboxes[i+1].ID).Error)
		require.Equal(t, types.CitationProfileOutboxStatusDelivering, storedOutbox.Status, original.ID)
		require.Equal(t, "old-resolution-worker", storedOutbox.LockedBy, original.ID)
		unaffectedPausedAt, _ := citationProfileACLRetryBudget(t, db, storedOutbox.ID)
		require.False(t, unaffectedPausedAt.Valid, original.ID)
	}
}

func TestCitationProfileACLMutationF7ZeroFieldsAreWildcardsButEmptyMutationIsInvalid(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, time.UTC)

	key41KBOne := citationProfileACLMutationF7APIScope("scope-key-41-a", 7, 7, 41, "kb-a", now)
	key41KBTwo := citationProfileACLMutationF7APIScope("scope-key-41-b", 7, 7, 41, "kb-b", now)
	key42 := citationProfileACLMutationF7APIScope("scope-key-42", 7, 7, 42, "kb-a", now)
	for _, scope := range []*types.CitationProfileScope{key41KBOne, key41KBTwo, key42} {
		require.NoError(t, db.Create(scope).Error)
	}

	require.NoError(t, repo.InvalidateCitationProfileACL(context.Background(), types.CitationProfileACLMutation{
		APIKeyID: 41,
	}))

	for _, original := range []*types.CitationProfileScope{key41KBOne, key41KBTwo} {
		var stored types.CitationProfileScope
		require.NoError(t, db.First(&stored, "id = ?", original.ID).Error)
		require.Equal(t, original.ACLGeneration+1, stored.ACLGeneration, original.ID)
		require.Equal(t, original.ProfileReadVersion+1, stored.ProfileReadVersion, original.ID)
		require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState, original.ID)
	}

	var storedKey42 types.CitationProfileScope
	require.NoError(t, db.First(&storedKey42, "id = ?", key42.ID).Error)
	require.Equal(t, key42.ACLGeneration, storedKey42.ACLGeneration)
	require.Equal(t, key42.ProfileReadVersion, storedKey42.ProfileReadVersion)
	require.Equal(t, types.CitationProfileACLStateCurrent, storedKey42.ACLCheckState)

	err := repo.InvalidateCitationProfileACL(context.Background(), types.CitationProfileACLMutation{})
	require.ErrorIs(t, err, types.ErrCitationProfileInvalidRequest)

	var afterRejectedEmpty types.CitationProfileScope
	require.NoError(t, db.First(&afterRejectedEmpty, "id = ?", key41KBOne.ID).Error)
	require.Equal(t, key41KBOne.ACLGeneration+1, afterRejectedEmpty.ACLGeneration, "a rejected empty mutation must not invalidate all scopes again")
}

func TestCitationProfileBlindDeleteF7FindsSharedSourceScopeByAuthenticatedTenantAndSubject(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)

	target := citationProfileACLMutationF7WebScope("scope-shared-blind-delete", 200, 100, "user-shared", "kb-shared", now)
	otherSubject := citationProfileACLMutationF7WebScope("scope-other-subject-blind-delete", 200, 100, "user-other", "kb-shared", now)
	require.NoError(t, db.Create(target).Error)
	require.NoError(t, db.Create(otherSubject).Error)

	operation, err := repo.RequestBlindDelete(context.Background(), 100, target.SubjectID, target.KnowledgeBaseID)
	require.NoError(t, err)
	require.NotNil(t, operation)
	require.Equal(t, types.CitationOperationDeleteBlind, operation.OperationType)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, operation.Status)
	require.Equal(t, target.ID, operation.ScopeID, "fallback must return a receipt bound to the actual source-tenant scope")
	require.Equal(t, target.TenantID, operation.TenantID)

	var deletedTarget types.CitationProfileScope
	require.NoError(t, db.Unscoped().First(&deletedTarget, "id = ?", target.ID).Error)
	require.False(t, deletedTarget.Enabled)
	require.NotNil(t, deletedTarget.FencedAt)
	require.NotNil(t, deletedTarget.DeletedAt)
	require.Equal(t, types.CitationOperationDeleteBlind, deletedTarget.FenceReason)
	require.Equal(t, target.ProfileReadVersion+1, deletedTarget.ProfileReadVersion)

	var untouchedOtherSubject types.CitationProfileScope
	require.NoError(t, db.First(&untouchedOtherSubject, "id = ?", otherSubject.ID).Error)
	require.True(t, untouchedOtherSubject.Enabled)
	require.Nil(t, untouchedOtherSubject.FencedAt)
	require.Nil(t, untouchedOtherSubject.DeletedAt)
	require.Equal(t, otherSubject.ProfileReadVersion, untouchedOtherSubject.ProfileReadVersion)
}

func citationProfileACLMutationF7WebScope(
	id string,
	sourceTenantID uint64,
	authenticatedTenantID uint64,
	userID string,
	kbID string,
	now time.Time,
) *types.CitationProfileScope {
	dueAt := now.Add(-time.Minute)
	checkedAt := now.Add(-time.Hour)
	return &types.CitationProfileScope{
		ID:                       id,
		TenantID:                 sourceTenantID,
		SubjectID:                userID,
		KnowledgeBaseID:          kbID,
		SubjectEpoch:             "epoch-" + id,
		ProfileReadVersion:       17,
		ProfilePolicyVersion:     types.CitationProfilePolicyVersion,
		RetentionPolicyVersion:   types.CitationProfileRetentionPolicyVersion,
		Enabled:                  true,
		ACLCheckState:            types.CitationProfileACLStateCurrent,
		ACLCheckedAt:             &checkedAt,
		NextACLCheckAt:           &dueAt,
		ACLPrincipalType:         types.PrincipalWebUser,
		ACLPrincipalID:           userID,
		ACLAuthenticatedTenantID: authenticatedTenantID,
		ACLAccessPath:            types.CitationProfileACLAccessPathKBShare,
		ACLAccessPathID:          kbID,
		ACLGeneration:            9,
		CreatedAt:                now.Add(-time.Hour),
		UpdatedAt:                now.Add(-time.Hour),
	}
}

func citationProfileACLMutationF7APIScope(
	id string,
	sourceTenantID uint64,
	authenticatedTenantID uint64,
	apiKeyID uint64,
	kbID string,
	now time.Time,
) *types.CitationProfileScope {
	scope := citationProfileACLMutationF7WebScope(
		id,
		sourceTenantID,
		authenticatedTenantID,
		fmt.Sprintf("%s%d:%d", types.SessionOwnerAPITenantKeyPrefix, authenticatedTenantID, apiKeyID),
		kbID,
		now,
	)
	scope.ACLPrincipalType = types.PrincipalAPITenant
	scope.ACLPrincipalID = fmt.Sprint(authenticatedTenantID)
	scope.ACLAPIKeyID = apiKeyID
	return scope
}
