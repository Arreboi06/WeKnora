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
	"gorm.io/gorm"
)

func TestCitationProfileAutoLinkedGraphChangeAdvancesSnapshotWithoutBackfill(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	profileRepo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope(
		"scope-f1-auto-links",
		"user-f1-auto-links",
		"kb-f1-auto-links",
		"epoch-f1-auto-links",
		1,
		0,
	)
	require.NoError(t, db.Create(scope).Error)

	pageA := citationProfileF1TestPage("page-f1-auto-links-a", scope, "source-f1-auto-links-a", now)
	pageB := citationProfileF1TestPage("page-f1-auto-links-b", scope, "source-f1-auto-links-b", now.Add(time.Second))
	require.NoError(t, wikiRepo.Create(ctx, pageA))
	require.NoError(t, wikiRepo.Create(ctx, pageB))

	beforeGraph, err := profileRepo.GetGraph(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID)
	require.NoError(t, err)
	require.Empty(t, beforeGraph.Edges)
	firstPage, err := profileRepo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, firstPage.NextCursor)
	rawCursor, err := base64.RawURLEncoding.DecodeString(*firstPage.NextCursor)
	require.NoError(t, err)
	var oldCursor types.CitationProfileCursor
	require.NoError(t, json.Unmarshal(rawCursor, &oldCursor))

	var beforeScope types.CitationProfileScope
	require.NoError(t, db.First(&beforeScope, "id = ?", scope.ID).Error)
	pageVersion := pageA.Version
	pageA.Content = "linked [[" + pageB.Slug + "]]"
	pageA.OutLinks = types.StringArray{pageB.Slug}
	require.NoError(t, wikiRepo.UpdateAutoLinkedContent(ctx, pageA))

	var afterScope types.CitationProfileScope
	require.NoError(t, db.First(&afterScope, "id = ?", scope.ID).Error)
	require.Greater(t, afterScope.ProfileReadVersion, beforeScope.ProfileReadVersion,
		"an observable graph change must invalidate the prior read snapshot")
	require.Greater(t, afterScope.MappingRevision, beforeScope.MappingRevision)
	require.Zero(t, afterScope.PendingEventCount, "automatic links must not backfill evidence")
	require.Zero(t, afterScope.DirtyMappingCount, "automatic links do not change source-ref mappings")

	var storedPage types.WikiPage
	require.NoError(t, db.First(&storedPage, "id = ?", pageA.ID).Error)
	require.Equal(t, pageVersion, storedPage.Version, "automatic decoration must not create a user revision")
	require.Equal(t, types.StringArray{pageB.Slug}, storedPage.OutLinks)
	var eventCount int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", scope.TenantID, scope.KnowledgeBaseID).
		Count(&eventCount).Error)
	require.Zero(t, eventCount, "graph maintenance must preserve no-backfill")

	stalePage, err := profileRepo.ListNodes(
		ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, &oldCursor, 1,
	)
	require.Nil(t, stalePage)
	require.ErrorIs(t, err, types.ErrCitationProfileChanged)
	afterGraph, err := profileRepo.GetGraph(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID)
	require.NoError(t, err)
	require.NotEqual(t, beforeGraph.Snapshot.ReadVersion, afterGraph.Snapshot.ReadVersion)
	require.Equal(t, []types.CitationProfileGraphEdgeDTO{{
		SourcePageUUID: pageA.ID,
		TargetPageUUID: pageB.ID,
		EdgeType:       "wiki_link",
	}}, afterGraph.Edges)
}

