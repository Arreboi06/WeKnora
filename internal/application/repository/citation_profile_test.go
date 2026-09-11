//go:build cgo

package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

func TestCitationProfileRepositorySetEnrollmentCreatesRandomEpochAndChecksReadVersion(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "user-7")
	ctx = types.WithAuthenticatedTenantID(ctx, 7)
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalWebUser, ID: "user-7"})
	ctx = types.WithCitationProfileACLBinding(ctx, types.CitationProfileACLBinding{
		PrincipalType:         types.PrincipalWebUser,
		PrincipalID:           "user-7",
		AuthenticatedTenantID: 7,
		AccessPath:            types.CitationProfileACLAccessPathOwner,
	})
	zero := uint64(0)
	idem := "11111111-1111-4111-8111-111111111111"

	scope, err := repo.SetEnrollment(ctx, 7, " user-7 ", " kb-a ", true, &zero, idem)
	require.NoError(t, err)
	require.NotNil(t, scope)
	require.True(t, scope.Enabled)
	require.Equal(t, "user-7", scope.SubjectID)
	require.Equal(t, "kb-a", scope.KnowledgeBaseID)
	require.Equal(t, uint64(1), scope.ProfileReadVersion)
	_, err = uuid.Parse(scope.SubjectEpoch)
	require.NoError(t, err)

	_, err = repo.SetEnrollment(ctx, 7, "user-7", "kb-a", true, &zero, "22222222-2222-4222-8222-222222222222")
	require.ErrorIs(t, err, types.ErrCitationProfileChanged)

	replayed, err := repo.SetEnrollment(ctx, 7, "user-7", "kb-a", true, &zero, idem)
	require.NoError(t, err)
	require.Equal(t, scope.ID, replayed.ID)
	require.Equal(t, scope.SubjectEpoch, replayed.SubjectEpoch)
}

func TestCitationProfileRepositoryStaleDisableWritesNothing(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	scope := citationProfileTestScope("scope-stale-disable", "user-7", "kb-a", "epoch-stale-disable", 9, 0)
	require.NoError(t, db.Create(scope).Error)

	stale := uint64(8)
	_, err := repo.SetEnrollment(
		context.Background(),
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		false,
		&stale,
		"99999999-9999-4999-8999-999999999999",
	)
	require.ErrorIs(t, err, types.ErrCitationProfileChanged)

	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
	require.True(t, stored.Enabled)
	require.Nil(t, stored.FencedAt)
	require.Nil(t, stored.DeletedAt)
	require.Equal(t, scope.ProfileReadVersion, stored.ProfileReadVersion)

	var operationCount int64
	require.NoError(t, db.Model(&types.CitationProfileOperation{}).
		Where("scope_id = ?", scope.ID).
		Count(&operationCount).Error)
	require.Zero(t, operationCount)
}

func TestCitationProfileBlindDeleteRaceRollsBackOperationButKeepsGenericReceipt(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	scope := citationProfileTestScope("scope-blind-race", "user-7", "kb-a", "epoch-blind-race", 5, 0)
	require.NoError(t, db.Create(scope).Error)

	callbackName := "topic4_force_blind_delete_fence_race"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (types.CitationProfileScope{}).TableName() {
			return
		}
		updates, ok := tx.Statement.Dest.(map[string]interface{})
		if !ok {
			return
		}
		if _, fencing := updates["fenced_at"]; !fencing {
			return
		}
		tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	receipt, err := repo.RequestBlindDelete(
		context.Background(),
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
	)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	require.Equal(t, types.CitationOperationDeleteBlind, receipt.OperationType)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, receipt.Status)

	var operationCount int64
	require.NoError(t, db.Model(&types.CitationProfileOperation{}).
		Where("scope_id = ?", scope.ID).
		Count(&operationCount).Error)
	require.Zero(t, operationCount, "a failed fence must roll back its operation row")

	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
	require.True(t, stored.Enabled)
	require.Nil(t, stored.FencedAt)
	require.Nil(t, stored.DeletedAt)
	require.Equal(t, scope.ProfileReadVersion, stored.ProfileReadVersion)
}

