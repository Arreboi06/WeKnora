//go:build cgo

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileOutboxClaimRecoversOnlyEligibleLeases(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-a", "user-a", "kb-a", "epoch-a", 1, 0)
	require.NoError(t, db.Create(scope).Error)
	rows := []types.CitationProfileEventOutbox{
		citationProfileOutboxFixture("outbox-due", "event-due", types.CitationProfileOutboxStatusPending, now.Add(-time.Hour), nil, "", 0),
		citationProfileOutboxFixture("outbox-stale", "event-stale", types.CitationProfileOutboxStatusDelivering, now.Add(-time.Hour), timePtr(now.Add(-2*time.Minute)), "dead-worker", 2),
		citationProfileOutboxFixture("outbox-fresh", "event-fresh", types.CitationProfileOutboxStatusDelivering, now.Add(-time.Hour), timePtr(now.Add(-30*time.Second)), "live-worker", 4),
		citationProfileOutboxFixture("outbox-future", "event-future", types.CitationProfileOutboxStatusPending, now.Add(time.Minute), nil, "", 0),
	}
	require.NoError(t, db.Create(&rows).Error)

	claimStartedAt := time.Now().UTC()
	claimed, err := repo.ClaimCitationProfileEventOutbox(context.Background(), "worker-new", 10, now, time.Minute)
	claimFinishedAt := time.Now().UTC()
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	require.Equal(t, []string{"outbox-due", "outbox-stale"}, []string{claimed[0].ID, claimed[1].ID})
	require.Equal(t, 0, claimed[0].AttemptCount, "leasing work must not consume a resolution attempt")
	require.Equal(t, 2, claimed[1].AttemptCount, "recovering an unstarted lease must preserve the budget")
	for _, row := range claimed {
		require.Equal(t, types.CitationProfileOutboxStatusDelivering, row.Status)
		require.Equal(t, "worker-new", row.LockedBy)
		require.NotNil(t, row.LockedAt)
		require.NotNil(t, row.LeaseUntil)
		require.False(t, row.LockedAt.Before(claimStartedAt.Add(-time.Second)))
		require.False(t, row.LockedAt.After(claimFinishedAt.Add(time.Second)))
		require.WithinDuration(t, row.LockedAt.Add(time.Minute), *row.LeaseUntil, time.Millisecond)
	}

	var fresh types.CitationProfileEventOutbox
	require.NoError(t, db.First(&fresh, "id = ?", "outbox-fresh").Error)
	require.Equal(t, "live-worker", fresh.LockedBy)
	require.Equal(t, 4, fresh.AttemptCount)

	var future types.CitationProfileEventOutbox
	require.NoError(t, db.First(&future, "id = ?", "outbox-future").Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, future.Status)
	require.Equal(t, 0, future.AttemptCount)
}

func TestCitationProfileOutboxDeadletterIsTerminalAndRedacted(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	scope := citationProfileTestScope("scope-a", "user-a", "kb-a", "epoch-a", 7, 1)
	scope.CreatedAt = now.Add(-time.Hour)
	scope.UpdatedAt = now.Add(-time.Hour)
	event := types.CitationProfileEvent{
		ID:                 "event-a",
		TenantID:           7,
		SubjectID:          "user-a",
		KnowledgeBaseID:    "kb-a",
		SubjectEpoch:       "epoch-a",
		ScopeID:            "scope-a",
		MessageID:          "message-a",
		SourceKnowledgeID:  "knowledge-a",
		SourceRefsSnapshot: []byte("{}"),
		KnowledgeSnapshot:  []byte("{}"),
		KnowledgeBaseProof: []byte("{}"),
		ProducerEventKey:   "producer-a",
		Status:             types.CitationProfileEventStatusPendingResolution,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now.Add(-time.Hour),
	}
	outbox := citationProfileOutboxFixture("outbox-a", event.ID, types.CitationProfileOutboxStatusPending, now.Add(-time.Hour), nil, "", 0)
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claimed, err := repo.ClaimCitationProfileEventOutbox(context.Background(), "worker-a", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	secret := "postgres dial failed for tenant=7 subject=user-a password=hunter2"
	require.NoError(t, repo.DeadletterCitationProfileEventOutbox(context.Background(), &claimed[0], now, fmt.Errorf("%s", secret)))

	var storedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusDeadletter, storedOutbox.Status)
	require.Equal(t, types.CitationProfileOutboxErrorResolutionFailed, storedOutbox.LastErrorCode)
	require.Equal(t, types.CitationProfileOutboxMessageResolutionFailed, storedOutbox.LastErrorMessage)
	require.NotContains(t, storedOutbox.LastErrorMessage, "user-a")
	require.NotContains(t, storedOutbox.LastErrorMessage, "hunter2")
	require.NotNil(t, storedOutbox.DeadletterAt)
	require.Nil(t, storedOutbox.LockedAt)
	require.Empty(t, storedOutbox.LockedBy)

	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusFailed, storedEvent.Status)
	require.Equal(t, types.CitationProfileOutboxErrorResolutionFailed, storedEvent.FailedReason)

	var storedScope types.CitationProfileScope
	require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
	require.Equal(t, 0, storedScope.PendingEventCount)
	require.Equal(t, uint64(8), storedScope.ProfileReadVersion)

	err = repo.RetryCitationProfileEventOutbox(context.Background(), &claimed[0], now.Add(time.Second), now.Add(time.Minute), errors.New("retry must not revive a terminal row"))
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusDeadletter, storedOutbox.Status)
}

