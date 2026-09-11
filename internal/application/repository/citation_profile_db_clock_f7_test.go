//go:build cgo

package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileACLDBClockPreventsFastNodeLeaseTheft(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	dbNow := time.Now().UTC()
	scope := citationProfileACLTestScope("scope-f7-db-clock-fast-acl", dbNow)
	require.NoError(t, db.Create(scope).Error)

	first, err := repo.ClaimCitationProfileACLScopes(
		context.Background(), "acl-db-clock-owner", 1, dbNow, 2*time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, first, 1)

	stolen, err := repo.ClaimCitationProfileACLScopes(
		context.Background(), "acl-db-clock-fast-node", 1, dbNow.Add(24*time.Hour), time.Second,
	)
	require.NoError(t, err)
	require.Empty(t, stolen,
		"a fast application clock must not expire or shorten another worker's database lease")

	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
	require.Equal(t, first[0].LeaseToken, stored.ACLCheckLeaseToken)
	require.NotNil(t, stored.ACLCheckLeaseUntil)
	require.WithinDuration(t, dbNow.Add(2*time.Minute), stored.ACLCheckLeaseUntil.UTC(), 5*time.Second)
}

func TestCitationProfileACLDBClockRejectsExpiredLeaseFromSlowNode(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	dbNow := time.Now().UTC()
	scope := citationProfileACLTestScope("scope-f7-db-clock-slow-apply", dbNow)
	require.NoError(t, db.Create(scope).Error)
	claims, err := repo.ClaimCitationProfileACLScopes(
		context.Background(), "acl-db-clock-expired-owner", 1, dbNow, 2*time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claims, 1)

	require.NoError(t, db.Exec(
		"UPDATE citation_profile_scopes SET acl_check_lease_until = datetime('now', '-1 minute') WHERE id = ?",
		scope.ID,
	).Error)
	slowNodeNow := dbNow.Add(-24 * time.Hour)
	err = repo.ApplyCitationProfileACLResult(
		context.Background(), &claims[0], types.CitationProfileACLDecisionAllow,
		slowNodeNow, dbNow.Add(time.Hour),
	)
	require.ErrorIs(t, err, types.ErrCitationProfileACLLeaseLost)

	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)
	require.Equal(t, claims[0].LeaseToken, stored.ACLCheckLeaseToken)
}

func TestCitationProfileACLDBClockBoundsAllowValidity(t *testing.T) {
	t.Run("far future caller deadline is hard capped", func(t *testing.T) {
		db := newCitationProfileRepositoryTestDB(t)
		repo := &citationProfileRepository{db: db}
		dbNow := time.Now().UTC()
		scope := citationProfileACLTestScope("scope-f7-db-clock-allow-cap", dbNow)
		require.NoError(t, db.Create(scope).Error)
		claims, err := repo.ClaimCitationProfileACLScopes(
			context.Background(), "acl-db-clock-allow-cap", 1, dbNow, 2*time.Minute,
		)
		require.NoError(t, err)
		require.Len(t, claims, 1)

		beforeApply := time.Now().UTC()
		require.NoError(t, repo.ApplyCitationProfileACLResult(
			context.Background(), &claims[0], types.CitationProfileACLDecisionAllow,
			dbNow.Add(-24*time.Hour), dbNow.Add(24*time.Hour),
		))
		var stored types.CitationProfileScope
		require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
		require.NotNil(t, stored.NextACLCheckAt)
		require.True(t, stored.NextACLCheckAt.After(beforeApply))
		require.False(t, stored.NextACLCheckAt.After(beforeApply.Add(5*time.Minute+5*time.Second)),
			"repository is the final defense and must cap every ALLOW to five minutes of DB time")
	})

	t.Run("deadline already expired in database time is rejected", func(t *testing.T) {
		db := newCitationProfileRepositoryTestDB(t)
		repo := &citationProfileRepository{db: db}
		dbNow := time.Now().UTC()
		scope := citationProfileACLTestScope("scope-f7-db-clock-allow-expired", dbNow)
		require.NoError(t, db.Create(scope).Error)
		claims, err := repo.ClaimCitationProfileACLScopes(
			context.Background(), "acl-db-clock-allow-expired", 1, dbNow, 2*time.Minute,
		)
		require.NoError(t, err)
		require.Len(t, claims, 1)

		slowNodeNow := dbNow.Add(-2 * time.Hour)
		err = repo.ApplyCitationProfileACLResult(
			context.Background(), &claims[0], types.CitationProfileACLDecisionAllow,
			slowNodeNow, dbNow.Add(-time.Hour),
		)
		require.Error(t, err)
		var stored types.CitationProfileScope
		require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
		require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)
		require.Equal(t, claims[0].LeaseToken, stored.ACLCheckLeaseToken)
	})
}

