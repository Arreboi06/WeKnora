//go:build cgo

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCitationProfileACLRepositoryClaimsAndAppliesNonDestructiveDecisions(t *testing.T) {
	tests := []struct {
		name      string
		decision  types.CitationProfileACLDecision
		wantState string
	}{
		{name: "allow refreshes current", decision: types.CitationProfileACLDecisionAllow, wantState: types.CitationProfileACLStateCurrent},
		{name: "unknown suspends", decision: types.CitationProfileACLDecisionUnknown, wantState: types.CitationProfileACLStateUnknown},
		{name: "error suspends", decision: types.CitationProfileACLDecisionError, wantState: types.CitationProfileACLStateError},
		{name: "timeout suspends", decision: types.CitationProfileACLDecisionTimeout, wantState: types.CitationProfileACLStateTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newCitationProfileRepositoryTestDB(t)
			repo := &citationProfileRepository{db: db}
			now := time.Now().UTC()
			nextCheckAt := now.Add(5 * time.Minute)
			scope := citationProfileACLTestScope("scope-f7-"+tt.wantState, now)
			outbox := citationProfileOutboxFixture(
				"outbox-f7-"+tt.wantState,
				"event-f7-"+tt.wantState,
				types.CitationProfileOutboxStatusPending,
				now.Add(-time.Minute),
				nil,
				"",
				0,
			)
			outbox.ScopeID = scope.ID
			outbox.SubjectEpoch = scope.SubjectEpoch
			require.NoError(t, db.Create(scope).Error)
			require.NoError(t, db.Create(&outbox).Error)

			claims, err := repo.ClaimCitationProfileACLScopes(
				context.Background(), "acl-worker-f7", 1, now, time.Minute,
			)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			claim := &claims[0]
			require.Equal(t, scope.ID, claim.ScopeID)
			require.NotEmpty(t, claim.LeaseToken)
			require.Equal(t, scope.ACLGeneration, claim.Generation)

			var leased types.CitationProfileScope
			require.NoError(t, db.First(&leased, "id = ?", scope.ID).Error)
			require.Equal(t, claim.LeaseToken, leased.ACLCheckLeaseToken)
			require.NotNil(t, leased.ACLCheckLeaseUntil)

			require.NoError(t, repo.ApplyCitationProfileACLResult(
				context.Background(), claim, tt.decision, now.Add(time.Second), nextCheckAt,
			))

			var stored types.CitationProfileScope
			require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
			require.Equal(t, tt.wantState, stored.ACLCheckState)
			require.Empty(t, stored.ACLCheckLeaseToken)
			require.Nil(t, stored.ACLCheckLeaseUntil)
			require.NotNil(t, stored.ACLCheckedAt)
			require.NotNil(t, stored.NextACLCheckAt)
			if tt.decision == types.CitationProfileACLDecisionAllow {
				require.WithinDuration(t, nextCheckAt, *stored.NextACLCheckAt, time.Millisecond)
			} else {
				require.WithinDuration(t, nextCheckAt, *stored.NextACLCheckAt, 2*time.Second,
					"retry delay may be requested by a worker, but its absolute timestamp is derived from DB time")
			}
			require.True(t, stored.Enabled)
			require.Nil(t, stored.FencedAt)
			require.Nil(t, stored.DeletedAt)

			var storedOutbox types.CitationProfileEventOutbox
			require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
			require.Equal(t, types.CitationProfileOutboxStatusPending, storedOutbox.Status)
			require.Zero(t, storedOutbox.AttemptCount, "ACL checks must not consume evidence retry attempts")
			require.Nil(t, storedOutbox.DeadletterAt, "non-DENY ACL results must never discard evidence work")
		})
	}
}