func TestCitationProfileOutboxAttemptFencesStaleSameWorkerClaims(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-a", "user-a", "kb-a", "epoch-a", 1, 0)
	require.NoError(t, db.Create(scope).Error)
	// Eligibility uses the database clock. Keep the fixture unambiguously due
	// instead of depending on SQLite's millisecond clock rounding relative to
	// Go's higher-resolution time.Now value.
	row := citationProfileOutboxFixture("outbox-start", "event-start", types.CitationProfileOutboxStatusPending, now.Add(-time.Second), nil, "", 0)
	require.NoError(t, db.Create(&row).Error)
	claims, err := repo.ClaimCitationProfileEventOutbox(ctx, "same-worker", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	stale := claims[0]
	attempt, err := repo.StartCitationProfileEventOutboxAttempt(ctx, &claims[0], now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, 1, attempt)
	_, err = repo.StartCitationProfileEventOutboxAttempt(ctx, &stale, now.Add(2*time.Second))
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)
	require.ErrorIs(t, repo.RetryCitationProfileEventOutbox(ctx, &stale, now, now, errors.New("stale")), types.ErrCitationProfileOutboxLeaseLost)

	expiredLockedAt := time.Now().UTC().Add(-2 * time.Minute)
	expiredLeaseUntil := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).
		Where("id = ?", row.ID).
		Updates(map[string]interface{}{
			"locked_at":   expiredLockedAt,
			"lease_until": expiredLeaseUntil,
		}).Error)
	// This models elapsed database time: the original owner's durable claim
	// carried these now-expired timestamps before the second worker reclaimed it.
	claims[0].LockedAt = &expiredLockedAt
	claims[0].LeaseUntil = &expiredLeaseUntil
	reclaimed, err := repo.ClaimCitationProfileEventOutbox(ctx, "same-worker", 1, now.Add(2*time.Minute), time.Minute)
	require.NoError(t, err)
	require.Len(t, reclaimed, 1)
	require.Equal(t, 1, reclaimed[0].AttemptCount)
	_, err = repo.StartCitationProfileEventOutboxAttempt(ctx, &claims[0], now.Add(2*time.Minute))
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = repo.StartCitationProfileEventOutboxAttempt(cancelled, &reclaimed[0], now.Add(2*time.Minute))
	require.ErrorIs(t, err, context.Canceled)
	var stored types.CitationProfileEventOutbox
	require.NoError(t, db.First(&stored, "id = ?", row.ID).Error)
	require.Equal(t, 1, stored.AttemptCount, "reclaims, stale callers and cancelled starts must not consume attempts")
}

func TestCitationProfileDeleteTerminatesUnresolvedWorkAtomically(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-delete", "user-delete", "kb-delete", "epoch-delete", 4, 1)
	scope.CreatedAt = now.Add(-time.Hour)
	scope.UpdatedAt = now.Add(-time.Hour)
	event := types.CitationProfileEvent{
		ID:                   "event-delete",
		TenantID:             7,
		SubjectID:            scope.SubjectID,
		KnowledgeBaseID:      scope.KnowledgeBaseID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		MessageID:            "message-delete",
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    "knowledge-delete",
		SourceRefsSnapshot:   []byte("{}"),
		KnowledgeSnapshot:    []byte("{}"),
		KnowledgeBaseProof:   []byte("{}"),
		ProducerEventKey:     "producer-delete",
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now.Add(-time.Minute),
		UpdatedAt:            now.Add(-time.Minute),
	}
	outbox := types.CitationProfileEventOutbox{
		ID:              "outbox-delete",
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		SubjectEpoch:    scope.SubjectEpoch,
		ScopeID:         scope.ID,
		EventID:         event.ID,
		Status:          types.CitationProfileOutboxStatusPending,
		NextAttemptAt:   now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&outbox).Error)

	expected := scope.ProfileReadVersion
	operation, err := repo.RequestCurrentACLDelete(
		context.Background(),
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		&expected,
		"66666666-6666-4666-8666-666666666666",
	)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, operation.Status)

	var storedScope types.CitationProfileScope
	require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
	require.NotNil(t, storedScope.FencedAt)
	require.NotNil(t, storedScope.DeletedAt)
	require.Zero(t, storedScope.PendingEventCount)
	require.Zero(t, storedScope.PendingMappingCount)
	require.Equal(t, expected+1, storedScope.ProfileReadVersion)

	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusFailed, storedEvent.Status)
	require.Equal(t, types.CitationProfileOutboxErrorScopeDeleted, storedEvent.FailedReason)

	var storedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusDeadletter, storedOutbox.Status)
	require.Equal(t, types.CitationProfileOutboxErrorScopeDeleted, storedOutbox.LastErrorCode)
	require.Equal(t, types.CitationProfileOutboxMessageScopeDeleted, storedOutbox.LastErrorMessage)
	require.NotNil(t, storedOutbox.DeadletterAt)
	require.Nil(t, storedOutbox.LockedAt)
	require.Empty(t, storedOutbox.LockedBy)

	_, err = repo.ResolveEvidenceEvent(context.Background(), scope.TenantID, scope.SubjectID, event.ID)
	require.ErrorIs(t, err, types.ErrCitationProfileDeleted)
}