func TestCitationProfileAutoLinkedGraphChangeRollsBackWithSnapshotFailure(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope(
		"scope-f1-auto-links-rollback",
		"user-f1-auto-links-rollback",
		"kb-f1-auto-links-rollback",
		"epoch-f1-auto-links-rollback",
		1,
		0,
	)
	require.NoError(t, db.Create(scope).Error)
	page := citationProfileF1TestPage(
		"page-f1-auto-links-rollback",
		scope,
		"source-f1-auto-links-rollback",
		now,
	)
	page.Content = "original content"
	require.NoError(t, wikiRepo.Create(ctx, page))

	var beforeScope types.CitationProfileScope
	require.NoError(t, db.First(&beforeScope, "id = ?", scope.ID).Error)
	snapshotFailure := errors.New("injected citation profile snapshot failure")
	callbackName := "p36_auto_links_snapshot_failure"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (types.CitationProfileScope{}).TableName() {
			tx.AddError(snapshotFailure)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	page.Content = "should roll back"
	page.OutLinks = types.StringArray{"doc/not-committed"}
	err := wikiRepo.UpdateAutoLinkedContent(ctx, page)
	require.ErrorIs(t, err, snapshotFailure)

	var storedPage types.WikiPage
	require.NoError(t, db.First(&storedPage, "id = ?", page.ID).Error)
	require.Equal(t, "original content", storedPage.Content)
	require.Empty(t, storedPage.OutLinks)
	var afterScope types.CitationProfileScope
	require.NoError(t, db.First(&afterScope, "id = ?", scope.ID).Error)
	require.Equal(t, beforeScope.ProfileReadVersion, afterScope.ProfileReadVersion)
	require.Equal(t, beforeScope.MappingRevision, afterScope.MappingRevision)
}

func TestCitationProfileWikiDriftSchedulingUsesDatabaseClock(t *testing.T) {
	db, scope, event, page, _ := citationProfileF1ResolvedFixture(t, "db-clock-schedule")
	wikiRepo := newCitationProfileWikiPageRepository(db)
	beforeDatabase, err := citationProfileDatabaseNow(db)
	require.NoError(t, err)

	page.SourceRefs = append(page.SourceRefs, "new-source-f1-db-clock|Source")
	require.NoError(t, wikiRepo.UpdateMeta(context.Background(), page))
	afterDatabase, err := citationProfileDatabaseNow(db)
	require.NoError(t, err)

	var outbox types.CitationProfileEventOutbox
	require.NoError(t, db.Where("scope_id = ? AND event_id = ?", scope.ID, event.ID).First(&outbox).Error)
	require.False(t, outbox.NextAttemptAt.Before(beforeDatabase),
		"resolution work must not be scheduled before the database transaction")
	require.False(t, outbox.NextAttemptAt.After(afterDatabase.Add(time.Second)),
		"a fast application clock must not postpone resolution work")
	var storedScope types.CitationProfileScope
	require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
	require.False(t, storedScope.UpdatedAt.After(afterDatabase.Add(time.Second)),
		"durable scope ordering timestamps must use the database clock")
}

func TestCitationProfileWikiDriftACLPauseDecisionUsesDatabaseClockWhenHostClockIsSlow(t *testing.T) {
	databaseNow := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	slowHostNow := databaseNow.Add(-2 * time.Minute)
	scope := citationProfileTestScope(
		"scope-f1-db-clock-acl-pause",
		"user-f1-db-clock-acl-pause",
		"kb-f1-db-clock-acl-pause",
		"epoch-f1-db-clock-acl-pause",
		1,
		0,
	)
	scope.ACLCheckState = types.CitationProfileACLStateCurrent
	checkedAt := databaseNow.Add(-10 * time.Minute)
	expiresAt := databaseNow.Add(-time.Minute)
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &expiresAt

	require.True(t, scope.ACLCurrentAt(slowHostNow),
		"the old host-clock decision would incorrectly keep this DB-expired ACL current")
	require.False(t, citationProfileScopeACLCurrent(scope, databaseNow),
		"Wiki invalidation must pause retry budget using the transaction's database clock")
}

func TestCitationProfileAutoLinkedContentRejectsStaleWikiRevision(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope(
		"scope-f1-auto-links-cas",
		"user-f1-auto-links-cas",
		"kb-f1-auto-links-cas",
		"epoch-f1-auto-links-cas",
		1,
		0,
	)
	require.NoError(t, db.Create(scope).Error)
	page := citationProfileF1TestPage("page-f1-auto-links-cas", scope, "source-f1-auto-links-cas", now)
	page.Content = "version one"
	require.NoError(t, wikiRepo.Create(ctx, page))
	staleDecoratorCopy := *page

	page.Content = "intentional user edit"
	require.NoError(t, wikiRepo.Update(ctx, page))
	require.Equal(t, 2, page.Version)
	var scopeAfterUserEdit types.CitationProfileScope
	require.NoError(t, db.First(&scopeAfterUserEdit, "id = ?", scope.ID).Error)

	staleDecoratorCopy.Content = "stale machine rewrite"
	staleDecoratorCopy.OutLinks = types.StringArray{"doc/stale-target"}
	err := wikiRepo.UpdateAutoLinkedContent(ctx, &staleDecoratorCopy)
	require.ErrorIs(t, err, ErrWikiPageConflict)

	var storedPage types.WikiPage
	require.NoError(t, db.First(&storedPage, "id = ?", page.ID).Error)
	require.Equal(t, 2, storedPage.Version)
	require.Equal(t, "intentional user edit", storedPage.Content)
	require.Empty(t, storedPage.OutLinks)
	var scopeAfterRejectedDecorator types.CitationProfileScope
	require.NoError(t, db.First(&scopeAfterRejectedDecorator, "id = ?", scope.ID).Error)
	require.Equal(t, scopeAfterUserEdit.ProfileReadVersion, scopeAfterRejectedDecorator.ProfileReadVersion)
	require.Equal(t, scopeAfterUserEdit.MappingRevision, scopeAfterRejectedDecorator.MappingRevision)
}

func TestCitationProfileUpdateMetaRejectsStaleWikiRevisionWithoutRestoringSourceUniverse(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope(
		"scope-f1-meta-cas",
		"user-f1-meta-cas",
		"kb-f1-meta-cas",
		"epoch-f1-meta-cas",
		1,
		0,
	)
	require.NoError(t, db.Create(scope).Error)
	page := citationProfileF1TestPage("page-f1-meta-cas", scope, "source-f1-meta-old", now)
	page.OutLinks = types.StringArray{"doc/old-target"}
	page.Aliases = types.StringArray{"old-alias"}
	require.NoError(t, wikiRepo.Create(ctx, page))
	staleBookkeepingCopy := *page

	page.Content = "intentional user edit"
	page.OutLinks = types.StringArray{"doc/new-target"}
	page.Aliases = types.StringArray{"new-alias"}
	page.SourceRefs = types.StringArray{"source-f1-meta-new|Source"}
	require.NoError(t, wikiRepo.Update(ctx, page))
	require.Equal(t, 2, page.Version)

	staleBookkeepingCopy.InLinks = types.StringArray{"doc/source"}
	err := wikiRepo.UpdateMeta(ctx, &staleBookkeepingCopy)
	require.ErrorIs(t, err, ErrWikiPageConflict)

	var storedPage types.WikiPage
	require.NoError(t, db.First(&storedPage, "id = ?", page.ID).Error)
	require.Equal(t, 2, storedPage.Version)
	require.Equal(t, "intentional user edit", storedPage.Content)
	require.Equal(t, types.StringArray{"doc/new-target"}, storedPage.OutLinks)
	require.Equal(t, types.StringArray{"new-alias"}, storedPage.Aliases)
	require.Equal(t, types.StringArray{"source-f1-meta-new|Source"}, storedPage.SourceRefs)
	require.Empty(t, storedPage.InLinks)

	var currentRefs []types.WikiSourceRefIndex
	require.NoError(t, db.Where(
		"tenant_id = ? AND knowledge_base_id = ? AND page_uuid = ? AND lifecycle_state = ?",
		page.TenantID,
		page.KnowledgeBaseID,
		page.ID,
		"current",
	).Find(&currentRefs).Error)
	require.Len(t, currentRefs, 1)
	require.Equal(t, "source-f1-meta-new", currentRefs[0].SourceKnowledgeID)
}

func TestCitationProfileSequentialSourceResolutionsDoNotMakeEachOtherStale(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-f1-independent-sources", "user-f1-independent-sources", "kb-f1-independent-sources", "epoch-f1-independent-sources", 1, 2)
	require.NoError(t, db.Create(scope).Error)

	pages := []types.WikiPage{
		*citationProfileF1TestPage("page-f1-source-a", scope, "knowledge-f1-source-a", now),
		*citationProfileF1TestPage("page-f1-source-b", scope, "knowledge-f1-source-b", now.Add(time.Second)),
	}
	require.NoError(t, db.Create(&pages).Error)
	refs := []types.WikiSourceRefIndex{
		citationProfileSourceRefRow("row-f1-source-a", scope.KnowledgeBaseID, "knowledge-f1-source-a", pages[0].ID, 1, 1),
		citationProfileSourceRefRow("row-f1-source-b", scope.KnowledgeBaseID, "knowledge-f1-source-b", pages[1].ID, 1, 2),
	}
	require.NoError(t, db.Create(&refs).Error)
	events := []types.CitationProfileEvent{
		*citationProfileF1TestEvent("event-f1-source-a", scope, "knowledge-f1-source-a", now),
		*citationProfileF1TestEvent("event-f1-source-b", scope, "knowledge-f1-source-b", now.Add(time.Second)),
	}
	require.NoError(t, db.Create(&events).Error)

	runA, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, events[0].ID)
	require.NoError(t, err)
	runB, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, events[1].ID)
	require.NoError(t, err)
	require.NotEqual(t, runA.RunUniverseWatermark, runB.RunUniverseWatermark, "fixture must exercise source-specific universe snapshots")

	var refreshedScope types.CitationProfileScope
	require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
	var links []types.EvidenceNodeLink
	require.NoError(t, db.Where("scope_id = ?", scope.ID).Order("page_uuid ASC").Find(&links).Error)
	require.Len(t, links, 2)
	currentRefs := make(map[citationProfileSourceRefKey]struct{}, len(refs))
	for _, ref := range refs {
		currentRefs[citationProfileSourceRefKeyFromIndex(ref)] = struct{}{}
	}
	var resolvedEvents []types.CitationProfileEvent
	require.NoError(t, db.Where("id IN ?", []string{events[0].ID, events[1].ID}).Find(&resolvedEvents).Error)
	require.Len(t, resolvedEvents, 2)
	liveEvents := make(map[string]types.CitationProfileEvent, len(resolvedEvents))
	for _, event := range resolvedEvents {
		require.Equal(t, types.CitationProfileEventStatusResolved, event.Status)
		liveEvents[event.ID] = event
	}
	counts := citationProfileNodeCountsFromLinks(&refreshedScope, links, currentRefs, liveEvents)
	for _, link := range links {
		require.False(t, citationProfileLinkStale(&refreshedScope, link, currentRefs),
			"resolving a different source in the same scope must not stale link %s", link.ID)
		require.False(t, citationProfileNodeStale(&refreshedScope, counts[link.PageUUID]),
			"resolving a different source in the same scope must not stale node %s", link.PageUUID)
	}
}

