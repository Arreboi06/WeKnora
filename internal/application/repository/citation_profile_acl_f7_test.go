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

func TestCitationProfileOutboxClaimSkipsACLNonCurrentScope(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	scope := citationProfileTestScope("scope-a", "user-a", "kb-a", "epoch-a", 7, 1)
	scope.ACLCheckState = types.CitationProfileACLStateUnknown
	outbox := citationProfileOutboxFixture(
		"outbox-f7-suspended",
		"event-f7-suspended",
		types.CitationProfileOutboxStatusPending,
		now.Add(-time.Minute),
		nil,
		"",
		0,
	)
	outbox.ScopeID = scope.ID
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claims, err := repo.ClaimCitationProfileEventOutbox(context.Background(), "worker-f7", 1, now, time.Minute)
	require.NoError(t, err)
	require.Empty(t, claims, "ACL UNKNOWN must suspend durable resolution work")

	var stored types.CitationProfileEventOutbox
	require.NoError(t, db.First(&stored, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, stored.Status)
	require.Zero(t, stored.AttemptCount, "ACL suspension must not spend attempts")
}

func TestCitationProfileOutboxStartAttemptRejectsACLStateChangedAfterClaim(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	scope := citationProfileTestScope("scope-a", "user-a", "kb-a", "epoch-a", 7, 1)
	outbox := citationProfileOutboxFixture(
		"outbox-f7-stale-allow",
		"event-f7-stale-allow",
		types.CitationProfileOutboxStatusPending,
		now.Add(-time.Minute),
		nil,
		"",
		0,
	)
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claims, err := repo.ClaimCitationProfileEventOutbox(context.Background(), "worker-f7", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("id = ?", scope.ID).
		Update("acl_check_state", types.CitationProfileACLStateUnknown).Error)

	_, err = repo.StartCitationProfileEventOutboxAttempt(context.Background(), &claims[0], now.Add(time.Second))
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)

	var stored types.CitationProfileEventOutbox
	require.NoError(t, db.First(&stored, "id = ?", outbox.ID).Error)
	require.Zero(t, stored.AttemptCount, "a post-claim ACL suspension must fence the attempt")
}