func TestCitationProfileACLExpiredCurrentFailsClosedAndClaimPausesBeforeAuthority(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC().Truncate(time.Second)
	scope := citationProfileACLTestScope("scope-f7-expired-current", now)
	checkedAt := now.Add(-time.Hour)
	scope.ACLCheckedAt = &checkedAt
	scope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
	outbox := citationProfileOutboxFixture(
		"outbox-f7-expired-current", "event-f7-expired-current", types.CitationProfileOutboxStatusPending,
		now.Add(-time.Minute), nil, "", 0,
	)
	outbox.ScopeID = scope.ID
	outbox.SubjectEpoch = scope.SubjectEpoch
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&outbox).Error)

	_, err := repo.ListNodes(context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, nil, 20)
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable,
		"an expired CURRENT decision must stop serving before a worker claims it")
	evidenceClaims, err := repo.ClaimCitationProfileEventOutbox(
		context.Background(), "evidence-worker-f7-expired", 1, now, time.Minute,
	)
	require.NoError(t, err)
	require.Empty(t, evidenceClaims, "expired CURRENT must not start evidence work")

	claims, err := repo.ClaimCitationProfileACLScopes(
		context.Background(), "acl-worker-f7-expired", 1, now, time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, scope.ID, claims[0].ScopeID)
	require.Equal(t, types.CitationProfileACLStateUnknown, claims[0].Scope.ACLCheckState)
	require.Equal(t, scope.ProfileReadVersion+1, claims[0].Scope.ProfileReadVersion)

	var checking types.CitationProfileScope
	require.NoError(t, db.First(&checking, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, checking.ACLCheckState,
		"claim must close CURRENT in the same transaction that grants the authority lease")
	require.Equal(t, scope.ProfileReadVersion+1, checking.ProfileReadVersion)
	pausedAt, pausedSeconds := citationProfileACLRetryBudget(t, db, outbox.ID)
	require.True(t, pausedAt.Valid)
	require.Equal(t, scope.NextACLCheckAt.UTC(), pausedAt.Time.UTC())
	require.Zero(t, pausedSeconds)

	allowAt := now.Add(time.Second)
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		context.Background(), &claims[0], types.CitationProfileACLDecisionAllow,
		allowAt, now.Add(time.Hour),
	))
	var allowed types.CitationProfileScope
	require.NoError(t, db.First(&allowed, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateCurrent, allowed.ACLCheckState)
	require.Equal(t, scope.ProfileReadVersion+2, allowed.ProfileReadVersion)
	pausedAt, _ = citationProfileACLRetryBudget(t, db, outbox.ID)
	require.False(t, pausedAt.Valid)
}

func TestCitationProfileACLExpiredLeaseRejectsEveryAuthorityResult(t *testing.T) {
	for _, decision := range []types.CitationProfileACLDecision{
		types.CitationProfileACLDecisionAllow,
		types.CitationProfileACLDecisionDeny,
		types.CitationProfileACLDecisionUnknown,
	} {
		t.Run(string(decision), func(t *testing.T) {
			db := newCitationProfileRepositoryTestDB(t)
			repo := &citationProfileRepository{db: db}
			claimAt := time.Now().UTC()
			scope := citationProfileACLTestScope("scope-f7-expired-lease-"+string(decision), claimAt)
			require.NoError(t, db.Create(scope).Error)

			claims, err := repo.ClaimCitationProfileACLScopes(
				context.Background(), "acl-worker-f7-expired-lease", 1, claimAt, time.Minute,
			)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			leaseToken := claims[0].LeaseToken
			expiredLeaseAt := time.Now().UTC().Add(-time.Second)
			require.NoError(t, db.Model(&types.CitationProfileScope{}).
				Where("id = ?", scope.ID).
				Update("acl_check_lease_until", expiredLeaseAt).Error)
			applyAt := claimAt.Add(time.Minute)
			nextCheckAt := time.Now().UTC().Add(time.Hour)
			if decision == types.CitationProfileACLDecisionDeny {
				nextCheckAt = time.Time{}
			}
			err = repo.ApplyCitationProfileACLResult(
				context.Background(), &claims[0], decision, applyAt, nextCheckAt,
			)
			require.ErrorIs(t, err, types.ErrCitationProfileACLLeaseLost,
				"authority results are stale once the exclusive lease deadline is reached")

			var stored types.CitationProfileScope
			require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
			require.True(t, stored.Enabled)
			require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)
			require.Equal(t, leaseToken, stored.ACLCheckLeaseToken)
			require.Equal(t, claims[0].Generation, stored.ACLGeneration)
		})
	}
}