func TestCitationProfileRepositoryResolveEvidenceEventExactZeroOneMany(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-a", "user-7", "kb-a", "epoch-a", 10, 3)
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&types.CitationProfileEvent{
		ID:                   "event-zero",
		TenantID:             7,
		SubjectID:            "user-7",
		KnowledgeBaseID:      "kb-a",
		SubjectEpoch:         "epoch-a",
		ScopeID:              scope.ID,
		MessageID:            "message-zero",
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    "knowledge-zero",
		SourceRefsSnapshot:   json.RawMessage("{}"),
		KnowledgeSnapshot:    json.RawMessage("{}"),
		KnowledgeBaseProof:   json.RawMessage("{}"),
		ProducerEventKey:     "event-zero-key",
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error)
	require.NoError(t, db.Create(&types.CitationProfileEvent{
		ID:                   "event-one",
		TenantID:             7,
		SubjectID:            "user-7",
		KnowledgeBaseID:      "kb-a",
		SubjectEpoch:         "epoch-a",
		ScopeID:              scope.ID,
		MessageID:            "message-one",
		OriginReferenceIndex: 1,
		SourceKnowledgeID:    "knowledge-one",
		SourceRefsSnapshot:   json.RawMessage("{}"),
		KnowledgeSnapshot:    json.RawMessage("{}"),
		KnowledgeBaseProof:   json.RawMessage("{}"),
		ProducerEventKey:     "event-one-key",
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error)
	require.NoError(t, db.Create(&types.CitationProfileEvent{
		ID:                   "event-many",
		TenantID:             7,
		SubjectID:            "user-7",
		KnowledgeBaseID:      "kb-a",
		SubjectEpoch:         "epoch-a",
		ScopeID:              scope.ID,
		MessageID:            "message-many",
		OriginReferenceIndex: 2,
		SourceKnowledgeID:    "knowledge-many",
		SourceRefsSnapshot:   json.RawMessage("{}"),
		KnowledgeSnapshot:    json.RawMessage("{}"),
		KnowledgeBaseProof:   json.RawMessage("{}"),
		ProducerEventKey:     "event-many-key",
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error)
	require.NoError(t, db.Create(&[]types.WikiSourceRefIndex{
		citationProfileSourceRefRow("row-one", "kb-a", "knowledge-one", "page-one", 1, 11),
		citationProfileSourceRefRow("row-many-a", "kb-a", "knowledge-many", "page-many-a", 1, 12),
		citationProfileSourceRefRow("row-many-b", "kb-a", "knowledge-many", "page-many-b", 2, 13),
	}).Error)

	runZero, err := repo.ResolveEvidenceEvent(ctx, 7, "user-7", "event-zero")
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, runZero.Status)
	require.Equal(t, 0, runZero.OutputCount)

	runOne, err := repo.ResolveEvidenceEvent(ctx, 7, "user-7", "event-one")
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, runOne.Status)
	require.Equal(t, 1, runOne.OutputCount)

	runMany, err := repo.ResolveEvidenceEvent(ctx, 7, "user-7", "event-many")
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, runMany.Status)
	require.Equal(t, 2, runMany.OutputCount)

	var links int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("event_id = ?", "event-many").Count(&links).Error)
	require.Equal(t, int64(2), links)

	var refreshed types.CitationProfileScope
	require.NoError(t, db.First(&refreshed, "id = ?", scope.ID).Error)
	require.Equal(t, uint64(13), refreshed.ProfileReadVersion)
	require.Equal(t, 0, refreshed.PendingEventCount)
	require.Equal(t, runMany.ID, refreshed.ActiveRunID)
}

func TestWikiSourceUniverseChangesInvalidateResolvedEmptyEventsAndOldCursors(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-f1-universe", "user-f1", "kb-f1", "epoch-f1", 10, 0)
	require.NoError(t, db.Create(scope).Error)

	newEvent := func(id, sourceID string) *types.CitationProfileEvent {
		event := &types.CitationProfileEvent{
			ID:                 id,
			TenantID:           scope.TenantID,
			SubjectID:          scope.SubjectID,
			KnowledgeBaseID:    scope.KnowledgeBaseID,
			SubjectEpoch:       scope.SubjectEpoch,
			ScopeID:            scope.ID,
			MessageID:          "message-" + id,
			SourceKnowledgeID:  sourceID,
			SourceRefsSnapshot: json.RawMessage(`{}`),
			KnowledgeSnapshot:  json.RawMessage(`{}`),
			KnowledgeBaseProof: json.RawMessage(`{}`),
			ProducerEventKey:   "producer-" + id,
			Status:             types.CitationProfileEventStatusPendingResolution,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		require.NoError(t, db.Create(event).Error)
		resolved, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
		require.NoError(t, err)
		require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, resolved.Status)
		return event
	}

	addedEvent := newEvent("event-f1-added", "knowledge-added")
	newPageEvent := newEvent("event-f1-new-page", "knowledge-new-page")

	existingPage := &types.WikiPage{
		ID:              "page-f1-existing",
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-existing",
		Title:           "F1 existing",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{"knowledge-existing|Existing"},
		UpdatedAt:       now,
	}
	otherPage := &types.WikiPage{
		ID:              "page-f1-other",
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-other",
		Title:           "F1 other",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		UpdatedAt:       now,
	}
	require.NoError(t, wikiRepo.Create(ctx, existingPage))
	require.NoError(t, wikiRepo.Create(ctx, otherPage))

	first, err := repo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor, "two pages must produce an old cursor to invalidate")
	decoded, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	require.NoError(t, err)
	var oldCursor types.CitationProfileCursor
	require.NoError(t, json.Unmarshal(decoded, &oldCursor))

	newPage := &types.WikiPage{
		ID:              "page-f1-new",
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-new",
		Title:           "F1 new",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{"knowledge-new-page|New page"},
		UpdatedAt:       now.Add(time.Minute),
	}
	// A newly-created page must invalidate a previously resolved-empty event
	// even though that event has no current link to discover it.
	require.NoError(t, wikiRepo.Create(ctx, newPage))
	var storedNewPageEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedNewPageEvent, "id = ?", newPageEvent.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedNewPageEvent.Status)
	require.Empty(t, storedNewPageEvent.ActiveRunID)

	// Adding a source ref to an existing page has the same obligation.
	existingPage.SourceRefs = types.StringArray{"knowledge-existing|Existing", "knowledge-added|Added"}
	require.NoError(t, wikiRepo.UpdateMeta(ctx, existingPage))
	var storedAddedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedAddedEvent, "id = ?", addedEvent.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedAddedEvent.Status)
	require.Empty(t, storedAddedEvent.ActiveRunID)

	var refreshedScope types.CitationProfileScope
	require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
	require.Greater(t, refreshedScope.ProfileReadVersion, scope.ProfileReadVersion)
	require.Greater(t, refreshedScope.MappingRevision, scope.MappingRevision)

	// Invalidation is no-backfill: the old terminal run remains historical and
	// no current link is published until the outbox resolves the pending event.
	var currentLinks int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("event_id IN ? AND relation_state = ?", []string{addedEvent.ID, newPageEvent.ID}, types.EvidenceRelationCurrent).
		Count(&currentLinks).Error)
	require.Zero(t, currentLinks)

	_, err = repo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, &oldCursor, 1)
	require.ErrorIs(t, err, types.ErrCitationProfileChanged)
}