func TestCitationProfileCorrectionTargetsActiveRerunLink(t *testing.T) {
	db, scope, event, pageA, firstRun := citationProfileF1ResolvedFixture(t, "active-correction")
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	_, secondRun := citationProfileF1ExpandAndRerun(t, db, scope, event, "active-correction")

	var historical types.EvidenceNodeLink
	require.NoError(t, db.Where("event_id = ? AND resolution_run_id = ? AND page_uuid = ?", event.ID, firstRun.ID, pageA.ID).First(&historical).Error)
	var active types.EvidenceNodeLink
	require.NoError(t, db.Where("event_id = ? AND resolution_run_id = ? AND page_uuid = ?", event.ID, secondRun.ID, pageA.ID).First(&active).Error)
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("id = ?", historical.ID).UpdateColumn("id", "000-old-run-link").Error)
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("id = ?", active.ID).UpdateColumn("id", "zzz-active-run-link").Error)

	var currentScope types.CitationProfileScope
	require.NoError(t, db.First(&currentScope, "id = ?", scope.ID).Error)
	_, err := repo.ApplyCorrection(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID,
		currentScope.ProfileReadVersion, "idem-active-run-link", types.CitationCorrectionRejectMapping,
		event.ID, pageA.ID, "incorrect_mapping")
	require.NoError(t, err)

	historical = types.EvidenceNodeLink{}
	active = types.EvidenceNodeLink{}
	require.NoError(t, db.First(&historical, "id = ?", "000-old-run-link").Error)
	require.NoError(t, db.First(&active, "id = ?", "zzz-active-run-link").Error)
	require.Equal(t, types.EvidenceRelationHistorical, historical.RelationState,
		"correction must not mutate a historical link from a superseded run")
	require.Equal(t, types.EvidenceRelationDisputed, active.RelationState,
		"correction must target event.ActiveRunID")
}

