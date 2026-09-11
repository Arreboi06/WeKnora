//go:build cgo

package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileWikiPageWithoutMatchingEventInvalidatesEveryReadableScopeCursor(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	kbID := "kb-f1-page-universe"
	scopeA := citationProfileTestScope("scope-f1-page-universe-a", "user-f1-page-universe-a", kbID, "epoch-f1-page-universe-a", 1, 0)
	scopeB := citationProfileTestScope("scope-f1-page-universe-b", "user-f1-page-universe-b", kbID, "epoch-f1-page-universe-b", 1, 0)
	require.NoError(t, db.Create(&[]types.CitationProfileScope{*scopeB, *scopeA}).Error)
	for i, pageID := range []string{"page-f1-page-universe-a", "page-f1-page-universe-b"} {
		require.NoError(t, wikiRepo.Create(ctx, citationProfileF1TestPage(
			pageID,
			scopeA,
			"unmatched-setup-source-"+pageID,
			now.Add(time.Duration(i)*time.Second),
		)))
	}

	readCursor := func(subjectID string) (types.CitationProfileCursor, types.CitationProfileScope) {
		t.Helper()
		page, err := repo.ListNodes(ctx, scopeA.TenantID, subjectID, kbID, nil, 1)
		require.NoError(t, err)
		require.NotNil(t, page.NextCursor)
		raw, err := base64.RawURLEncoding.DecodeString(*page.NextCursor)
		require.NoError(t, err)
		var cursor types.CitationProfileCursor
		require.NoError(t, json.Unmarshal(raw, &cursor))
		var scope types.CitationProfileScope
		require.NoError(t, db.Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", scopeA.TenantID, subjectID, kbID).First(&scope).Error)
		return cursor, scope
	}
	cursorA, beforeA := readCursor(scopeA.SubjectID)
	cursorB, beforeB := readCursor(scopeB.SubjectID)

	require.NoError(t, wikiRepo.Create(ctx, citationProfileF1TestPage(
		"page-f1-page-universe-new",
		scopeA,
		"unmatched-new-source",
		now.Add(time.Minute),
	)))
	var eventCount int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).Where("knowledge_base_id = ?", kbID).Count(&eventCount).Error)
	require.Zero(t, eventCount, "page-universe invalidation must not backfill citation events")

	for _, item := range []struct {
		subject string
		before  types.CitationProfileScope
		cursor  types.CitationProfileCursor
	}{
		{subject: scopeA.SubjectID, before: beforeA, cursor: cursorA},
		{subject: scopeB.SubjectID, before: beforeB, cursor: cursorB},
	} {
		var after types.CitationProfileScope
		require.NoError(t, db.Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", scopeA.TenantID, item.subject, kbID).First(&after).Error)
		require.Greater(t, after.ProfileReadVersion, item.before.ProfileReadVersion)
		require.Greater(t, after.MappingRevision, item.before.MappingRevision)
		_, err := repo.ListNodes(ctx, scopeA.TenantID, item.subject, kbID, &item.cursor, 1)
		require.ErrorIs(t, err, types.ErrCitationProfileChanged)
	}
}

