package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type citationProfileOutboxRunnerRepo struct {
	claims        []types.CitationProfileEventOutbox
	retryCalls    int
	deadCalls     int
	lastRetryAt   time.Time
	lastDeadCause error
	startCalls    int
}

func (r *citationProfileOutboxRunnerRepo) ClaimCitationProfileEventOutbox(
	_ context.Context, _ string, limit int, _ time.Time, _ time.Duration,
) ([]types.CitationProfileEventOutbox, error) {
	n := min(limit, len(r.claims))
	claims := append([]types.CitationProfileEventOutbox(nil), r.claims[:n]...)
	r.claims = r.claims[n:]
	return claims, nil
}

func (r *citationProfileOutboxRunnerRepo) StartCitationProfileEventOutboxAttempt(
	ctx context.Context, claim *types.CitationProfileEventOutbox, _ time.Time,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.startCalls++
	return claim.AttemptCount + 1, nil
}

func (r *citationProfileOutboxRunnerRepo) RetryCitationProfileEventOutbox(
	_ context.Context,
	_ *types.CitationProfileEventOutbox,
	_ time.Time,
	next time.Time,
	_ error,
) error {
	r.retryCalls++
	r.lastRetryAt = next
	return nil
}

func (r *citationProfileOutboxRunnerRepo) DeadletterCitationProfileEventOutbox(
	_ context.Context,
	_ *types.CitationProfileEventOutbox,
	_ time.Time,
	cause error,
) error {
	r.deadCalls++
	r.lastDeadCause = cause
	return nil
}

var _ interfaces.CitationProfileOutboxRepository = (*citationProfileOutboxRunnerRepo)(nil)

type citationProfileOutboxResolverRepo struct {
	spyCitationProfileScopeStore
	resolveErrByEvent map[string]error
	resolveIDs        []string
	afterResolve      func()
}

func (r *citationProfileOutboxResolverRepo) ResolveEvidenceEvent(
	_ context.Context, _ uint64, _ string, eventID string,
) (*types.EvidenceResolutionRun, error) {
	r.resolveIDs = append(r.resolveIDs, eventID)
	if r.afterResolve != nil {
		r.afterResolve()
	}
	return nil, r.resolveErrByEvent[eventID]
}

func (r *citationProfileOutboxResolverRepo) ResolveClaimedEvidenceEvent(
	ctx context.Context, claim *types.CitationProfileEventOutbox,
) (*types.EvidenceResolutionRun, error) {
	return r.ResolveEvidenceEvent(ctx, claim.TenantID, claim.SubjectID, claim.EventID)
}

func TestCitationProfileOutboxRunnerCancellationDoesNotStartRemainingWork(t *testing.T) {
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &citationProfileOutboxRunnerRepo{claims: []types.CitationProfileEventOutbox{
		{EventID: "started", CreatedAt: now},
		{EventID: "not-started", CreatedAt: now},
	}}
	resolver := &citationProfileOutboxResolverRepo{afterResolve: cancel}
	runner := NewCitationProfileOutboxRunner(&types.CitationProfileConfig{Enabled: true}, resolver, store)
	summary, err := runner.RunOnce(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"started"}, resolver.resolveIDs)
	require.Equal(t, 1, summary.Resolved)
	require.Zero(t, summary.Deadlettered)
}

func TestCitationProfileOutboxRunnerExhaustedBudgetDoesNotResolve(t *testing.T) {
	store := &citationProfileOutboxRunnerRepo{claims: []types.CitationProfileEventOutbox{
		{EventID: "exhausted", AttemptCount: 8, CreatedAt: time.Now().UTC()},
	}}
	resolver := &citationProfileOutboxResolverRepo{}
	runner := NewCitationProfileOutboxRunner(&types.CitationProfileConfig{Enabled: true}, resolver, store)
	summary, err := runner.RunOnce(context.Background())
	require.NoError(t, err)
	require.Empty(t, resolver.resolveIDs)
	require.Equal(t, 1, summary.Deadlettered)
	require.ErrorIs(t, store.lastDeadCause, types.ErrCitationProfileOutboxMaxAttempts)
}