func TestCitationProfileACLDelayedClaimBackdatesRetryPauseToAuthorizationExpiry(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	claimAt := time.Now().UTC()
	expiredAt := claimAt.Add(-2 * time.Hour)
	checkedAt := expiredAt.Add(-time.Hour)
	scope := citationProfileTestScope("scope-f7-delayed-expiry", "user-a", "kb-a", "epoch-a", 11, 1)
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &expiredAt
	outbox := citationProfileOutboxFixture(
		"outbox-f7-delayed-expiry",
		"event-f7-delayed-expiry",
		types.CitationProfileOutboxStatusPending,
		claimAt.Add(-3*time.Hour),
		nil,
		"",
		0,
	)
	outbox.ScopeID = scope.ID
	require.True(t, outbox.CreatedAt.Before(expiredAt))
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(
		context.Background(), "acl-worker-f7-delayed-expiry", 1, claimAt, time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	pausedAt, pausedSeconds := citationProfileACLRetryBudget(t, db, outbox.ID)
	require.True(t, pausedAt.Valid)
	require.WithinDuration(t, expiredAt, pausedAt.Time, time.Millisecond,
		"retry max-age must stop when CURRENT authorization expires, not when a delayed worker finally claims")
	require.Zero(t, pausedSeconds)

	allowAt := claimAt.Add(10 * time.Second)
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		context.Background(), &claims[0], types.CitationProfileACLDecisionAllow,
		allowAt, allowAt.Add(time.Hour),
	))
	pausedAt, pausedSeconds = citationProfileACLRetryBudget(t, db, outbox.ID)
	require.False(t, pausedAt.Valid)
	require.GreaterOrEqual(t, pausedSeconds, int64((2*time.Hour-time.Second)/time.Second))
}

