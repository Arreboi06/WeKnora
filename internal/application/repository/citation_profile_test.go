//go:build cgo

package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCitationProfileRepositorySetEnrollmentCreatesRandomEpochAndChecksReadVersion(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
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

func citationProfileTestScope(id string, subjectID string, kbID string, epoch string, readVersion uint64, pendingEvents int) *types.CitationProfileScope {
	now := time.Now().UTC()
	return &types.CitationProfileScope{
		ID:                     id,
		TenantID:               7,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           epoch,
		ProfileReadVersion:     readVersion,
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          "current",
		PendingEventCount:      pendingEvents,
		CreatedAt:              now,
		UpdatedAt:              now,
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