func TestCitationProfileRejectedMappingStaysDisputedAfterWikiRerun(t *testing.T) {
	db, scope, event, pageA, _ := citationProfileF1ResolvedFixture(t, "rejected-rerun")
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	var currentScope types.CitationProfileScope
	require.NoError(t, db.First(&currentScope, "id = ?", scope.ID).Error)
	_, err := repo.ApplyCorrection(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID,
		currentScope.ProfileReadVersion, "idem-rejected-rerun", types.CitationCorrectionRejectMapping,
		event.ID, pageA.ID, "incorrect_mapping")
	require.NoError(t, err)

	_, secondRun := citationProfileF1ExpandAndRerun(t, db, scope, event, "rejected-rerun")
	var rerunLink types.EvidenceNodeLink
	require.NoError(t, db.Where("event_id = ? AND resolution_run_id = ? AND page_uuid = ?", event.ID, secondRun.ID, pageA.ID).First(&rerunLink).Error)
	require.Equal(t, types.EvidenceRelationDisputed, rerunLink.RelationState,
		"a wiki source-universe rerun must inherit the latest correction overlay")
	var currentRejectedMapping int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("event_id = ? AND page_uuid = ? AND relation_state = ?", event.ID, pageA.ID, types.EvidenceRelationCurrent).
		Count(&currentRejectedMapping).Error)
	require.Zero(t, currentRejectedMapping, "rerun must not republish a rejected event/page mapping as current")
}

