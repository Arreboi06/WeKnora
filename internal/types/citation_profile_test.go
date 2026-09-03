package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCitationProfileDefaultGuidanceIsEvidenceInsufficient(t *testing.T) {
	require.Equal(t, "t4-p36-contract-v1", CitationProfileContractVersion)

	got := CitationProfileDefaultGuidance()
	require.Equal(t, CitationProfileGuidance{
		Kind:       "none",
		Reason:     "evidence_insufficient",
		Candidates: []string{},
	}, got)
	require.NotNil(t, got.Candidates, "candidates must encode as [] rather than null")
}

func TestCitationProfileScopeSuspendedFollowsACLState(t *testing.T) {
	for _, state := range []string{"", CitationProfileACLStateCurrent} {
		scope := &CitationProfileScope{Enabled: true, ACLCheckState: state}
		require.False(t, scope.Suspended(), "state %q should be readable", state)
	}
	for _, state := range []string{CitationProfileACLStateUnknown, CitationProfileACLStateError, CitationProfileACLStateTimeout} {
		scope := &CitationProfileScope{Enabled: true, ACLCheckState: state}
		require.True(t, scope.Suspended(), "state %q should suspend profile surfaces", state)
	}
}