func TestCitationProfileOutboxDBClockRejectsExpiredACLAtClaimAndStart(t *testing.T) {
	t.Run("claim", func(t *testing.T) {
		db := newCitationProfileRepositoryTestDB(t)
		repo := &citationProfileRepository{db: db}
		dbNow := time.Now().UTC()
		scope := citationProfileACLTestScope("scope-f7-db-clock-expired-acl-claim", dbNow)
		checkedAt := dbNow.Add(-4 * time.Hour)
		expiredAt := dbNow.Add(-time.Minute)
		scope.ACLCheckedAt = &checkedAt
		scope.NextACLCheckAt = &expiredAt
		outbox := citationProfileOutboxFixture(
			"outbox-f7-db-clock-expired-acl-claim", "event-f7-db-clock-expired-acl-claim",
			types.CitationProfileOutboxStatusPending, dbNow.Add(-3*time.Hour), nil, "", 0,
		)
		outbox.ScopeID = scope.ID
		require.NoError(t, db.Create(scope).Error)
		require.NoError(t, db.Create(&outbox).Error)

		claims, err := repo.ClaimCitationProfileEventOutbox(
			context.Background(), "outbox-db-clock-slow-claim", 1, dbNow.Add(-2*time.Hour), 2*time.Minute,
		)
		require.NoError(t, err)
		require.Empty(t, claims, "database-expired ACL must not be revived by a slow caller clock")
	})

	t.Run("start", func(t *testing.T) {
		db := newCitationProfileRepositoryTestDB(t)
		repo := &citationProfileRepository{db: db}
		dbNow := time.Now().UTC()
		scope := citationProfileACLTestScope("scope-f7-db-clock-expired-acl-start", dbNow)
		checkedAt := dbNow.Add(-4 * time.Hour)
		validUntil := dbNow.Add(time.Hour)
		scope.ACLCheckedAt = &checkedAt
		scope.NextACLCheckAt = &validUntil
		outbox := citationProfileOutboxFixture(
			"outbox-f7-db-clock-expired-acl-start", "event-f7-db-clock-expired-acl-start",
			types.CitationProfileOutboxStatusPending, dbNow.Add(-time.Minute), nil, "", 0,
		)
		outbox.ScopeID = scope.ID
		require.NoError(t, db.Create(scope).Error)
		require.NoError(t, db.Create(&outbox).Error)
		claims, err := repo.ClaimCitationProfileEventOutbox(
			context.Background(), "outbox-db-clock-start-owner", 1, dbNow, 2*time.Minute,
		)
		require.NoError(t, err)
		require.Len(t, claims, 1)

		require.NoError(t, db.Exec(
			"UPDATE citation_profile_scopes SET acl_checked_at = datetime('now', '-2 hour'), next_acl_check_at = datetime('now', '-1 minute') WHERE id = ?",
			scope.ID,
		).Error)
		_, err = repo.StartCitationProfileEventOutboxAttempt(
			context.Background(), &claims[0], dbNow.Add(-time.Hour),
		)
		require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)
	})
}

func TestCitationProfileOutboxDBLeaseCannotBeShortenedByFastNode(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	dbNow := time.Now().UTC()
	scope := citationProfileACLTestScope("scope-f7-db-clock-fast-outbox", dbNow)
	checkedAt := dbNow.Add(-time.Hour)
	validUntil := dbNow.Add(48 * time.Hour)
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &validUntil
	outbox := citationProfileOutboxFixture(
		"outbox-f7-db-clock-fast-outbox", "event-f7-db-clock-fast-outbox",
		types.CitationProfileOutboxStatusPending, dbNow.Add(-time.Minute), nil, "", 0,
	)
	outbox.ScopeID = scope.ID
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&outbox).Error)

	first, err := repo.ClaimCitationProfileEventOutbox(
		context.Background(), "outbox-db-clock-owner", 1, dbNow, 2*time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, first, 1)

	stolen, err := repo.ClaimCitationProfileEventOutbox(
		context.Background(), "outbox-db-clock-fast-node", 1, dbNow.Add(24*time.Hour), time.Second,
	)
	require.NoError(t, err)
	require.Empty(t, stolen,
		"a fast node and a shorter requested lease must not reclaim a database-owned live lease")

	var stored types.CitationProfileEventOutbox
	require.NoError(t, db.First(&stored, "id = ?", outbox.ID).Error)
	require.Equal(t, first[0].LockedBy, stored.LockedBy)
	require.NotNil(t, stored.LeaseUntil)
}