func TestCitationProfileWikiRerunClearsDirtyMappingCount(t *testing.T) {
	db, scope, event, _, _ := citationProfileF1ResolvedFixture(t, "dirty-cleared")
	repo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	pageB := citationProfileF1TestPage("page-f1-dirty-cleared-b", scope, event.SourceKnowledgeID, time.Now().UTC().Add(time.Minute))
	require.NoError(t, wikiRepo.Create(ctx, pageB))

	var dirty types.CitationProfileScope
	require.NoError(t, db.First(&dirty, "id = ?", scope.ID).Error)
	require.Greater(t, dirty.DirtyMappingCount, 0, "fixture must enter a dirty mapping state")
	require.Equal(t, 1, dirty.PendingEventCount)
	_, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)

	var resolved types.CitationProfileScope
	require.NoError(t, db.First(&resolved, "id = ?", scope.ID).Error)
	require.Zero(t, resolved.PendingEventCount)
	require.Zero(t, resolved.DirtyMappingCount,
		"successful rerun must discharge the mapping dirtiness that it resolved")
}

func citationProfileF1ResolvedFixture(
	t *testing.T,
	prefix string,
) (*gorm.DB, *types.CitationProfileScope, *types.CitationProfileEvent, *types.WikiPage, *types.EvidenceResolutionRun) {
	t.Helper()
	db := newCitationProfileRepositoryTestDB(t)
	scope := citationProfileTestScope("scope-f1-"+prefix, "user-f1-"+prefix, "kb-f1-"+prefix, "epoch-f1-"+prefix, 1, 1)
	require.NoError(t, db.Create(scope).Error)
	now := time.Now().UTC()
	pageA := citationProfileF1TestPage("page-f1-"+prefix+"-a", scope, "knowledge-f1-"+prefix, now)
	require.NoError(t, newCitationProfileWikiPageRepository(db).Create(context.Background(), pageA))
	event := citationProfileF1TestEvent("event-f1-"+prefix, scope, "knowledge-f1-"+prefix, now)
	require.NoError(t, db.Create(event).Error)
	run, err := NewCitationProfileRepository(db).ResolveEvidenceEvent(context.Background(), scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, run.Status)
	return db, scope, event, pageA, run
}

func citationProfileF1ExpandAndRerun(
	t *testing.T,
	db *gorm.DB,
	scope *types.CitationProfileScope,
	event *types.CitationProfileEvent,
	prefix string,
) (*types.WikiPage, *types.EvidenceResolutionRun) {
	t.Helper()
	pageB := citationProfileF1TestPage("page-f1-"+prefix+"-b", scope, event.SourceKnowledgeID, time.Now().UTC().Add(time.Minute))
	require.NoError(t, newCitationProfileWikiPageRepository(db).Create(context.Background(), pageB))
	var invalidated types.CitationProfileEvent
	require.NoError(t, db.First(&invalidated, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, invalidated.Status)
	require.Empty(t, invalidated.ActiveRunID)
	run, err := NewCitationProfileRepository(db).ResolveEvidenceEvent(context.Background(), scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, run.Status)
	return pageB, run
}

func citationProfileF1TestEvent(id string, scope *types.CitationProfileScope, sourceKnowledgeID string, now time.Time) *types.CitationProfileEvent {
	return &types.CitationProfileEvent{
		ID:                 id,
		TenantID:           scope.TenantID,
		SubjectID:          scope.SubjectID,
		KnowledgeBaseID:    scope.KnowledgeBaseID,
		SubjectEpoch:       scope.SubjectEpoch,
		ScopeID:            scope.ID,
		MessageID:          "message-" + id,
		SourceKnowledgeID:  sourceKnowledgeID,
		SourceRefsSnapshot: json.RawMessage(`{}`),
		KnowledgeSnapshot:  json.RawMessage(`{}`),
		KnowledgeBaseProof: json.RawMessage(`{}`),
		ProducerEventKey:   "producer-" + id,
		Status:             types.CitationProfileEventStatusPendingResolution,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func citationProfileF1TestPage(id string, scope *types.CitationProfileScope, sourceKnowledgeID string, now time.Time) *types.WikiPage {
	return &types.WikiPage{
		ID:              id,
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/" + id,
		Title:           id,
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{sourceKnowledgeID + "|Source"},
		UpdatedAt:       now,
	}
}