func TestWikiSourceUniverseExpansionRetiresPriorRunAndRepublishesExactCurrentSet(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-f1-expanded", "user-f1-expanded", "kb-f1-expanded", "epoch-f1-expanded", 1, 1)
	require.NoError(t, db.Create(scope).Error)

	pageA := &types.WikiPage{
		ID:              "page-f1-expanded-a",
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-expanded-a",
		Title:           "F1 expanded A",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{"knowledge-f1-expanded|A"},
		UpdatedAt:       now,
	}
	require.NoError(t, wikiRepo.Create(ctx, pageA))
	event := &types.CitationProfileEvent{
		ID:                 "event-f1-expanded",
		TenantID:           scope.TenantID,
		SubjectID:          scope.SubjectID,
		KnowledgeBaseID:    scope.KnowledgeBaseID,
		SubjectEpoch:       scope.SubjectEpoch,
		ScopeID:            scope.ID,
		MessageID:          "message-f1-expanded",
		SourceKnowledgeID:  "knowledge-f1-expanded",
		SourceRefsSnapshot: json.RawMessage(`{}`),
		KnowledgeSnapshot:  json.RawMessage(`{}`),
		KnowledgeBaseProof: json.RawMessage(`{}`),
		ProducerEventKey:   "producer-f1-expanded",
		Status:             types.CitationProfileEventStatusPendingResolution,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	require.NoError(t, db.Create(event).Error)
	firstRun, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, firstRun.Status)

	var firstRunLinks []types.EvidenceNodeLink
	require.NoError(t, db.Where("resolution_run_id = ?", firstRun.ID).Find(&firstRunLinks).Error)
	require.Len(t, firstRunLinks, 1)
	require.Equal(t, types.EvidenceRelationCurrent, firstRunLinks[0].RelationState)

	pageB := &types.WikiPage{
		ID:              "page-f1-expanded-b",
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-expanded-b",
		Title:           "F1 expanded B",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{"knowledge-f1-expanded|B"},
		UpdatedAt:       now.Add(time.Minute),
	}
	require.NoError(t, wikiRepo.Create(ctx, pageB))

	var invalidated types.CitationProfileEvent
	require.NoError(t, db.First(&invalidated, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, invalidated.Status)
	require.Empty(t, invalidated.ActiveRunID)
	var currentBeforeRerun int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("event_id = ? AND relation_state = ?", event.ID, types.EvidenceRelationCurrent).
		Count(&currentBeforeRerun).Error)
	if currentBeforeRerun != 0 {
		t.Errorf("source-universe invalidation left %d prior-run current links; want 0", currentBeforeRerun)
	}

	secondRun, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	require.NotEqual(t, firstRun.ID, secondRun.ID)
	var currentAfterRerun []types.EvidenceNodeLink
	require.NoError(t, db.Where("event_id = ? AND relation_state = ?", event.ID, types.EvidenceRelationCurrent).
		Order("page_uuid ASC, id ASC").Find(&currentAfterRerun).Error)
	require.Len(t, currentAfterRerun, 2, "rerun must publish exactly one current link per current source-ref row")
	require.Equal(t, pageA.ID, currentAfterRerun[0].PageUUID)
	require.Equal(t, pageB.ID, currentAfterRerun[1].PageUUID)
	for _, link := range currentAfterRerun {
		require.Equal(t, secondRun.ID, link.ResolutionRunID)
	}
	var firstRunHistorical int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("resolution_run_id = ? AND relation_state = ?", firstRun.ID, types.EvidenceRelationHistorical).
		Count(&firstRunHistorical).Error)
	require.Equal(t, int64(1), firstRunHistorical)
}

func TestResolveEvidenceEventPublishesLinksAtRunMappingRevision(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-f1-revision", "user-f1-revision", "kb-f1-revision", "epoch-f1-revision", 1, 1)
	scope.MappingRevision = 10
	scope.SourceUniverseWatermark = "wm-before-rerun"
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&[]types.WikiSourceRefIndex{
		citationProfileSourceRefRow("row-f1-revision-a", scope.KnowledgeBaseID, "knowledge-f1-revision", "page-f1-revision-a", 1, 1),
		citationProfileSourceRefRow("row-f1-revision-b", scope.KnowledgeBaseID, "knowledge-f1-revision", "page-f1-revision-b", 3, 3),
	}).Error)
	event := &types.CitationProfileEvent{
		ID:                 "event-f1-revision",
		TenantID:           scope.TenantID,
		SubjectID:          scope.SubjectID,
		KnowledgeBaseID:    scope.KnowledgeBaseID,
		SubjectEpoch:       scope.SubjectEpoch,
		ScopeID:            scope.ID,
		MessageID:          "message-f1-revision",
		SourceKnowledgeID:  "knowledge-f1-revision",
		SourceRefsSnapshot: json.RawMessage(`{}`),
		KnowledgeSnapshot:  json.RawMessage(`{}`),
		KnowledgeBaseProof: json.RawMessage(`{}`),
		ProducerEventKey:   "producer-f1-revision",
		Status:             types.CitationProfileEventStatusPendingResolution,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	require.NoError(t, db.Create(event).Error)

	run, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	var refreshedScope types.CitationProfileScope
	require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
	require.Equal(t, run.RunMappingRevision, refreshedScope.MappingRevision)
	var links []types.EvidenceNodeLink
	require.NoError(t, db.Where("resolution_run_id = ?", run.ID).Order("page_uuid ASC").Find(&links).Error)
	require.Len(t, links, 2)
	var refs []types.WikiSourceRefIndex
	require.NoError(t, db.Where("knowledge_base_id = ? AND source_knowledge_id = ? AND lifecycle_state = ?",
		scope.KnowledgeBaseID, event.SourceKnowledgeID, "current").Find(&refs).Error)
	currentKeys := make(map[citationProfileSourceRefKey]struct{}, len(refs))
	for _, ref := range refs {
		currentKeys[citationProfileSourceRefKeyFromIndex(ref)] = struct{}{}
	}
	for _, link := range links {
		require.Equal(t, run.RunMappingRevision, link.MappingRevision)
		require.False(t, citationProfileLinkStale(&refreshedScope, link, currentKeys))
	}
}