func TestCitationProfileWikiDriftDuringACLUnknownRequeuesAndResumesOnAllow(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope(
		"scope-f1-acl-unknown-drift",
		"user-f1-acl-unknown-drift",
		"kb-f1-acl-unknown-drift",
		"epoch-f1-acl-unknown-drift",
		1,
		1,
	)
	require.NoError(t, db.Create(scope).Error)
	event := citationProfileF1TestEvent(
		"event-f1-acl-unknown-drift",
		scope,
		"knowledge-f1-acl-unknown-drift",
		now,
	)
	require.NoError(t, db.Create(event).Error)
	resolvedEmptyRun, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, resolvedEmptyRun.Status)

	deliveredAt := now.Add(time.Second)
	outbox := citationProfileOutboxFixture(
		"outbox-f1-acl-unknown-drift",
		event.ID,
		types.CitationProfileOutboxStatusDelivered,
		deliveredAt,
		nil,
		"",
		1,
	)
	outbox.TenantID = scope.TenantID
	outbox.SubjectID = scope.SubjectID
	outbox.KnowledgeBaseID = scope.KnowledgeBaseID
	outbox.SubjectEpoch = scope.SubjectEpoch
	outbox.ScopeID = scope.ID
	outbox.DeliveredAt = &deliveredAt
	require.NoError(t, db.Create(&outbox).Error)

	for i := 0; i < 2; i++ {
		require.NoError(t, wikiRepo.Create(ctx, citationProfileF1TestPage(
			"page-f1-acl-unknown-unrelated-"+string(rune('a'+i)),
			scope,
			"unrelated-f1-acl-unknown-"+string(rune('a'+i)),
			now.Add(time.Duration(i+1)*time.Minute),
		)))
	}
	firstPage, err := repo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, firstPage.NextCursor)
	rawCursor, err := base64.RawURLEncoding.DecodeString(*firstPage.NextCursor)
	require.NoError(t, err)
	var oldCursor types.CitationProfileCursor
	require.NoError(t, json.Unmarshal(rawCursor, &oldCursor))

	require.NoError(t, repo.InvalidateCitationProfileACL(ctx, types.CitationProfileACLMutation{
		SourceTenantID:  scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
	}))
	var beforeDrift types.CitationProfileScope
	require.NoError(t, db.First(&beforeDrift, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, beforeDrift.ACLCheckState)
	var eventCountBefore int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("scope_id = ?", scope.ID).
		Count(&eventCountBefore).Error)

	matchingPage := citationProfileF1TestPage(
		"page-f1-acl-unknown-matching",
		scope,
		event.SourceKnowledgeID,
		now.Add(3*time.Minute),
	)
	require.NoError(t, wikiRepo.Create(ctx, matchingPage))

	var afterDrift types.CitationProfileScope
	require.NoError(t, db.First(&afterDrift, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, afterDrift.ACLCheckState)
	require.Greater(t, afterDrift.ProfileReadVersion, beforeDrift.ProfileReadVersion)
	require.Greater(t, afterDrift.MappingRevision, beforeDrift.MappingRevision)
	require.NotEqual(t, beforeDrift.SourceUniverseWatermark, afterDrift.SourceUniverseWatermark)
	require.Equal(t, 1, afterDrift.PendingEventCount)
	require.Equal(t, 1, afterDrift.DirtyMappingCount)

	var requeued types.CitationProfileEvent
	require.NoError(t, db.First(&requeued, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, requeued.Status)
	require.Empty(t, requeued.ActiveRunID)
	require.Nil(t, requeued.ResolvedAt)
	require.Equal(t, "wiki_source_ref_drift", requeued.PendingReason)
	var eventCountAfter int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("scope_id = ?", scope.ID).
		Count(&eventCountAfter).Error)
	require.Equal(t, eventCountBefore, eventCountAfter, "Wiki drift must not backfill citation events")

	var paused types.CitationProfileEventOutbox
	require.NoError(t, db.First(&paused, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, paused.Status)
	require.Nil(t, paused.DeliveredAt)
	require.Nil(t, paused.DeadletterAt)
	require.NotNil(t, paused.RetryBudgetPausedAt)
	require.Zero(t, paused.RetryBudgetPausedSeconds)
	require.Zero(t, paused.AttemptCount)

	claimAt := time.Now().UTC()
	// Give the database clock a small boundary margin: durable pause
	// accounting intentionally ignores the caller's wall clock and truncates
	// completed seconds, so an exact application-clock boundary is flaky.
	simulatedPausedAt := claimAt.Add(-48*time.Hour - 2*time.Second)
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).
		Where("id = ?", outbox.ID).
		Update("retry_budget_paused_at", simulatedPausedAt).Error)
	aclClaims, err := repo.ClaimCitationProfileACLScopes(ctx, "acl-f1-unknown-drift", 1, claimAt, time.Minute)
	require.NoError(t, err)
	require.Len(t, aclClaims, 1)
	require.Equal(t, scope.ID, aclClaims[0].ScopeID)
	allowAt := time.Now().UTC()
	require.NoError(t, repo.ApplyCitationProfileACLResult(
		ctx,
		&aclClaims[0],
		types.CitationProfileACLDecisionAllow,
		allowAt,
		allowAt.Add(time.Hour),
	))

	var resumed types.CitationProfileEventOutbox
	require.NoError(t, db.First(&resumed, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, resumed.Status)
	require.Nil(t, resumed.RetryBudgetPausedAt)
	require.GreaterOrEqual(t, resumed.RetryBudgetPausedSeconds, int64((48*time.Hour)/time.Second))
	outboxClaimAt := allowAt.Add(time.Second)
	if !outboxClaimAt.After(resumed.NextAttemptAt) {
		outboxClaimAt = resumed.NextAttemptAt.Add(time.Second)
	}
	require.True(t, outboxClaimAt.Before(allowAt.Add(time.Hour)), "requeued work must become due inside the refreshed ACL window")
	claims, err := repo.ClaimCitationProfileEventOutbox(ctx, "outbox-f1-unknown-drift", 1, outboxClaimAt, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, event.ID, claims[0].EventID)
	_, err = repo.StartCitationProfileEventOutboxAttempt(ctx, &claims[0], outboxClaimAt.Add(time.Second))
	require.NoError(t, err)
	_, err = repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost,
		"a foreground resolver must not bypass a live worker lease")
	run, err := repo.ResolveClaimedEvidenceEvent(ctx, &claims[0])
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, run.Status)
	var currentLink types.EvidenceNodeLink
	require.NoError(t, db.Where(
		"event_id = ? AND page_uuid = ? AND relation_state = ?",
		event.ID,
		matchingPage.ID,
		types.EvidenceRelationCurrent,
	).First(&currentLink).Error)
	_, err = repo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, &oldCursor, 1)
	require.ErrorIs(t, err, types.ErrCitationProfileChanged)
}