func TestCitationProfileOutboxDBClockRejectsExpiredLeaseTransitions(t *testing.T) {
	for _, transition := range []string{"start", "retry", "deadletter"} {
		t.Run(transition, func(t *testing.T) {
			db := newCitationProfileRepositoryTestDB(t)
			repo := &citationProfileRepository{db: db}
			dbNow := time.Now().UTC()
			scope := citationProfileACLTestScope("scope-f7-db-clock-expired-outbox-"+transition, dbNow)
			checkedAt := dbNow.Add(-4 * time.Hour)
			validUntil := dbNow.Add(time.Hour)
			scope.ACLCheckedAt = &checkedAt
			scope.NextACLCheckAt = &validUntil
			event := citationProfileACLTestEvent(scope, "event-f7-db-clock-expired-outbox-"+transition, dbNow.Add(-time.Minute))
			outbox := citationProfileOutboxFixture(
				"outbox-f7-db-clock-expired-outbox-"+transition, event.ID,
				types.CitationProfileOutboxStatusPending, dbNow.Add(-time.Minute), nil, "", 0,
			)
			outbox.ScopeID = scope.ID
			require.NoError(t, db.Create(scope).Error)
			require.NoError(t, db.Create(&event).Error)
			require.NoError(t, db.Create(&outbox).Error)
			claims, err := repo.ClaimCitationProfileEventOutbox(
				context.Background(), "outbox-db-clock-expired-owner", 1, dbNow, 2*time.Minute,
			)
			require.NoError(t, err)
			require.Len(t, claims, 1)

			require.NoError(t, db.Exec(
				"UPDATE citation_profile_event_outbox SET locked_at = datetime('now', '-2 minute'), lease_until = datetime('now', '-1 minute') WHERE id = ?",
				outbox.ID,
			).Error)
			var expiredClaim types.CitationProfileEventOutbox
			require.NoError(t, db.First(&expiredClaim, "id = ?", outbox.ID).Error)
			slowNodeNow := dbNow.Add(-30 * time.Minute)
			switch transition {
			case "start":
				_, err = repo.StartCitationProfileEventOutboxAttempt(context.Background(), &expiredClaim, slowNodeNow)
			case "retry":
				err = repo.RetryCitationProfileEventOutbox(
					context.Background(), &expiredClaim, slowNodeNow, slowNodeNow.Add(time.Minute), errors.New("retry"),
				)
			case "deadletter":
				err = repo.DeadletterCitationProfileEventOutbox(
					context.Background(), &expiredClaim, slowNodeNow, errors.New("deadletter"),
				)
			}
			require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)

			var stored types.CitationProfileEventOutbox
			require.NoError(t, db.First(&stored, "id = ?", outbox.ID).Error)
			require.Equal(t, types.CitationProfileOutboxStatusDelivering, stored.Status)
			require.Zero(t, stored.AttemptCount)
			require.NotNil(t, stored.LeaseUntil)
		})
	}
}

func TestCitationProfileClaimedResolverRejectsReclaimedStaleWorkerBeforePublish(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	dbNow := time.Now().UTC()
	scope := citationProfileACLTestScope("scope-f7-claimed-resolver-owner", dbNow)
	checkedAt := dbNow.Add(-time.Hour)
	validUntil := dbNow.Add(time.Hour)
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &validUntil
	scope.PendingEventCount = 1
	event := citationProfileACLTestEvent(scope, "event-f7-claimed-resolver-owner", dbNow.Add(-time.Minute))
	outbox := citationProfileOutboxFixture(
		"outbox-f7-claimed-resolver-owner", event.ID,
		types.CitationProfileOutboxStatusPending, dbNow.Add(-time.Minute), nil, "", 0,
	)
	outbox.ScopeID = scope.ID
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claimsA, err := repo.ClaimCitationProfileEventOutbox(
		context.Background(), "resolver-worker-a", 1, dbNow, 2*time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claimsA, 1)
	_, err = repo.StartCitationProfileEventOutboxAttempt(context.Background(), &claimsA[0], dbNow)
	require.NoError(t, err)

	require.NoError(t, db.Exec(
		"UPDATE citation_profile_event_outbox SET locked_at = datetime('now', '-2 minute'), lease_until = datetime('now', '-1 minute') WHERE id = ?",
		outbox.ID,
	).Error)
	claimsB, err := repo.ClaimCitationProfileEventOutbox(
		context.Background(), "resolver-worker-b", 1, dbNow.Add(-24*time.Hour), 2*time.Minute,
	)
	require.NoError(t, err)
	require.Len(t, claimsB, 1)
	_, err = repo.StartCitationProfileEventOutboxAttempt(context.Background(), &claimsB[0], dbNow.Add(24*time.Hour))
	require.NoError(t, err)

	_, err = repo.ResolveClaimedEvidenceEvent(context.Background(), &claimsA[0])
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)
	var runCount int64
	require.NoError(t, db.Model(&types.EvidenceResolutionRun{}).Where("event_id = ?", event.ID).Count(&runCount).Error)
	require.Zero(t, runCount)
	var unchangedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&unchangedEvent, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, unchangedEvent.Status)
	require.Empty(t, unchangedEvent.ActiveRunID)
	var unchangedScope types.CitationProfileScope
	require.NoError(t, db.First(&unchangedScope, "id = ?", scope.ID).Error)
	require.Equal(t, scope.ProfileReadVersion, unchangedScope.ProfileReadVersion)
	require.Equal(t, 1, unchangedScope.PendingEventCount)

	run, err := repo.ResolveClaimedEvidenceEvent(context.Background(), &claimsB[0])
	require.NoError(t, err)
	require.NotNil(t, run)
	var delivered types.CitationProfileEventOutbox
	require.NoError(t, db.First(&delivered, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusDelivered, delivered.Status)
	require.NotNil(t, delivered.DeliveredAt)
	require.Nil(t, delivered.LockedAt)
	require.Nil(t, delivered.LeaseUntil)
	require.Empty(t, delivered.LockedBy)
}