func TestWikiSourceRefSameVersionRemovalInvalidatesPendingEventAndCursor(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-f1-same-version", "user-f1-same-version", "kb-f1-same-version", "epoch-f1-same-version", 1, 1)
	require.NoError(t, db.Create(scope).Error)

	page := &types.WikiPage{
		ID:              "page-f1-same-version",
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-same-version",
		Title:           "F1 same version",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		UpdatedAt:       now,
	}
	require.NoError(t, wikiRepo.Create(ctx, page))
	require.NoError(t, wikiRepo.Create(ctx, &types.WikiPage{
		ID:              "page-f1-same-version-other",
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-same-version-other",
		Title:           "F1 same version other",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		UpdatedAt:       now,
	}))
	event := &types.CitationProfileEvent{
		ID:                 "event-f1-same-version",
		TenantID:           scope.TenantID,
		SubjectID:          scope.SubjectID,
		KnowledgeBaseID:    scope.KnowledgeBaseID,
		SubjectEpoch:       scope.SubjectEpoch,
		ScopeID:            scope.ID,
		MessageID:          "message-f1-same-version",
		SourceKnowledgeID:  "knowledge-f1-same-version",
		SourceRefsSnapshot: json.RawMessage(`{}`),
		KnowledgeSnapshot:  json.RawMessage(`{}`),
		KnowledgeBaseProof: json.RawMessage(`{}`),
		ProducerEventKey:   "producer-f1-same-version",
		Status:             types.CitationProfileEventStatusPendingResolution,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	require.NoError(t, db.Create(event).Error)
	run, err := repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, run.Status)

	page.SourceRefs = types.StringArray{"knowledge-f1-same-version|Added"}
	require.NoError(t, wikiRepo.UpdateMeta(ctx, page))
	require.Equal(t, 1, page.Version, "UpdateMeta must exercise the same-version index replacement path")
	var afterAdd types.CitationProfileScope
	require.NoError(t, db.First(&afterAdd, "id = ?", scope.ID).Error)
	firstPage, err := repo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, firstPage.NextCursor)
	decoded, err := base64.RawURLEncoding.DecodeString(*firstPage.NextCursor)
	require.NoError(t, err)
	var cursorAfterAdd types.CitationProfileCursor
	require.NoError(t, json.Unmarshal(decoded, &cursorAfterAdd))

	page.SourceRefs = nil
	require.NoError(t, wikiRepo.UpdateMeta(ctx, page))
	require.Equal(t, 1, page.Version)
	var afterRemove types.CitationProfileScope
	require.NoError(t, db.First(&afterRemove, "id = ?", scope.ID).Error)
	require.Greater(t, afterRemove.ProfileReadVersion, afterAdd.ProfileReadVersion)
	require.Greater(t, afterRemove.MappingRevision, afterAdd.MappingRevision)
	_, err = repo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, &cursorAfterAdd, 1)
	require.ErrorIs(t, err, types.ErrCitationProfileChanged)
}