func TestCitationProfileResolverRejectsNonTerminalActiveRunWithoutDeliveringOutbox(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-invalid-run", "user-invalid-run", "kb-invalid-run", "epoch-invalid-run", 7, 1)
	event := types.CitationProfileEvent{
		ID:                   "event-invalid-run",
		TenantID:             scope.TenantID,
		SubjectID:            scope.SubjectID,
		KnowledgeBaseID:      scope.KnowledgeBaseID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		MessageID:            "message-invalid-run",
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    "knowledge-invalid-run",
		SourceRefsSnapshot:   []byte("{}"),
		KnowledgeSnapshot:    []byte("{}"),
		KnowledgeBaseProof:   []byte("{}"),
		ProducerEventKey:     "producer-invalid-run",
		Status:               types.CitationProfileEventStatusPendingResolution,
		ActiveRunID:          "run-invalid-run",
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	run := types.EvidenceResolutionRun{
		ID:                   event.ActiveRunID,
		TenantID:             scope.TenantID,
		SubjectID:            scope.SubjectID,
		KnowledgeBaseID:      scope.KnowledgeBaseID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		EventID:              event.ID,
		Status:               "running",
		RunUniverseWatermark: "wm-invalid-run",
		StartedAt:            now,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	outbox := citationProfileOutboxFixture(
		"outbox-invalid-run",
		event.ID,
		types.CitationProfileOutboxStatusPending,
		now,
		nil,
		"",
		0,
	)
	outbox.SubjectID = scope.SubjectID
	outbox.KnowledgeBaseID = scope.KnowledgeBaseID
	outbox.SubjectEpoch = scope.SubjectEpoch
	outbox.ScopeID = scope.ID

	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&run).Error)
	require.NoError(t, db.Create(&outbox).Error)

	resolved, err := repo.ResolveEvidenceEvent(context.Background(), scope.TenantID, scope.SubjectID, event.ID)
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
	require.Nil(t, resolved)

	var storedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, storedOutbox.Status)
	require.Nil(t, storedOutbox.DeliveredAt)

	claims, err := repo.ClaimCitationProfileEventOutbox(context.Background(), "deadletter-worker", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, repo.DeadletterCitationProfileEventOutbox(context.Background(), &claims[0], now, types.ErrCitationProfileOutboxMaxAttempts))
	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusFailed, storedEvent.Status, "a dead letter must not strand a pending event with an unfinished run")
	var storedScope types.CitationProfileScope
	require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
	require.Zero(t, storedScope.PendingEventCount)
	var storedRun types.EvidenceResolutionRun
	require.NoError(t, db.First(&storedRun, "id = ?", run.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusFailed, storedRun.Status)
	require.NotNil(t, storedRun.FailedAt)
	require.Nil(t, storedRun.ResolvedAt)
}

func citationProfileOutboxFixture(
	id string,
	eventID string,
	status string,
	nextAttemptAt time.Time,
	lockedAt *time.Time,
	lockedBy string,
	attemptCount int,
) types.CitationProfileEventOutbox {
	createdAt := nextAttemptAt.Add(-time.Minute)
	return types.CitationProfileEventOutbox{
		ID:              id,
		TenantID:        7,
		SubjectID:       "user-a",
		KnowledgeBaseID: "kb-a",
		SubjectEpoch:    "epoch-a",
		ScopeID:         "scope-a",
		EventID:         eventID,
		Status:          status,
		AttemptCount:    attemptCount,
		NextAttemptAt:   nextAttemptAt,
		LockedAt:        lockedAt,
		LeaseUntil: func() *time.Time {
			if status != types.CitationProfileOutboxStatusDelivering || lockedAt == nil {
				return nil
			}
			leaseUntil := lockedAt.UTC().Add(time.Minute)
			return &leaseUntil
		}(),
		LockedBy:  lockedBy,
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
}

func timePtr(value time.Time) *time.Time {
	return &value
}
