package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type citationProfileACLValidityAuthority struct {
	result types.CitationProfileACLAuthorityResult
}

func (a *citationProfileACLValidityAuthority) CheckCitationProfileACL(
	_ context.Context,
	_ *types.CitationProfileScope,
) (types.CitationProfileACLAuthorityResult, error) {
	return a.result, nil
}

type citationProfileACLValidityRepository struct {
	interfaces.CitationProfileACLRepository
	claimed     bool
	decision    types.CitationProfileACLDecision
	appliedAt   time.Time
	nextCheckAt time.Time
}

func (r *citationProfileACLValidityRepository) ClaimCitationProfileACLScopes(
	_ context.Context,
	_ string,
	_ int,
	_ time.Time,
	_ time.Duration,
) ([]types.CitationProfileACLClaim, error) {
	if r.claimed {
		return nil, nil
	}
	r.claimed = true
	return []types.CitationProfileACLClaim{{
		ScopeID:    "scope-validity-f7",
		LeaseToken: "lease-validity-f7",
		Generation: 1,
		Scope: types.CitationProfileScope{
			ID:            "scope-validity-f7",
			ACLGeneration: 1,
		},
	}}, nil
}

func (r *citationProfileACLValidityRepository) ApplyCitationProfileACLResult(
	_ context.Context,
	_ *types.CitationProfileACLClaim,
	decision types.CitationProfileACLDecision,
	appliedAt time.Time,
	nextCheckAt time.Time,
) error {
	r.decision = decision
	r.appliedAt = appliedAt
	r.nextCheckAt = nextCheckAt
	return nil
}

func TestCitationProfileACLRefreshRunnerForwardsAuthorityHorizonWithoutTrustingHostClock(t *testing.T) {
	databaseNow := time.Now().UTC()
	shortHorizon := databaseNow.Add(2 * time.Minute)
	pastHorizon := databaseNow.Add(-time.Minute)
	longHorizon := databaseNow.Add(30 * time.Minute)

	for _, tc := range []struct {
		name       string
		hostNow    time.Time
		validUntil *time.Time
	}{
		{
			name:    "unbounded authority delegates default TTL to repository DB clock",
			hostNow: databaseNow.Add(24 * time.Hour),
		},
		{
			name:       "fast host cannot turn DB-current credential into deny",
			hostNow:    databaseNow.Add(10 * time.Minute),
			validUntil: &shortHorizon,
		},
		{
			name:       "slow host forwards DB-current credential horizon unchanged",
			hostNow:    databaseNow.Add(-10 * time.Minute),
			validUntil: &shortHorizon,
		},
		{
			name:       "apparently expired horizon is adjudicated only by repository DB clock",
			hostNow:    databaseNow.Add(24 * time.Hour),
			validUntil: &pastHorizon,
		},
		{
			name:       "long horizon is forwarded for repository hard cap",
			hostNow:    databaseNow.Add(-24 * time.Hour),
			validUntil: &longHorizon,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &citationProfileACLValidityRepository{}
			runner, err := NewCitationProfileACLRefreshRunner(
				&types.CitationProfileConfig{Enabled: true},
				&citationProfileACLValidityAuthority{result: types.CitationProfileACLAuthorityResult{
					Decision:   types.CitationProfileACLDecisionAllow,
					ValidUntil: tc.validUntil,
				}},
				repo,
			)
			require.NoError(t, err)
			runner.now = func() time.Time { return tc.hostNow }
			runner.batchSize = 1

			summary, err := runner.RunOnce(context.Background())
			require.NoError(t, err)
			require.Equal(t, 1, summary.Claimed)
			require.Equal(t, 1, summary.Allowed)
			require.Zero(t, summary.Denied)
			require.Equal(t, types.CitationProfileACLDecisionAllow, repo.decision)
			require.Equal(t, tc.hostNow, repo.appliedAt)
			if tc.validUntil == nil {
				require.True(t, repo.nextCheckAt.IsZero(), "zero delegates the bounded default to database time")
			} else {
				require.Equal(t, tc.validUntil.UTC(), repo.nextCheckAt)
			}
		})
	}
}