func TestWikiSourceUniverseRequeueRevivesTerminalOutboxWithFreshRetryBudget(t *testing.T) {
	for _, terminalStatus := range []string{
		types.CitationProfileOutboxStatusDeadletter,
		types.CitationProfileOutboxStatusDelivered,
	} {
		t.Run(terminalStatus, func(t *testing.T) {
			db := newCitationProfileRepositoryTestDB(t)
			repo := &citationProfileRepository{db: db}
			wikiRepo := newCitationProfileWikiPageRepository(db)
			ctx := context.Background()
			now := time.Now().UTC()
			old := now.Add(-48 * time.Hour)
			scope := citationProfileTestScope("scope-f1-outbox-"+terminalStatus, "user-f1-outbox-"+terminalStatus, "kb-f1-outbox-"+terminalStatus, "epoch-f1-outbox-"+terminalStatus, 1, 0)
			require.NoError(t, db.Create(scope).Error)
			event := &types.CitationProfileEvent{
				ID:                 "event-f1-outbox-" + terminalStatus,
				TenantID:           scope.TenantID,
				SubjectID:          scope.SubjectID,
				KnowledgeBaseID:    scope.KnowledgeBaseID,
				SubjectEpoch:       scope.SubjectEpoch,
				ScopeID:            scope.ID,
				MessageID:          "message-f1-outbox-" + terminalStatus,
				SourceKnowledgeID:  "knowledge-f1-outbox",
				SourceRefsSnapshot: json.RawMessage(`{}`),
				KnowledgeSnapshot:  json.RawMessage(`{}`),
				KnowledgeBaseProof: json.RawMessage(`{}`),
				ProducerEventKey:   "producer-f1-outbox-" + terminalStatus,
				Status:             types.CitationProfileEventStatusResolvedEmpty,
				CreatedAt:          old,
				UpdatedAt:          old,
			}
			require.NoError(t, db.Create(event).Error)
			terminalAt := old.Add(time.Hour)
			outbox := &types.CitationProfileEventOutbox{
				ID:               "outbox-f1-" + terminalStatus,
				TenantID:         scope.TenantID,
				SubjectID:        scope.SubjectID,
				KnowledgeBaseID:  scope.KnowledgeBaseID,
				SubjectEpoch:     scope.SubjectEpoch,
				ScopeID:          scope.ID,
				EventID:          event.ID,
				Status:           terminalStatus,
				AttemptCount:     8,
				NextAttemptAt:    old,
				LastErrorCode:    "old-error",
				LastErrorMessage: "old failure",
				CreatedAt:        old,
				UpdatedAt:        terminalAt,
			}
			if terminalStatus == types.CitationProfileOutboxStatusDeadletter {
				outbox.DeadletterAt = &terminalAt
			} else {
				outbox.DeliveredAt = &terminalAt
			}
			require.NoError(t, db.Create(outbox).Error)

			requeueAt := now.Add(time.Minute)
			requeueStartedAt, err := citationProfileDatabaseNow(db)
			require.NoError(t, err)
			require.NoError(t, wikiRepo.Create(ctx, &types.WikiPage{
				ID:              "page-f1-outbox-" + terminalStatus,
				TenantID:        scope.TenantID,
				KnowledgeBaseID: scope.KnowledgeBaseID,
				Slug:            "doc/f1-outbox-" + terminalStatus,
				Title:           "F1 outbox " + terminalStatus,
				PageType:        types.WikiPageTypeConcept,
				Status:          types.WikiPageStatusPublished,
				Version:         1,
				SourceRefs:      types.StringArray{"knowledge-f1-outbox|Requeue"},
				UpdatedAt:       requeueAt,
			}))
			requeueFinishedAt, err := citationProfileDatabaseNow(db)
			require.NoError(t, err)

			var stored types.CitationProfileEventOutbox
			require.NoError(t, db.First(&stored, "id = ?", outbox.ID).Error)
			require.Equal(t, types.CitationProfileOutboxStatusPending, stored.Status)
			require.Zero(t, stored.AttemptCount)
			require.Nil(t, stored.LockedAt)
			require.Empty(t, stored.LockedBy)
			require.Nil(t, stored.DeliveredAt)
			require.Nil(t, stored.DeadletterAt)
			require.Empty(t, stored.LastErrorCode)
			require.Empty(t, stored.LastErrorMessage)
			require.True(t, stored.CreatedAt.After(terminalAt), "requeue must replace the terminal row's exhausted max-age budget")
			require.False(t, stored.CreatedAt.Before(requeueStartedAt.Add(-time.Second)), "requeue time must come from the database clock")
			require.False(t, stored.CreatedAt.After(requeueFinishedAt.Add(time.Second)), "requeue time must come from the database clock")

			claims, err := repo.ClaimCitationProfileEventOutbox(ctx, "f1-requeue-worker", 1, requeueAt, time.Minute)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			require.Equal(t, outbox.ID, claims[0].ID)
		})
	}
}

type citationProfileSQLCapture struct {
	lines []string
}

func (c *citationProfileSQLCapture) Printf(format string, args ...interface{}) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func TestCitationProfileMultiScopeInvalidationLocksScopesInDatabaseOrder(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	now := time.Now().UTC()
	scopeA := citationProfileTestScope("scope-f6-a", "user-f6-a", "kb-f6", "epoch-f6-a", 1, 0)
	scopeZ := citationProfileTestScope("scope-f6-z", "user-f6-z", "kb-f6", "epoch-f6-z", 1, 0)
	require.NoError(t, db.Create(&[]types.CitationProfileScope{*scopeZ, *scopeA}).Error)
	for _, event := range []types.CitationProfileEvent{
		{ID: "event-f6-z", TenantID: 7, SubjectID: scopeZ.SubjectID, KnowledgeBaseID: scopeZ.KnowledgeBaseID, SubjectEpoch: scopeZ.SubjectEpoch, ScopeID: scopeZ.ID, MessageID: "message-f6-z", SourceKnowledgeID: "source-f6-z", ProducerEventKey: "producer-f6-z", SourceRefsSnapshot: json.RawMessage("{}"), KnowledgeSnapshot: json.RawMessage("{}"), KnowledgeBaseProof: json.RawMessage("{}"), Status: types.CitationProfileEventStatusResolvedEmpty, CreatedAt: now, UpdatedAt: now},
		{ID: "event-f6-a", TenantID: 7, SubjectID: scopeA.SubjectID, KnowledgeBaseID: scopeA.KnowledgeBaseID, SubjectEpoch: scopeA.SubjectEpoch, ScopeID: scopeA.ID, MessageID: "message-f6-a", SourceKnowledgeID: "source-f6-a", ProducerEventKey: "producer-f6-a", SourceRefsSnapshot: json.RawMessage("{}"), KnowledgeSnapshot: json.RawMessage("{}"), KnowledgeBaseProof: json.RawMessage("{}"), Status: types.CitationProfileEventStatusResolvedEmpty, CreatedAt: now, UpdatedAt: now},
	} {
		require.NoError(t, db.Create(&event).Error)
	}
	capture := &citationProfileSQLCapture{}
	observedDB := db.Session(&gorm.Session{Logger: gormlogger.New(capture, gormlogger.Config{LogLevel: gormlogger.Info})})
	require.NoError(t, markCitationProfileMappingsDirtyForPage(observedDB, &types.WikiPage{
		ID:              "page-f6",
		TenantID:        7,
		KnowledgeBaseID: "kb-f6",
		SourceRefs:      types.StringArray{"source-f6-z", "source-f6-a"},
		UpdatedAt:       now,
	}, now))
	var scopeLockSQL []string
	for _, line := range capture.lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "citation_profile_scopes") && strings.Contains(lower, "select") {
			scopeLockSQL = append(scopeLockSQL, line)
		}
	}
	require.NotEmpty(t, scopeLockSQL)
	require.Contains(t, strings.ToLower(strings.Join(scopeLockSQL, "\n")), "order by id asc")
}