func TestCitationProfileDriftDeadletterReleasesDirtyCountBeforeRequeue(t *testing.T) {
	db, scope, event, _, _ := citationProfileF1ResolvedFixture(t, "deadletter-dirty")
	repo := &citationProfileRepository{db: db}
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, wikiRepo.Create(ctx, citationProfileF1TestPage(
		"page-f1-deadletter-dirty-b",
		scope,
		event.SourceKnowledgeID,
		now.Add(time.Minute),
	)))
	var dirty types.CitationProfileScope
	require.NoError(t, db.First(&dirty, "id = ?", scope.ID).Error)
	require.Equal(t, 1, dirty.DirtyMappingCount)
	require.Equal(t, 1, dirty.PendingEventCount)

	deadletterAt := now.Add(2 * time.Minute)
	claims, err := repo.ClaimCitationProfileEventOutbox(ctx, "f1-deadletter-dirty-worker", 1, deadletterAt, time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, event.ID, claims[0].EventID)
	require.NoError(t, repo.DeadletterCitationProfileEventOutbox(ctx, &claims[0], deadletterAt, errors.New("forced test terminal failure")))
	dirty = types.CitationProfileScope{}
	require.NoError(t, db.First(&dirty, "id = ?", scope.ID).Error)
	require.Zero(t, dirty.DirtyMappingCount, "deadletter terminalization must discharge drift dirtiness")
	require.Zero(t, dirty.PendingEventCount)

	require.NoError(t, wikiRepo.Create(ctx, citationProfileF1TestPage(
		"page-f1-deadletter-dirty-c",
		scope,
		event.SourceKnowledgeID,
		now.Add(3*time.Minute),
	)))
	dirty = types.CitationProfileScope{}
	require.NoError(t, db.First(&dirty, "id = ?", scope.ID).Error)
	require.Equal(t, 1, dirty.DirtyMappingCount)
	require.Equal(t, 1, dirty.PendingEventCount)
	_, err = repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	dirty = types.CitationProfileScope{}
	require.NoError(t, db.First(&dirty, "id = ?", scope.ID).Error)
	require.Zero(t, dirty.DirtyMappingCount)
	require.Zero(t, dirty.PendingEventCount)
}