func TestCitationProfileACLClaimPrefersOldestDueAcrossIDs(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC().Truncate(time.Second)

	newerLowID := citationProfileACLTestScope("00000000-0000-0000-0000-000000000001", now)
	newerLowID.SubjectID = "user-f7-newer-low-id"
	newerLowID.KnowledgeBaseID = "kb-f7-newer-low-id"
	newerDue := now.Add(-time.Minute)
	newerLowID.NextACLCheckAt = &newerDue
	olderHighID := citationProfileACLTestScope("ffffffff-ffff-ffff-ffff-ffffffffffff", now)
	olderHighID.SubjectID = "user-f7-older-high-id"
	olderHighID.KnowledgeBaseID = "kb-f7-older-high-id"
	olderDue := now.Add(-time.Hour)
	olderHighID.NextACLCheckAt = &olderDue
	require.NoError(t, db.Create(&[]types.CitationProfileScope{*newerLowID, *olderHighID}).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(
		context.Background(), "acl-worker-f7-oldest", 1, now, time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, olderHighID.ID, claims[0].ScopeID,
		"repeatedly due low IDs must not starve an older high-ID authority refresh")
}

func TestCitationProfileACLRepositoryRejectsStaleAllowByLeaseAndGeneration(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	scope := citationProfileACLTestScope("scope-f7-stale-allow", now)
	scope.ACLGeneration = 9
	require.NoError(t, db.Create(scope).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(context.Background(), "acl-worker-old", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	staleClaim := &claims[0]
	require.Equal(t, uint64(9), staleClaim.Generation)

	newLeaseUntil := now.Add(2 * time.Minute)
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("id = ?", scope.ID).
		Updates(map[string]interface{}{
			"acl_generation":        gorm.Expr("acl_generation + 1"),
			"acl_check_state":       types.CitationProfileACLStateUnknown,
			"acl_check_lease_token": "newer-authority-change",
			"acl_check_lease_until": newLeaseUntil,
			"profile_read_version":  gorm.Expr("profile_read_version + 1"),
		}).Error)

	err = repo.ApplyCitationProfileACLResult(
		context.Background(), staleClaim, types.CitationProfileACLDecisionAllow,
		now.Add(3*time.Second), now.Add(10*time.Minute),
	)
	require.Error(t, err, "a stale ALLOW must not overwrite a newer ACL invalidation")

	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)
	require.Equal(t, uint64(10), stored.ACLGeneration)
	require.Equal(t, "newer-authority-change", stored.ACLCheckLeaseToken)
	require.Equal(t, scope.ProfileReadVersion+2, stored.ProfileReadVersion)
	require.Nil(t, stored.FencedAt)
}

func TestCitationProfileACLRepositoryDenyFencesWorkAndRevokesExports(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 15, 0, 0, 0, time.UTC)
	scope := citationProfileACLTestScope("scope-f7-deny", now)
	scope.PendingEventCount = 1
	event := citationProfileACLTestEvent(scope, "event-f7-deny", now.Add(-time.Minute))
	outbox := citationProfileOutboxFixture(
		"outbox-f7-deny", event.ID, types.CitationProfileOutboxStatusPending,
		now.Add(-time.Minute), nil, "", 0,
	)
	outbox.ScopeID = scope.ID
	outbox.SubjectEpoch = scope.SubjectEpoch
	ready := citationProfileACLTestExport(scope, "export-f7-ready", types.CitationProfileOperationStatusReady, now)
	ready.ArtifactURI = "db:result_summary"
	preparing := citationProfileACLTestExport(scope, "export-f7-preparing", types.CitationProfileOperationStatusPreparing, now)
	completed := citationProfileACLTestExport(scope, "export-f7-completed", types.CitationProfileOperationStatusExpired, now)
	completedAt := now.Add(-time.Minute)
	completed.CompletedAt = &completedAt

	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&outbox).Error)
	require.NoError(t, db.Create(&[]types.CitationProfileOperation{ready, preparing, completed}).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(context.Background(), "acl-worker-deny", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		context.Background(), &claims[0], types.CitationProfileACLDecisionDeny,
		now.Add(time.Second), time.Time{},
	))

	var storedScope types.CitationProfileScope
	require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
	require.False(t, storedScope.Enabled)
	require.NotNil(t, storedScope.FencedAt)
	require.NotEmpty(t, storedScope.FenceReason)
	require.Zero(t, storedScope.PendingEventCount)
	require.Greater(t, storedScope.ProfileReadVersion, scope.ProfileReadVersion)

	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
	require.NotEqual(t, types.CitationProfileEventStatusPendingResolution, storedEvent.Status)

	var storedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusDeadletter, storedOutbox.Status)
	require.NotNil(t, storedOutbox.DeadletterAt)
	require.Empty(t, storedOutbox.LockedBy)
	require.Nil(t, storedOutbox.LockedAt)

	var storedReady, storedPreparing, storedCompleted types.CitationProfileOperation
	require.NoError(t, db.First(&storedReady, "id = ?", ready.ID).Error)
	require.NoError(t, db.First(&storedPreparing, "id = ?", preparing.ID).Error)
	require.NoError(t, db.First(&storedCompleted, "id = ?", completed.ID).Error)
	require.Equal(t, types.CitationProfileOperationStatusRevoked, storedReady.Status)
	require.Equal(t, types.CitationProfileOperationStatusRevoked, storedPreparing.Status)
	require.Empty(t, storedReady.ArtifactURI)
	require.Empty(t, storedPreparing.ArtifactURI)
	require.Equal(t, types.CitationProfileOperationStatusExpired, storedCompleted.Status,
		"an already completed non-download operation must retain its terminal audit state")
}

