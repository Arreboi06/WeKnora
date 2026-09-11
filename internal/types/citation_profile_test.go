package types

import (
	"testing"
	"time"

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
	now := time.Now().UTC()
	checkedAt := now.Add(-time.Minute)
	nextCheckAt := now.Add(time.Hour)
	valid := &CitationProfileScope{
		TenantID:                 7,
		KnowledgeBaseID:          "kb-f7-current",
		Enabled:                  true,
		ACLCheckState:            CitationProfileACLStateCurrent,
		ACLCheckedAt:             &checkedAt,
		NextACLCheckAt:           &nextCheckAt,
		ACLPrincipalType:         PrincipalWebUser,
		ACLPrincipalID:           "user-f7-current",
		ACLAuthenticatedTenantID: 7,
		ACLAccessPath:            CitationProfileACLAccessPathOwner,
		ACLGeneration:            1,
	}
	require.False(t, valid.Suspended(), "a fresh CURRENT decision with a complete server binding is readable")

	for _, tc := range []struct {
		name   string
		mutate func(*CitationProfileScope)
	}{
		{name: "empty state", mutate: func(scope *CitationProfileScope) { scope.ACLCheckState = "" }},
		{name: "unknown", mutate: func(scope *CitationProfileScope) { scope.ACLCheckState = CitationProfileACLStateUnknown }},
		{name: "error", mutate: func(scope *CitationProfileScope) { scope.ACLCheckState = CitationProfileACLStateError }},
		{name: "timeout", mutate: func(scope *CitationProfileScope) { scope.ACLCheckState = CitationProfileACLStateTimeout }},
		{name: "missing checked at", mutate: func(scope *CitationProfileScope) { scope.ACLCheckedAt = nil }},
		{name: "missing expiry", mutate: func(scope *CitationProfileScope) { scope.NextACLCheckAt = nil }},
		{name: "expired", mutate: func(scope *CitationProfileScope) { expired := now.Add(-time.Second); scope.NextACLCheckAt = &expired }},
		{name: "missing principal", mutate: func(scope *CitationProfileScope) { scope.ACLPrincipalID = "" }},
		{name: "invalid API binding", mutate: func(scope *CitationProfileScope) { scope.ACLAPIKeyID = 9 }},
		{name: "missing access path", mutate: func(scope *CitationProfileScope) { scope.ACLAccessPath = "" }},
		{name: "zero generation", mutate: func(scope *CitationProfileScope) { scope.ACLGeneration = 0 }},
		{name: "active lease token", mutate: func(scope *CitationProfileScope) { scope.ACLCheckLeaseToken = "lease-f7" }},
		{name: "active lease deadline", mutate: func(scope *CitationProfileScope) {
			leaseUntil := now.Add(time.Minute)
			scope.ACLCheckLeaseUntil = &leaseUntil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := *valid
			tc.mutate(&scope)
			require.True(t, scope.Suspended(), "an expired or incomplete ACL decision must fail closed")
		})
	}
}