func TestCitationProfileRepositoryExportAndDeleteStaySubjectScoped(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	scopeOne := citationProfileTestScope("scope-u1", "user-1", "kb-a", "epoch-u1", 5, 0)
	scopeTwo := citationProfileTestScope("scope-u2", "user-2", "kb-a", "epoch-u2", 5, 0)
	require.NoError(t, db.Create(scopeOne).Error)
	require.NoError(t, db.Create(scopeTwo).Error)
	require.NoError(t, db.Create(&types.CitationProfileEvent{
		ID:                   "event-u1",
		TenantID:             7,
		SubjectID:            "user-1",
		KnowledgeBaseID:      "kb-a",
		SubjectEpoch:         "epoch-u1",
		ScopeID:              scopeOne.ID,
		MessageID:            "message-u1",
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    "knowledge-u1",
		SourceRefsSnapshot:   json.RawMessage("{}"),
		KnowledgeSnapshot:    json.RawMessage("{}"),
		KnowledgeBaseProof:   json.RawMessage("{}"),
		ProducerEventKey:     "event-u1-key",
		Status:               types.CitationProfileEventStatusResolved,
		ActiveRunID:          "run-u1",
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error)
	require.NoError(t, db.Create(&types.EvidenceNodeLink{
		ID:                "link-u1",
		TenantID:          7,
		SubjectID:         "user-1",
		KnowledgeBaseID:   "kb-a",
		SubjectEpoch:      "epoch-u1",
		ScopeID:           scopeOne.ID,
		EventID:           "event-u1",
		ResolutionRunID:   "run-u1",
		SourceKnowledgeID: "knowledge-u1",
		PageUUID:          "page-u1",
		PageVersion:       1,
		NormalizedRef:     "knowledge-u1",
		RelationState:     types.EvidenceRelationCurrent,
		RelationSource:    "source_ref_index",
		MappingRevision:   5,
		CreatedAt:         now,
	}).Error)

	op, err := repo.CreateExportOperation(ctx, 7, "user-1", "kb-a", 5, "33333333-3333-4333-8333-333333333333", types.CitationProfileExportFormatJSON)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusReady, op.Status)
	var payload struct {
		Events []map[string]interface{} `json:"events"`
		Links  []map[string]interface{} `json:"links"`
	}
	require.NoError(t, json.Unmarshal(op.ResultSummary, &payload))
	require.Len(t, payload.Events, 1)
	require.Len(t, payload.Links, 1)
	require.NotContains(t, payload.Events[0], "subject_id")

	_, err = repo.GetExportOperation(ctx, 7, "user-2", "kb-a", op.ID)
	require.ErrorIs(t, err, types.ErrCitationProfileNotFound)

	expected := uint64(5)
	deleteOp, err := repo.RequestCurrentACLDelete(ctx, 7, "user-1", "kb-a", &expected, "44444444-4444-4444-8444-444444444444")
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, deleteOp.Status)

	var deletedOne, untouchedTwo types.CitationProfileScope
	require.NoError(t, db.First(&deletedOne, "id = ?", scopeOne.ID).Error)
	require.NoError(t, db.First(&untouchedTwo, "id = ?", scopeTwo.ID).Error)
	require.NotNil(t, deletedOne.FencedAt)
	require.NotNil(t, deletedOne.DeletedAt)
	require.False(t, deletedOne.Enabled)
	require.Equal(t, deleteOp.ID, deletedOne.DeleteRequestID)
	require.Nil(t, untouchedTwo.FencedAt)
	require.Nil(t, untouchedTwo.DeletedAt)
	require.True(t, untouchedTwo.Enabled)
}