func TestCitationProfileACLRepositoryDenyThenBlindDeletePersistsDeletion(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 15, 30, 0, 0, time.UTC)
	scope := citationProfileACLTestScope("scope-f7-deny-blind-delete", now)
	require.NoError(t, db.Create(scope).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(
		context.Background(), "acl-worker-deny-blind-delete", 1, now, time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		context.Background(), &claims[0], types.CitationProfileACLDecisionDeny,
		now.Add(time.Second), time.Time{},
	))

	var denied types.CitationProfileScope
	require.NoError(t, db.First(&denied, "id = ?", scope.ID).Error)
	require.NotNil(t, denied.FencedAt)
	require.Nil(t, denied.DeletedAt,
		"ACL DENY fences immediately but must not consume the subject's later blind-delete right")

	operation, err := repo.RequestBlindDelete(
		context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID,
	)
	require.NoError(t, err)
	require.NotNil(t, operation)
	require.Equal(t, types.CitationOperationDeleteBlind, operation.OperationType)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, operation.Status)
	require.Equal(t, scope.KnowledgeBaseID, operation.KnowledgeBaseID)

	var deleted types.CitationProfileScope
	require.NoError(t, db.Unscoped().First(&deleted, "id = ?", scope.ID).Error)
	require.NotNil(t, deleted.DeletedAt,
		"an accepted blind delete after ACL loss must persist the deletion boundary")
	require.Equal(t, operation.ID, deleted.DeleteRequestID)
	require.Equal(t, scope.ID, operation.ScopeID,
		"the internal operation must be durable even though the public receipt remains generic")

	var operationCount int64
	require.NoError(t, db.Model(&types.CitationProfileOperation{}).
		Where("id = ? AND scope_id = ? AND operation_type = ?", operation.ID, scope.ID, types.CitationOperationDeleteBlind).
		Count(&operationCount).Error)
	require.Equal(t, int64(1), operationCount)
}

func TestCitationProfileACLRepositorySuspensionPausesOutboxRetryBudget(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC()
	scope := citationProfileACLTestScope("scope-f7-budget", now)
	outbox := citationProfileOutboxFixture(
		"outbox-f7-budget", "event-f7-budget", types.CitationProfileOutboxStatusPending,
		now.Add(-30*time.Hour), nil, "", 0,
	)
	outbox.ScopeID = scope.ID
	outbox.SubjectEpoch = scope.SubjectEpoch
	outbox.CreatedAt = now.Add(-30 * time.Hour)
	outbox.UpdatedAt = outbox.CreatedAt
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claims, err := repo.ClaimCitationProfileACLScopes(context.Background(), "acl-worker-suspend", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		context.Background(), &claims[0], types.CitationProfileACLDecisionTimeout,
		now, now.Add(time.Minute),
	))

	pausedAt, pausedSeconds := citationProfileACLRetryBudget(t, db, outbox.ID)
	require.True(t, pausedAt.Valid)
	require.Zero(t, pausedSeconds)

	resumeAt := now.Add(48 * time.Hour)
	evidenceClaims, err := repo.ClaimCitationProfileEventOutbox(context.Background(), "evidence-worker-blocked", 1, resumeAt, time.Minute)
	require.NoError(t, err)
	require.Empty(t, evidenceClaims, "non-current ACL must keep old work suspended")

	// Advancing a worker clock must not advance the database. Model a genuine
	// 48-hour suspension by moving only persisted DB deadlines into the past.
	databasePast := time.Now().UTC().Add(-time.Second)
	pauseStartedAt := time.Now().UTC().Add(-48 * time.Hour)
	reallyOldCreatedAt := time.Now().UTC().Add(-50 * time.Hour)
	require.NoError(t, db.Model(&types.CitationProfileScope{}).Where("id = ?", scope.ID).
		Update("next_acl_check_at", databasePast).Error)
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).Where("id = ?", outbox.ID).
		Updates(map[string]interface{}{
			"retry_budget_paused_at": pauseStartedAt,
			"created_at":             reallyOldCreatedAt,
		}).Error)
	aclClaims, err := repo.ClaimCitationProfileACLScopes(context.Background(), "acl-worker-resume", 1, resumeAt, time.Minute)
	require.NoError(t, err)
	require.Len(t, aclClaims, 1)
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		context.Background(), &aclClaims[0], types.CitationProfileACLDecisionAllow,
		resumeAt, time.Now().UTC().Add(time.Hour),
	))

	pausedAt, pausedSeconds = citationProfileACLRetryBudget(t, db, outbox.ID)
	require.False(t, pausedAt.Valid)
	require.GreaterOrEqual(t, pausedSeconds, int64((48*time.Hour-time.Second)/time.Second))

	evidenceClaims, err = repo.ClaimCitationProfileEventOutbox(context.Background(), "evidence-worker-resumed", 1, resumeAt, time.Minute)
	require.NoError(t, err)
	require.Len(t, evidenceClaims, 1, "ALLOW must resume work despite wall-clock age exceeding max-age")
	require.Zero(t, evidenceClaims[0].AttemptCount)
}