func TestCitationProfileACLCurrentGateMatchesReadAndOutboxSQLForEveryBindingShape(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name   string
		valid  bool
		mutate func(*types.CitationProfileScope)
	}{
		{name: "web owner", valid: true},
		{name: "api tenant kb share", valid: true, mutate: func(scope *types.CitationProfileScope) {
			scope.ACLPrincipalType = types.PrincipalAPITenant
			scope.ACLAPIKeyID = 41
			scope.ACLAuthenticatedTenantID = 8
			scope.ACLAccessPath = types.CitationProfileACLAccessPathKBShare
			scope.ACLAccessPathID = scope.KnowledgeBaseID
		}},
		{name: "api external agent share", valid: true, mutate: func(scope *types.CitationProfileScope) {
			scope.ACLPrincipalType = types.PrincipalAPIExternalUser
			scope.ACLAPIKeyID = 42
			scope.ACLAuthenticatedTenantID = 9
			scope.ACLAccessPath = types.CitationProfileACLAccessPathAgentShare
			scope.ACLAccessPathID = "agent-f7-gate"
		}},
		{name: "missing principal", mutate: func(scope *types.CitationProfileScope) { scope.ACLPrincipalID = "" }},
		{name: "web with api key", mutate: func(scope *types.CitationProfileScope) { scope.ACLAPIKeyID = 1 }},
		{name: "api without key", mutate: func(scope *types.CitationProfileScope) {
			scope.ACLPrincipalType = types.PrincipalAPITenant
		}},
		{name: "owner tenant mismatch", mutate: func(scope *types.CitationProfileScope) {
			scope.ACLAuthenticatedTenantID++
		}},
		{name: "kb share path mismatch", mutate: func(scope *types.CitationProfileScope) {
			scope.ACLAccessPath = types.CitationProfileACLAccessPathKBShare
			scope.ACLAccessPathID = "different-kb"
		}},
		{name: "agent share missing id", mutate: func(scope *types.CitationProfileScope) {
			scope.ACLAccessPath = types.CitationProfileACLAccessPathAgentShare
		}},
		{name: "zero generation", mutate: func(scope *types.CitationProfileScope) { scope.ACLGeneration = 0 }},
		{name: "missing checked at", mutate: func(scope *types.CitationProfileScope) { scope.ACLCheckedAt = nil }},
		{name: "missing next check", mutate: func(scope *types.CitationProfileScope) { scope.NextACLCheckAt = nil }},
		{name: "active lease token", mutate: func(scope *types.CitationProfileScope) {
			scope.ACLCheckLeaseToken = "lease-f7-gate"
		}},
		{name: "active lease deadline", mutate: func(scope *types.CitationProfileScope) {
			leaseUntil := now.Add(time.Minute)
			scope.ACLCheckLeaseUntil = &leaseUntil
		}},
		{name: "future check", mutate: func(scope *types.CitationProfileScope) {
			checkedAt := now.Add(time.Minute)
			scope.ACLCheckedAt = &checkedAt
		}},
		{name: "expired check", mutate: func(scope *types.CitationProfileScope) {
			nextCheckAt := now
			scope.NextACLCheckAt = &nextCheckAt
		}},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := newCitationProfileRepositoryTestDB(t)
			repo := &citationProfileRepository{db: db}
			suffix := fmt.Sprintf("%02d", i)
			scope := citationProfileTestScope(
				"scope-f7-current-gate-"+suffix,
				"user-f7-current-gate-"+suffix,
				"kb-f7-current-gate-"+suffix,
				"epoch-f7-current-gate-"+suffix,
				1,
				1,
			)
			checkedAt := now.Add(-time.Minute)
			nextCheckAt := now.Add(time.Hour)
			scope.ACLCheckedAt = &checkedAt
			scope.NextACLCheckAt = &nextCheckAt
			if tc.mutate != nil {
				tc.mutate(scope)
			}
			require.Equal(t, tc.valid, scope.ACLCurrentAt(now), "in-memory CURRENT decision")
			require.NoError(t, db.Create(scope).Error)

			outbox := citationProfileOutboxFixture(
				"outbox-f7-current-gate-"+suffix,
				"event-f7-current-gate-"+suffix,
				types.CitationProfileOutboxStatusPending,
				now.Add(-time.Minute),
				nil,
				"",
				0,
			)
			outbox.TenantID = scope.TenantID
			outbox.SubjectID = scope.SubjectID
			outbox.KnowledgeBaseID = scope.KnowledgeBaseID
			outbox.SubjectEpoch = scope.SubjectEpoch
			outbox.ScopeID = scope.ID
			require.NoError(t, db.Create(&outbox).Error)

			_, readErr := repo.ListNodes(
				context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, nil, 20,
			)
			claims, claimErr := repo.ClaimCitationProfileEventOutbox(
				context.Background(), "worker-f7-current-gate-"+suffix, 1, now, time.Minute,
			)
			require.NoError(t, claimErr)
			if tc.valid {
				require.NoError(t, readErr)
				require.Len(t, claims, 1, "SQL CURRENT gate must accept every valid authority shape")
				return
			}
			require.ErrorIs(t, readErr, types.ErrCitationProfileUnavailable)
			require.Empty(t, claims, "SQL CURRENT gate must reject every invalid authority shape")
		})
	}
}

func TestCitationProfileOutboxStartAttemptRejectsACLBindingChangedAfterClaim(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-f7-binding-race", "user-a", "kb-a", "epoch-a", 7, 1)
	outbox := citationProfileOutboxFixture(
		"outbox-f7-binding-race",
		"event-f7-binding-race",
		types.CitationProfileOutboxStatusPending,
		now.Add(-time.Minute),
		nil,
		"",
		0,
	)
	outbox.ScopeID = scope.ID
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&outbox).Error)

	claims, err := repo.ClaimCitationProfileEventOutbox(context.Background(), "worker-f7-binding-race", 1, now, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("id = ?", scope.ID).
		Update("acl_principal_id", "").Error)

	_, err = repo.StartCitationProfileEventOutboxAttempt(context.Background(), &claims[0], now.Add(time.Second))
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost)
	var stored types.CitationProfileEventOutbox
	require.NoError(t, db.First(&stored, "id = ?", outbox.ID).Error)
	require.Zero(t, stored.AttemptCount, "a post-claim authority-binding change must fence the attempt")
}