func TestCitationProfileOutboxRunnerRetriesTransientAndDeadlettersTerminalWork(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(time.Minute)
	transient := types.CitationProfileEventOutbox{
		ID:              "outbox-transient",
		TenantID:        7,
		SubjectID:       "user-7",
		KnowledgeBaseID: "kb-a",
		SubjectEpoch:    "epoch-a",
		ScopeID:         "scope-a",
		EventID:         "event-transient",
		AttemptCount:    1,
		LockedAt:        &now,
		LeaseUntil:      &leaseUntil,
		CreatedAt:       now,
	}
	expired := transient
	expired.ID = "outbox-expired"
	expired.EventID = "event-expired"
	expired.CreatedAt = now.Add(-25 * time.Hour)
	terminal := transient
	terminal.ID = "outbox-deleted"
	terminal.EventID = "event-deleted"
	store := &citationProfileOutboxRunnerRepo{claims: []types.CitationProfileEventOutbox{transient, expired, terminal}}
	resolver := &citationProfileOutboxResolverRepo{resolveErrByEvent: map[string]error{
		transient.EventID: errors.New("temporary provider failure"),
		terminal.EventID:  types.ErrCitationProfileDeleted,
	}}
	runner := &CitationProfileOutboxRunner{
		enabled:     true,
		repo:        resolver,
		outbox:      store,
		interval:    time.Hour,
		lease:       time.Minute,
		maxAge:      24 * time.Hour,
		maxAttempts: 8,
		batchSize:   10,
		now:         func() time.Time { return now },
	}

	// The terminal event is classified by the resolver, while the ordinary
	// error remains retryable. A real worker must process every claim in one
	// sweep rather than stopping at the first bad row.
	summary, err := runner.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, len(resolver.resolveIDs), "expired work must be dead-lettered before resolver access")
	require.Equal(t, 2, summary.Deadlettered)
	require.Equal(t, 1, summary.Retried)
	require.Equal(t, 2, store.deadCalls)
	require.Equal(t, 1, store.retryCalls)
	require.ErrorIs(t, store.lastDeadCause, types.ErrCitationProfileDeleted)
}

func TestCitationProfileOutboxRunnerDisabledDoesNotClaim(t *testing.T) {
	store := &citationProfileOutboxRunnerRepo{claims: []types.CitationProfileEventOutbox{{ID: "never"}}}
	runner := &CitationProfileOutboxRunner{
		enabled: true,
		repo:    nil,
		outbox:  store,
	}
	// A missing request-path repository is a disabled/safe configuration. It
	// must not touch the outbox and must not panic during container startup.
	summary, err := runner.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, summary.Claimed)
	require.Equal(t, 0, store.retryCalls)
	require.Equal(t, 0, store.deadCalls)
}

func TestCitationProfileOutboxRunnerZeroValueStartStopIsSafe(t *testing.T) {
	runner := &CitationProfileOutboxRunner{}
	require.NotPanics(t, func() {
		runner.Start(context.Background())
		runner.Stop()
	})
}

func TestCitationProfileOutboxRunnerUsesFinalAllowedAttempt(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	claim := types.CitationProfileEventOutbox{
		ID:              "outbox-final-attempt",
		TenantID:        7,
		SubjectID:       "user-7",
		KnowledgeBaseID: "kb-a",
		SubjectEpoch:    "epoch-a",
		ScopeID:         "scope-a",
		EventID:         "event-final-attempt",
		AttemptCount:    7,
		CreatedAt:       now,
	}
	store := &citationProfileOutboxRunnerRepo{claims: []types.CitationProfileEventOutbox{claim}}
	resolver := &citationProfileOutboxResolverRepo{resolveErrByEvent: map[string]error{
		claim.EventID: errors.New("last permitted attempt failed"),
	}}
	runner := &CitationProfileOutboxRunner{
		enabled:     true,
		repo:        resolver,
		outbox:      store,
		maxAge:      24 * time.Hour,
		maxAttempts: 8,
		now:         func() time.Time { return now },
	}

	summary, err := runner.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{claim.EventID}, resolver.resolveIDs)
	require.Equal(t, 1, summary.Deadlettered)
	require.Zero(t, summary.Retried)
	require.ErrorIs(t, store.lastDeadCause, types.ErrCitationProfileOutboxMaxAttempts)
}

func TestCitationProfileOutboxMaxAgeUsesDatabaseClaimTime(t *testing.T) {
	databaseClaimedAt := time.Date(2026, 9, 11, 5, 0, 0, 0, time.UTC)
	fresh := &types.CitationProfileEventOutbox{
		CreatedAt: databaseClaimedAt.Add(-time.Minute),
		LockedAt:  &databaseClaimedAt,
	}
	require.False(t, outboxClaimExpired(fresh, databaseClaimedAt.Add(24*time.Hour), 24*time.Hour),
		"a fast worker clock must not permanently deadletter fresh database work")

	old := &types.CitationProfileEventOutbox{
		CreatedAt: databaseClaimedAt.Add(-25 * time.Hour),
		LockedAt:  &databaseClaimedAt,
	}
	require.True(t, outboxClaimExpired(old, databaseClaimedAt.Add(-24*time.Hour), 24*time.Hour),
		"a slow worker clock must not revive work that is old in database time")
}