func citationProfileACLTestScope(id string, now time.Time) *types.CitationProfileScope {
	dueAt := now.Add(-time.Minute)
	checkedAt := now.Add(-time.Hour)
	return &types.CitationProfileScope{
		ID:                       id,
		TenantID:                 7,
		SubjectID:                "user-a",
		KnowledgeBaseID:          "kb-a",
		SubjectEpoch:             "epoch-a",
		ProfileReadVersion:       11,
		ProfilePolicyVersion:     types.CitationProfilePolicyVersion,
		RetentionPolicyVersion:   types.CitationProfileRetentionPolicyVersion,
		Enabled:                  true,
		ACLCheckState:            types.CitationProfileACLStateCurrent,
		ACLCheckedAt:             &checkedAt,
		NextACLCheckAt:           &dueAt,
		ACLPrincipalType:         types.PrincipalWebUser,
		ACLPrincipalID:           "user-a",
		ACLAuthenticatedTenantID: 7,
		ACLAccessPath:            types.CitationProfileACLAccessPathOwner,
		ACLGeneration:            3,
		CreatedAt:                now.Add(-time.Hour),
		UpdatedAt:                now.Add(-time.Hour),
	}
}

func citationProfileACLTestEvent(scope *types.CitationProfileScope, id string, now time.Time) types.CitationProfileEvent {
	return types.CitationProfileEvent{
		ID:                   id,
		TenantID:             scope.TenantID,
		SubjectID:            scope.SubjectID,
		KnowledgeBaseID:      scope.KnowledgeBaseID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		MessageID:            "message-" + id,
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    "knowledge-" + id,
		SourceRefsSnapshot:   json.RawMessage("{}"),
		KnowledgeSnapshot:    json.RawMessage("{}"),
		KnowledgeBaseProof:   json.RawMessage("{}"),
		ProducerEventKey:     "producer-" + id,
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func citationProfileACLTestExport(scope *types.CitationProfileScope, id string, status string, now time.Time) types.CitationProfileOperation {
	return types.CitationProfileOperation{
		ID:              id,
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		SubjectEpoch:    scope.SubjectEpoch,
		ScopeID:         scope.ID,
		OperationType:   types.CitationOperationExport,
		IdempotencyKey:  "idem-" + id,
		Status:          status,
		RequestSnapshot: json.RawMessage("{}"),
		ResultSummary:   json.RawMessage("{}"),
		NextAttemptAt:   now,
		CreatedAt:       now.Add(-time.Minute),
		UpdatedAt:       now.Add(-time.Minute),
	}
}

func citationProfileACLRetryBudget(t *testing.T, db *gorm.DB, outboxID string) (sql.NullTime, int64) {
	t.Helper()
	var pausedAt sql.NullTime
	var pausedSeconds int64
	require.NoError(t, db.Raw(
		"SELECT retry_budget_paused_at, retry_budget_paused_seconds FROM citation_profile_event_outbox WHERE id = ?",
		outboxID,
	).Row().Scan(&pausedAt, &pausedSeconds))
	return pausedAt, pausedSeconds
}
