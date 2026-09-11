package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCitationProfileACLLeaseTokenIsBoundedOpaqueAndUnique(t *testing.T) {
	workerID := "citation-profile-acl-" + strings.Repeat("worker-identity-must-never-reach-storage-", 32)

	first := newCitationProfileACLLeaseToken(workerID)
	second := newCitationProfileACLLeaseToken(workerID)

	require.NotEmpty(t, first)
	require.LessOrEqual(t, len(first), citationProfileACLLeaseTokenMaxLength)
	require.NotContains(t, first, workerID, "the storage token must not expose an unbounded worker identity")
	require.NotEqual(t, first, second, "each claim requires an independent fencing token")
}