func TestCitationProfileRepositoryReadsNodesGraphAndEvidence(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	now := time.Date(2026, 9, 2, 12, 30, 0, 0, time.UTC)
	scope := citationProfileTestScope("scope-read", "user-7", "kb-a", "epoch-read", 20, 0)
	scope.MappingRevision = 20
	scope.SourceUniverseWatermark = "wm-current"
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(citationProfileTestScope("scope-other", "user-8", "kb-a", "epoch-other", 20, 0)).Error)
	require.NoError(t, db.Create(&[]types.WikiPage{
		{ID: "page-a", TenantID: 7, KnowledgeBaseID: "kb-a", Slug: "doc/a", Title: "Page A", PageType: types.WikiPageTypeConcept, Status: types.WikiPageStatusPublished, Version: 1, OutLinks: types.StringArray{"doc/b"}, UpdatedAt: now},
		{ID: "page-b", TenantID: 7, KnowledgeBaseID: "kb-a", Slug: "doc/b", Title: "Page B", PageType: types.WikiPageTypeEntity, Status: types.WikiPageStatusPublished, Version: 2, UpdatedAt: now.Add(time.Minute)},
		{ID: "page-c", TenantID: 7, KnowledgeBaseID: "kb-a", Slug: "doc/c", Title: "Page C", PageType: types.WikiPageTypeSummary, Status: types.WikiPageStatusPublished, Version: 3, UpdatedAt: now.Add(2 * time.Minute)},
	}).Error)
	knowledgeSnapshot, err := citationJSON(map[string]interface{}{"updated_at": citationTime(now.Add(-time.Hour))})
	require.NoError(t, err)
	knowledgeBaseProof, err := citationJSON(map[string]interface{}{"knowledge_base_id": "kb-a"})
	require.NoError(t, err)
	resolvedAt := now.Add(time.Second)
	require.NoError(t, db.Create(&types.CitationProfileEvent{
		ID:                   "event-read",
		TenantID:             7,
		SubjectID:            "user-7",
		KnowledgeBaseID:      "kb-a",
		SubjectEpoch:         "epoch-read",
		ScopeID:              "scope-read",
		MessageID:            "message-read",
		MessageVersion:       "message-v1",
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    "knowledge-a",
		SourceResultID:       "result-a",
		SourceRefsSnapshot:   json.RawMessage(`{}`),
		ProducerEventKey:     "event-read-key",
		Status:               types.CitationProfileEventStatusResolved,
		ActiveRunID:          "run-read",
		KnowledgeSnapshot:    knowledgeSnapshot,
		KnowledgeBaseProof:   knowledgeBaseProof,
		MessageCompletedAt:   &now,
		ResolvedAt:           &resolvedAt,
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error)
	require.NoError(t, db.Create(&types.EvidenceResolutionRun{
		ID:                   "run-read",
		TenantID:             7,
		SubjectID:            "user-7",
		KnowledgeBaseID:      "kb-a",
		SubjectEpoch:         "epoch-read",
		ScopeID:              "scope-read",
		EventID:              "event-read",
		Status:               types.CitationProfileEventStatusResolved,
		RunMappingRevision:   20,
		RunUniverseWatermark: "wm-current",
		StartedAt:            now,
		ResolvedAt:           &resolvedAt,
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error)
	require.NoError(t, db.Create(&[]types.EvidenceNodeLink{
		{ID: "link-read", TenantID: 7, SubjectID: "user-7", KnowledgeBaseID: "kb-a", SubjectEpoch: "epoch-read", ScopeID: "scope-read", EventID: "event-read", ResolutionRunID: "run-read", SourceKnowledgeID: "knowledge-a", PageUUID: "page-b", PageVersion: 2, NormalizedRef: "knowledge-a", RelationState: types.EvidenceRelationCurrent, RelationSource: "source_ref_index", MappingRevision: 20, UniverseWatermark: "wm-current", CreatedAt: resolvedAt},
		{ID: "link-other", TenantID: 7, SubjectID: "user-8", KnowledgeBaseID: "kb-a", SubjectEpoch: "epoch-other", ScopeID: "scope-other", EventID: "event-other", ResolutionRunID: "run-other", SourceKnowledgeID: "knowledge-b", PageUUID: "page-b", PageVersion: 2, NormalizedRef: "knowledge-b", RelationState: types.EvidenceRelationCurrent, RelationSource: "source_ref_index", MappingRevision: 20, UniverseWatermark: "wm-current", CreatedAt: resolvedAt},
	}).Error)
	require.NoError(t, db.Create(citationProfileSourceRefRow("source-ref-read", "kb-a", "knowledge-a", "page-b", 2, 20)).Error)

	first, err := repo.ListNodes(ctx, 7, "user-7", "kb-a", nil, 1)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, "page-a", first.Items[0].PageUUID)
	require.Equal(t, types.EvidenceRelationUnknown, first.Items[0].Overlay)
	require.NotNil(t, first.NextCursor)
	require.False(t, first.CompleteList)

	var cursor types.CitationProfileCursor
	rawCursor, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(rawCursor, &cursor))
	require.NotEmpty(t, first.Snapshot.EventCutoff)
	require.NotEmpty(t, first.Snapshot.CorrectionCutoff)
	require.NotEmpty(t, first.Snapshot.ActiveRunPointerCutoff)
	require.Equal(t, first.Snapshot.EventCutoff, cursor.EventCutoff)
	require.Equal(t, first.Snapshot.CorrectionCutoff, cursor.CorrectionCutoff)
	require.Equal(t, first.Snapshot.ActiveRunPointerCutoff, cursor.ActiveRunPointerCutoff)
	second, err := repo.ListNodes(ctx, 7, "user-7", "kb-a", &cursor, 1)
	require.NoError(t, err)
	require.Len(t, second.Items, 1)
	require.Equal(t, "page-b", second.Items[0].PageUUID)
	require.Equal(t, types.EvidenceRelationCurrent, second.Items[0].Overlay)
	require.Equal(t, 1, second.Items[0].AuthorizedEvidenceCount)

	graph, err := repo.GetGraph(ctx, 7, "user-7", "kb-a")
	require.NoError(t, err)
	require.Len(t, graph.Nodes, 3)
	require.Len(t, graph.Edges, 1)
	require.Equal(t, "page-a", graph.Edges[0].SourcePageUUID)
	require.Equal(t, "page-b", graph.Edges[0].TargetPageUUID)
	require.Equal(t, 1, graph.Edges[0].EvidenceEventCount)
	require.False(t, graph.GraphTruncated)

	evidence, err := repo.ListNodeEvidence(ctx, 7, "user-7", "kb-a", "page-b", nil, 10)
	require.NoError(t, err)
	require.Equal(t, "page-b", evidence.Page.PageUUID)
	require.Len(t, evidence.Items, 1)
	require.Equal(t, types.CitationProfileEvidenceClaimCode, evidence.Items[0].ClaimCode)
	require.Equal(t, types.CitationProfileCorrectionStateNone, evidence.Items[0].CorrectionState)
	require.Equal(t, "kb-a", evidence.Items[0].AuthoritativeKnowledgeBaseID)
}
func TestWikiSourceRefIndexSyncTracksCurrentHistoricalAndDeletedRows(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	now := time.Now().UTC()
	page := &types.WikiPage{
		ID:              "page-a",
		TenantID:        7,
		KnowledgeBaseID: "kb-a",
		Slug:            "doc/page-a",
		Title:           "Page A",
		Version:         1,
		Status:          types.WikiPageStatusPublished,
		SourceRefs:      types.StringArray{"source-a|Doc A", "source-a|Duplicate", "source-b"},
		UpdatedAt:       now,
	}
	require.NoError(t, syncWikiSourceRefIndexForPage(db, page))

	var current []types.WikiSourceRefIndex
	require.NoError(t, db.Where("page_uuid = ? AND lifecycle_state = ?", page.ID, "current").Order("source_knowledge_id ASC").Find(&current).Error)
	require.Len(t, current, 2)
	require.Equal(t, "source-a", current[0].SourceKnowledgeID)
	require.Equal(t, "source-b", current[1].SourceKnowledgeID)

	page.Version = 2
	page.SourceRefs = types.StringArray{"source-b"}
	page.UpdatedAt = now.Add(time.Minute)
	require.NoError(t, syncWikiSourceRefIndexForPage(db, page))

	var historicalCount int64
	require.NoError(t, db.Model(&types.WikiSourceRefIndex{}).Where("page_uuid = ? AND lifecycle_state = ?", page.ID, "historical").Count(&historicalCount).Error)
	require.Equal(t, int64(2), historicalCount)
	current = nil
	require.NoError(t, db.Where("page_uuid = ? AND lifecycle_state = ?", page.ID, "current").Find(&current).Error)
	require.Len(t, current, 1)
	require.Equal(t, 2, current[0].PageVersion)
	require.Equal(t, "source-b", current[0].SourceKnowledgeID)

	require.NoError(t, markWikiSourceRefIndexPageDeleted(db, page))
	var deletedCount int64
	require.NoError(t, db.Model(&types.WikiSourceRefIndex{}).Where("page_uuid = ? AND lifecycle_state = ?", page.ID, "deleted").Count(&deletedCount).Error)
	require.Equal(t, int64(1), deletedCount)
}

func newCitationProfileRepositoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", urlSafeTestName(t.Name()))), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(
		&types.CitationProfileScope{},
		&types.CitationProfileEvent{},
		&types.CitationProfileEventOutbox{},
		&types.WikiSourceRefIndex{},
		&types.EvidenceResolutionRun{},
		&types.EvidenceNodeLink{},
		&types.CitationProfileCorrection{},
		&types.CitationProfileOperation{},
		&types.WikiPage{},
	))
	return db
}

