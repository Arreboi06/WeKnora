package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type citationProfileF7ACLAuthoritySpy struct {
	calls int
}

func (s *citationProfileF7ACLAuthoritySpy) CheckCitationProfileACL(
	_ context.Context,
	_ *types.CitationProfileScope,
) (types.CitationProfileACLAuthorityResult, error) {
	s.calls++
	return types.CitationProfileACLAuthorityResult{}, nil
}

type citationProfileF7ACLRepositoryPanicSpy struct {
	interfaces.CitationProfileACLRepository
}

type citationProfileF7ACLStartupRepository struct {
	interfaces.CitationProfileACLRepository
	startupFenceCalls int
	startupFenceErr   error
	lastEnabled       bool
}

func (r *citationProfileF7ACLStartupRepository) PrepareCitationProfileACLFeatureState(_ context.Context, enabled bool) error {
	r.startupFenceCalls++
	r.lastEnabled = enabled
	return r.startupFenceErr
}

func TestCitationProfileACLRefreshRunnerFeatureOffPersistsTransitionWithoutAuthorityIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config *types.CitationProfileConfig
	}{
		{name: "nil config", config: nil},
		{name: "explicitly disabled", config: &types.CitationProfileConfig{Enabled: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := &citationProfileF7ACLAuthoritySpy{}
			repo := &citationProfileF7ACLStartupRepository{}
			runner, err := NewCitationProfileACLRefreshRunner(tc.config, authority, repo)
			require.NoError(t, err)
			require.NotNil(t, runner)

			require.NoError(t, runner.PrepareStart(context.Background()))
			require.Equal(t, 1, repo.startupFenceCalls)
			require.False(t, repo.lastEnabled)
			_, err = runner.RunOnce(context.Background())
			require.NoError(t, err)
			require.Zero(t, authority.calls, "feature-off must not consult ACL authority")
		})
	}
}

func TestCitationProfileACLRefreshRunnerPrepareStartFencesBeforeServing(t *testing.T) {
	repo := &citationProfileF7ACLStartupRepository{}
	runner, err := NewCitationProfileACLRefreshRunner(
		&types.CitationProfileConfig{Enabled: true},
		&citationProfileF7ACLAuthoritySpy{},
		repo,
	)
	require.NoError(t, err)
	require.NoError(t, runner.PrepareStart(context.Background()))
	require.Equal(t, 1, repo.startupFenceCalls)
	require.True(t, repo.lastEnabled)

	repo.startupFenceErr = errors.New("startup quarantine unavailable")
	err = runner.PrepareStart(context.Background())
	require.ErrorContains(t, err, "startup quarantine unavailable")
	require.Equal(t, 2, repo.startupFenceCalls)
}

func TestCitationProfileACLRefreshRunnerStartFailsClosedWhenStartupFenceFails(t *testing.T) {
	sentinel := errors.New("startup quarantine unavailable")
	repo := &citationProfileF7ACLStartupRepository{startupFenceErr: sentinel}
	runner, err := NewCitationProfileACLRefreshRunner(
		&types.CitationProfileConfig{Enabled: true},
		&citationProfileF7ACLAuthoritySpy{},
		repo,
	)
	require.NoError(t, err)

	err = runner.Start(context.Background())
	require.ErrorIs(t, err, sentinel)
	require.False(t, runner.started.Load(), "background work must not start after a failed startup fence")
	require.Equal(t, 1, repo.startupFenceCalls)
	require.ErrorIs(t, runner.Start(context.Background()), sentinel,
		"a failed start must remain failed instead of silently succeeding on retry")
	require.Equal(t, 1, repo.startupFenceCalls)
}

func TestCitationProfileACLRefreshRunnerFeatureOffRejectsMissingStateRepository(t *testing.T) {
	runner, err := NewCitationProfileACLRefreshRunner(
		&types.CitationProfileConfig{Enabled: false},
		nil,
		nil,
	)
	require.Error(t, err)
	require.Nil(t, runner)
	require.Contains(t, strings.ToLower(err.Error()), "repository")
}

func TestCitationProfileACLRefreshRunnerFeatureOnRejectsMissingAuthority(t *testing.T) {
	runner, err := NewCitationProfileACLRefreshRunner(
		&types.CitationProfileConfig{Enabled: true},
		nil,
		&citationProfileF7ACLRepositoryPanicSpy{},
	)
	require.Nil(t, runner)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "authority")
}

func TestCitationProfileACLRefreshRunnerFeatureOnRejectsMissingRepository(t *testing.T) {
	authority := &citationProfileF7ACLAuthoritySpy{}
	runner, err := NewCitationProfileACLRefreshRunner(
		&types.CitationProfileConfig{Enabled: true},
		authority,
		nil,
	)
	require.Nil(t, runner)
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "repository")
	require.Zero(t, authority.calls, "constructor validation must not consult authority")
}

func TestCitationProfileACLRefreshRunnerFeatureOnConstructsWithDependencies(t *testing.T) {
	authority := &citationProfileF7ACLAuthoritySpy{}
	runner, err := NewCitationProfileACLRefreshRunner(
		&types.CitationProfileConfig{Enabled: true},
		authority,
		&citationProfileF7ACLRepositoryPanicSpy{},
	)
	require.NoError(t, err)
	require.NotNil(t, runner)
	require.Zero(t, authority.calls, "construction must not perform an ACL check")
}