func newCitationProfileWikiPageRepository(db *gorm.DB) interfaces.WikiPageRepository {
	return NewWikiPageRepository(db, &types.CitationProfileConfig{Enabled: true})
}

func citationProfileTestScope(id string, subjectID string, kbID string, epoch string, readVersion uint64, pendingEvents int) *types.CitationProfileScope {
	now := time.Now().UTC()
	checkedAt := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	nextCheckAt := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	return &types.CitationProfileScope{
		ID:                       id,
		TenantID:                 7,
		SubjectID:                subjectID,
		KnowledgeBaseID:          kbID,
		SubjectEpoch:             epoch,
		ProfileReadVersion:       readVersion,
		ProfilePolicyVersion:     types.CitationProfilePolicyVersion,
		RetentionPolicyVersion:   types.CitationProfileRetentionPolicyVersion,
		Enabled:                  true,
		ACLCheckState:            types.CitationProfileACLStateCurrent,
		ACLCheckedAt:             &checkedAt,
		NextACLCheckAt:           &nextCheckAt,
		ACLPrincipalType:         types.PrincipalWebUser,
		ACLPrincipalID:           subjectID,
		ACLAuthenticatedTenantID: 7,
		ACLAccessPath:            types.CitationProfileACLAccessPathOwner,
		ACLGeneration:            1,
		PendingEventCount:        pendingEvents,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
}

func citationProfileSourceRefRow(id string, kbID string, sourceKnowledgeID string, pageID string, version int, mappingRevision uint64) types.WikiSourceRefIndex {
	now := time.Now().UTC()
	return types.WikiSourceRefIndex{
		ID:                id,
		TenantID:          7,
		KnowledgeBaseID:   kbID,
		SourceKnowledgeID: sourceKnowledgeID,
		PageUUID:          pageID,
		PageVersion:       version,
		PageSlug:          "doc/" + pageID,
		PageTitle:         pageID,
		NormalizedRef:     sourceKnowledgeID,
		MappingRevision:   mappingRevision,
		LifecycleState:    "current",
		IndexWatermark:    fmt.Sprintf("wm-%s-%d", pageID, version),
		IndexedAt:         now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func urlSafeTestName(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			out = append(out, r)
			continue
		}
		out = append(out, '_')
	}
	return string(out)
}
