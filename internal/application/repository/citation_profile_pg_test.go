//go:build t4pg

package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestCitationProfilePostgresExtendedProtocolStatus(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}

	// postgres.Open uses pgx extended protocol by default, matching the
	// production container. Do not set PreferSimpleProtocol here: untyped CASE
	// result parameters can pass simple-protocol tests yet fail in production.
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	repo := NewCitationProfileRepository(db)
	status, err := repo.GetScopeStatus(
		context.Background(), 76001, "extended-protocol-subject", uuid.NewString(),
	)
	require.NoError(t, err)
	require.Nil(t, status)
}

var (
	citationProfilePGSchemaOnce sync.Once
	citationProfilePGSchemaErr  error
)

type citationProfilePGSQLCapture struct {
	lines []string
}

func (c *citationProfilePGSQLCapture) Printf(format string, args ...interface{}) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func TestCitationProfilePostgresTerminalIdentityConstraints(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}

	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	var indexDefs []string
	require.NoError(t, db.Raw(`
SELECT indexdef
FROM pg_indexes
WHERE schemaname = current_schema()
  AND indexname IN (
    'uq_citation_profile_event_producer',
    'uq_citation_profile_event_key',
    'uq_citation_profile_outbox_event'
  )
ORDER BY indexname`).Pluck("indexdef", &indexDefs).Error)
	require.Len(t, indexDefs, 3)
	for _, definition := range indexDefs {
		require.NotContains(t, strings.ToUpper(definition), " WHERE ", "terminal identity index must cover terminal rows")
	}

	tenantID := uint64(987654)
	subjectID := "terminal-identity-subject-" + uuid.NewString()
	kbID := uuid.NewString()
	epoch := uuid.NewString()
	scopeID := uuid.NewString()
	messageID := uuid.NewString()
	sessionID := uuid.NewString()
	knowledgeID := uuid.NewString()
	firstEventID := uuid.NewString()
	now := time.Now().UTC()

	eventInsert := `INSERT INTO citation_profile_events
 (id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
  session_id, message_id, origin_reference_index, source_knowledge_id,
  source_result_id, producer_event_key, status, retracted_at, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 'resolved', ?, ?, ?)`
	retractedAt := now
	require.NoError(t, db.Exec(eventInsert,
		firstEventID, tenantID, subjectID, kbID, epoch, scopeID, sessionID, messageID,
		knowledgeID, "terminal-result", "terminal-key", retractedAt, now, now,
	).Error)

	err = db.Exec(eventInsert,
		uuid.NewString(), tenantID, subjectID, kbID, epoch, scopeID, sessionID, messageID,
		knowledgeID, "terminal-result", "terminal-key", retractedAt, now, now,
	).Error
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23505", pgErr.Code)

	deadletterInsert := `INSERT INTO citation_profile_event_outbox
 (id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
  event_id, status, deadletter_at, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, 'deadletter', ?, ?, ?)`
	firstOutboxID := uuid.NewString()
	require.NoError(t, db.Exec(deadletterInsert,
		firstOutboxID, tenantID, subjectID, kbID, epoch, scopeID, firstEventID, now, now, now,
	).Error)
	err = db.Exec(deadletterInsert,
		uuid.NewString(), tenantID, subjectID, kbID, epoch, scopeID, firstEventID, now, now, now,
	).Error
	pgErr = nil
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23505", pgErr.Code)

	// Keep this test isolated even when a developer points T4_PG_DSN at a
	// reusable local database instead of the disposable probe database.
	require.NoError(t, db.Exec("DELETE FROM citation_profile_event_outbox WHERE id = ?", firstOutboxID).Error)
	require.NoError(t, db.Exec("DELETE FROM citation_profile_events WHERE id = ?", firstEventID).Error)
}

func TestCitationProfileRepositoryPostgresVertical(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}

	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	require.NoError(t, ensureCitationProfilePGSchema(db))

	repo := NewCitationProfileRepository(db)
	tenantID := uint64(77)
	subjectID := "subject-pg"
	ctx := citationProfilePGOwnerContext(context.Background(), tenantID, subjectID)
	kbID := uuid.NewString()
	pageA := uuid.NewString()
	pageB := uuid.NewString()
	knowledgeLive := uuid.NewString()
	knowledgeRetracted := uuid.NewString()
	now := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)

	zero := uint64(0)
	scope, err := repo.SetEnrollment(ctx, tenantID, " "+subjectID+" ", kbID, true, &zero, uuid.NewString())
	require.NoError(t, err)
	require.True(t, scope.Enabled)
	require.NotEmpty(t, scope.SubjectEpoch)
	require.Equal(t, uint64(1), scope.ProfileReadVersion)
	citationProfilePGAuthorizeScope(t, db, scope)

	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageA, "doc/a", "Page A", types.StringArray{"doc/b"}, now))
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageB, "doc/b", "Page B", nil, now.Add(time.Minute)))
	require.NoError(t, db.Create(&[]types.WikiSourceRefIndex{
		citationProfilePGSourceRef(kbID, knowledgeLive, pageB, 1, 10, "wm-pg", now),
		citationProfilePGSourceRef(kbID, knowledgeRetracted, pageB, 1, 10, "wm-pg", now),
	}).Error)

	liveEvent := citationProfilePGEvent(tenantID, subjectID, kbID, scope, knowledgeLive, "message-live", 0, now)
	retractedEvent := citationProfilePGEvent(tenantID, subjectID, kbID, scope, knowledgeRetracted, "message-retracted", 1, now.Add(time.Second))
	require.NoError(t, db.Create(&[]types.CitationProfileEvent{liveEvent, retractedEvent}).Error)

	liveRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, liveEvent.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, liveRun.Status)
	require.Equal(t, 1, liveRun.OutputCount)
	retractedRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, retractedEvent.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, retractedRun.Status)
	require.Equal(t, 1, retractedRun.OutputCount)

	status, err := repo.GetScopeStatus(ctx, tenantID, subjectID, kbID)
	require.NoError(t, err)
	_, err = repo.ApplyCorrection(ctx, tenantID, subjectID, kbID, status.ProfileReadVersion, uuid.NewString(), types.CitationCorrectionRetractEvent, retractedEvent.ID, pageB, "wrong_page")
	require.NoError(t, err)

	nodes, err := repo.ListNodes(ctx, tenantID, subjectID, kbID, nil, 10)
	require.NoError(t, err)
	nodeB := citationProfilePGFindNode(t, nodes.Items, pageB)
	require.Equal(t, 1, nodeB.AuthorizedEvidenceCount)
	require.Equal(t, 1, nodeB.CurrentLinkCount)
	require.Equal(t, 0, nodeB.DisputedLinkCount)
	require.Equal(t, types.EvidenceRelationCurrent, nodeB.Overlay)

	graph, err := repo.GetGraph(ctx, tenantID, subjectID, kbID)
	require.NoError(t, err)
	require.Len(t, graph.Edges, 1)
	require.Equal(t, pageA, graph.Edges[0].SourcePageUUID)
	require.Equal(t, pageB, graph.Edges[0].TargetPageUUID)
	require.Equal(t, 1, graph.Edges[0].EvidenceEventCount)

	evidence, err := repo.ListNodeEvidence(ctx, tenantID, subjectID, kbID, pageB, nil, 10)
	require.NoError(t, err)
	require.Len(t, evidence.Items, 1)
	require.Equal(t, liveEvent.ID, evidence.Items[0].EventID)

	status, err = repo.GetScopeStatus(ctx, tenantID, subjectID, kbID)
	require.NoError(t, err)
	exportOp, err := repo.CreateExportOperation(ctx, tenantID, subjectID, kbID, status.ProfileReadVersion, uuid.NewString(), types.CitationProfileExportFormatJSON)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusReady, exportOp.Status)
	require.NotContains(t, string(exportOp.ResultSummary), "subject_id")
	var payload struct {
		Events []map[string]interface{} `json:"events"`
		Links  []map[string]interface{} `json:"links"`
	}
	require.NoError(t, json.Unmarshal(exportOp.ResultSummary, &payload))
	require.Len(t, payload.Events, 1)
	require.Len(t, payload.Links, 1)
	require.Equal(t, liveEvent.ID, payload.Events[0]["event_id"])

	status, err = repo.GetScopeStatus(ctx, tenantID, subjectID, kbID)
	require.NoError(t, err)
	deleteOp, err := repo.RequestCurrentACLDelete(ctx, tenantID, subjectID, kbID, &status.ProfileReadVersion, uuid.NewString())
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, deleteOp.Status)
	_, err = repo.ListNodes(ctx, tenantID, subjectID, kbID, nil, 10)
	require.True(t, errors.Is(err, types.ErrCitationProfileDeleted), "got %v", err)
	_, err = repo.GetExportOperation(ctx, tenantID, subjectID, kbID, exportOp.ID)
	require.True(t, errors.Is(err, types.ErrCitationProfileDeleted), "got %v", err)

	blindOp, err := repo.RequestBlindDelete(ctx, tenantID, subjectID, uuid.NewString())
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, blindOp.Status)

	requireCitationProfilePGFactA(t, db, repo, tenantID, subjectID, now.Add(10*time.Minute))
	requireCitationProfilePGFactBExactRetryAndFailure(t, db, repo, tenantID, subjectID, now.Add(20*time.Minute))
}

// citationProfilePGOwnerContext mirrors the server-derived identity and ACL
// proof installed by the authenticated KB owner middleware. Keeping the proof
// in context (rather than the enrollment DTO) exercises the fail-closed
// production contract while preserving the repository vertical's owner scope.
func citationProfilePGOwnerContext(ctx context.Context, tenantID uint64, subjectID string) context.Context {
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	ctx = context.WithValue(ctx, types.UserIDContextKey, subjectID)
	ctx = types.WithAuthenticatedTenantID(ctx, tenantID)
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalWebUser, ID: subjectID})
	return types.WithCitationProfileACLBinding(ctx, types.CitationProfileACLBinding{
		PrincipalType:         types.PrincipalWebUser,
		PrincipalID:           subjectID,
		AuthenticatedTenantID: tenantID,
		AccessPath:            types.CitationProfileACLAccessPathOwner,
	})
}

func TestCitationProfilePostgresWikiSourceUniverseInvalidatesResolvedEmpty(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	tenantID := uint64(77001)
	subjectID := "f1-source-universe-" + uuid.NewString()
	kbID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           uuid.NewString(),
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          "current",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)
	citationRepo := NewCitationProfileRepository(db)
	ctx := context.Background()

	newResolvedEmpty := func(id, sourceID string) *types.CitationProfileEvent {
		event := citationProfilePGEvent(tenantID, subjectID, kbID, scope, sourceID, uuid.NewString(), 0, now)
		event.ID = id
		require.NoError(t, db.Create(&event).Error)
		run, resolveErr := citationRepo.ResolveEvidenceEvent(ctx, tenantID, subjectID, event.ID)
		require.NoError(t, resolveErr)
		require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, run.Status)
		return &event
	}
	addedEvent := newResolvedEmpty(uuid.NewString(), "knowledge-f1-pg-added")
	newPageEvent := newResolvedEmpty(uuid.NewString(), "knowledge-f1-pg-page")

	existingPage := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/existing",
		Title:           "F1 existing",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{"knowledge-f1-old"},
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	otherPage := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/other",
		Title:           "F1 other",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	persistPageAndInvalidate := func(page *types.WikiPage) {
		require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, page.ID, page.Slug, page.Title, nil, page.UpdatedAt))
		refsJSON, marshalErr := json.Marshal(page.SourceRefs)
		require.NoError(t, marshalErr)
		require.NoError(t, db.Exec("UPDATE wiki_pages SET source_refs = ?::json, updated_at = ? WHERE id = ?", string(refsJSON), page.UpdatedAt, page.ID).Error)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			if err := syncWikiSourceRefIndexForPage(tx, page); err != nil {
				return err
			}
			return markCitationProfileMappingsDirtyForPage(tx, page, page.UpdatedAt)
		}))
	}
	persistPageAndInvalidate(existingPage)
	persistPageAndInvalidate(otherPage)
	first, err := citationRepo.ListNodes(ctx, tenantID, subjectID, kbID, nil, 1)
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	oldCursorBytes, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	require.NoError(t, err)
	var oldCursor types.CitationProfileCursor
	require.NoError(t, json.Unmarshal(oldCursorBytes, &oldCursor))

	newPage := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/new",
		Title:           "F1 new",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{"knowledge-f1-pg-page"},
		CreatedAt:       now.Add(time.Minute),
		UpdatedAt:       now.Add(time.Minute),
	}
	persistPageAndInvalidate(newPage)
	var storedNewPageEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedNewPageEvent, "id = ?", newPageEvent.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedNewPageEvent.Status)
	require.Empty(t, storedNewPageEvent.ActiveRunID)

	existingPage.SourceRefs = types.StringArray{"knowledge-f1-old", "knowledge-f1-pg-added"}
	existingPage.UpdatedAt = now.Add(2 * time.Minute)
	refsJSON, err := json.Marshal(existingPage.SourceRefs)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE wiki_pages SET source_refs = ?::json, updated_at = ? WHERE id = ?", string(refsJSON), existingPage.UpdatedAt, existingPage.ID).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := syncWikiSourceRefIndexForPage(tx, existingPage); err != nil {
			return err
		}
		return markCitationProfileMappingsDirtyForPage(tx, existingPage, existingPage.UpdatedAt)
	}))
	var storedAddedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedAddedEvent, "id = ?", addedEvent.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedAddedEvent.Status)
	require.Empty(t, storedAddedEvent.ActiveRunID)

	var refreshedScope types.CitationProfileScope
	require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
	require.Greater(t, refreshedScope.ProfileReadVersion, scope.ProfileReadVersion)
	require.Greater(t, refreshedScope.MappingRevision, scope.MappingRevision)
	var currentLinks int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("event_id IN ? AND relation_state = ?", []string{addedEvent.ID, newPageEvent.ID}, types.EvidenceRelationCurrent).
		Count(&currentLinks).Error)
	require.Zero(t, currentLinks)
	_, err = citationRepo.ListNodes(ctx, tenantID, subjectID, kbID, &oldCursor, 1)
	require.ErrorIs(t, err, types.ErrCitationProfileChanged)
}

func TestCitationProfilePostgresWikiSourceUniverseRaceCannotPublishStaleRun(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	tenantID := uint64(77002)
	subjectID := "f1-source-race-" + uuid.NewString()
	kbID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           uuid.NewString(),
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          "current",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)
	repo := NewCitationProfileRepository(db)
	ctx := context.Background()
	racePage := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/race",
		Title:           "F1 race",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		SourceRefs:      types.StringArray{"knowledge-f1-race"},
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, racePage.ID, racePage.Slug, racePage.Title, nil, now))
	refsJSON, err := json.Marshal(racePage.SourceRefs)
	require.NoError(t, err)
	require.NoError(t, db.Exec("UPDATE wiki_pages SET source_refs = ?::json, updated_at = ? WHERE id = ?", string(refsJSON), now, racePage.ID).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := syncWikiSourceRefIndexForPage(tx, racePage); err != nil {
			return err
		}
		return markCitationProfileMappingsDirtyForPage(tx, racePage, now)
	}))
	raceEvent := citationProfilePGEvent(tenantID, subjectID, kbID, scope, "knowledge-f1-race", uuid.NewString(), 0, now)
	require.NoError(t, db.Create(&raceEvent).Error)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var resolveErr, mutateErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, resolveErr = repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, raceEvent.ID)
	}()
	go func() {
		defer wg.Done()
		<-start
		racePage.Version = 2
		racePage.UpdatedAt = now.Add(time.Minute)
		refsJSON, marshalErr := json.Marshal(racePage.SourceRefs)
		if marshalErr != nil {
			mutateErr = marshalErr
			return
		}
		mutateErr = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("UPDATE wiki_pages SET version = ?, source_refs = ?::json, updated_at = ? WHERE id = ?", racePage.Version, string(refsJSON), racePage.UpdatedAt, racePage.ID).Error; err != nil {
				return err
			}
			if err := syncWikiSourceRefIndexForPage(tx, racePage); err != nil {
				return err
			}
			return markCitationProfileMappingsDirtyForPage(tx, racePage, racePage.UpdatedAt)
		})
	}()
	close(start)
	wg.Wait()
	require.NoError(t, resolveErr)
	require.NoError(t, mutateErr)

	var currentRef types.WikiSourceRefIndex
	require.NoError(t, db.Where(
		"tenant_id = ? AND knowledge_base_id = ? AND source_knowledge_id = ? AND page_uuid = ? AND lifecycle_state = ?",
		tenantID, kbID, "knowledge-f1-race", racePage.ID, "current",
	).First(&currentRef).Error)
	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", raceEvent.ID).Error)
	var refreshedScope types.CitationProfileScope
	require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
	if storedEvent.Status == types.CitationProfileEventStatusPendingResolution {
		require.Empty(t, storedEvent.ActiveRunID)
	} else {
		require.Equal(t, types.CitationProfileEventStatusResolved, storedEvent.Status)
		require.NotEmpty(t, storedEvent.ActiveRunID)
		var run types.EvidenceResolutionRun
		require.NoError(t, db.First(&run, "id = ?", storedEvent.ActiveRunID).Error)
		require.Equal(t, currentRef.MappingRevision, run.RunMappingRevision)
		require.Equal(t, refreshedScope.MappingRevision, run.RunMappingRevision)
		var link types.EvidenceNodeLink
		require.NoError(t, db.Where("event_id = ? AND relation_state = ?", raceEvent.ID, types.EvidenceRelationCurrent).First(&link).Error)
		require.Equal(t, currentRef.PageVersion, link.PageVersion)
	}
}

func TestCitationProfilePostgresConcurrentUpdateMetaCapturesCommittedSourceGeneration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	openDB := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}

	db := openDB()
	require.NoError(t, ensureCitationProfilePGSchema(db))
	tenantID := uint64(77004)
	subjectID := "f1-update-meta-race-" + uuid.NewString()
	kbID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           uuid.NewString(),
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          "current",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)

	sourceA := uuid.NewString()
	sourceB := uuid.NewString()
	sourceC := uuid.NewString()
	page := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/update-meta-race",
		Title:           "F1 update meta race",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		Aliases:         types.StringArray{},
		CategoryPath:    types.StringArray{},
		SourceRefs:      types.StringArray{sourceA + "|A"},
		ChunkRefs:       types.StringArray{},
		InLinks:         types.StringArray{},
		OutLinks:        types.StringArray{},
		PageMetadata:    types.JSON(`{}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, newCitationProfileWikiPageRepository(db).Create(context.Background(), page))
	eventB := citationProfilePGEvent(tenantID, subjectID, kbID, scope, sourceB, uuid.NewString(), 0, now)
	require.NoError(t, db.Create(&eventB).Error)
	var before types.CitationProfileScope
	require.NoError(t, db.First(&before, "id = ?", scope.ID).Error)

	dbB := openDB()
	dbC := openDB()
	bHasPageLock := make(chan struct{})
	releaseB := make(chan struct{})
	cReachedPageUpdate := make(chan struct{})
	var bOnce, cOnce sync.Once
	require.NoError(t, dbB.Callback().Update().After("gorm:update").Register(
		"p36:f1:update-meta-b-holds-page-lock",
		func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == "wiki_pages" {
				bOnce.Do(func() {
					close(bHasPageLock)
					<-releaseB
				})
			}
		},
	))
	require.NoError(t, dbC.Callback().Update().Before("gorm:update").Register(
		"p36:f1:update-meta-c-reaches-page-update",
		func(tx *gorm.DB) {
			if tx.Statement != nil && tx.Statement.Table == "wiki_pages" {
				cOnce.Do(func() { close(cReachedPageUpdate) })
			}
		},
	))

	pageB := *page
	pageB.SourceRefs = types.StringArray{sourceB + "|B"}
	pageC := *page
	pageC.SourceRefs = types.StringArray{sourceC + "|C"}
	bDone := make(chan error, 1)
	cDone := make(chan error, 1)
	go func() { bDone <- newCitationProfileWikiPageRepository(dbB).UpdateMeta(context.Background(), &pageB) }()
	select {
	case <-bHasPageLock:
	case <-time.After(10 * time.Second):
		t.Fatal("writer B did not acquire the wiki page row lock")
	}
	go func() { cDone <- newCitationProfileWikiPageRepository(dbC).UpdateMeta(context.Background(), &pageC) }()
	select {
	case <-cReachedPageUpdate:
	case <-time.After(10 * time.Second):
		t.Fatal("writer C did not reach the blocked wiki page update")
	}
	select {
	case err := <-cDone:
		t.Fatalf("writer C completed while writer B still held the page lock: %v", err)
	default:
	}
	close(releaseB)
	select {
	case err := <-bDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("writer B did not complete after release")
	}
	select {
	case err := <-cDone:
		require.ErrorIs(t, err, ErrWikiPageConflict,
			"the second same-generation writer must reload instead of overwriting writer B")
	case <-time.After(10 * time.Second):
		t.Fatal("writer C did not complete after writer B committed")
	}
	var pageCAfterConflict types.WikiPage
	require.NoError(t, dbC.First(&pageCAfterConflict, "id = ?", page.ID).Error)
	pageCAfterConflict.SourceRefs = types.StringArray{sourceC + "|C"}
	require.NoError(t, newCitationProfileWikiPageRepository(dbC).UpdateMeta(context.Background(), &pageCAfterConflict))

	var after types.CitationProfileScope
	require.NoError(t, db.First(&after, "id = ?", scope.ID).Error)
	require.Equal(t, before.ProfileReadVersion+2, after.ProfileReadVersion,
		"writer C must capture writer B's committed source generation and fence the B event again")
	require.Equal(t, before.MappingRevision+2, after.MappingRevision)
	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", eventB.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedEvent.Status)
	require.Empty(t, storedEvent.ActiveRunID)
	var currentLinks int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("event_id = ? AND relation_state = ?", eventB.ID, types.EvidenceRelationCurrent).
		Count(&currentLinks).Error)
	require.Zero(t, currentLinks)
	var currentRefs []types.WikiSourceRefIndex
	require.NoError(t, db.Where("tenant_id = ? AND knowledge_base_id = ? AND page_uuid = ? AND lifecycle_state = ?",
		tenantID, kbID, page.ID, "current").Find(&currentRefs).Error)
	require.Len(t, currentRefs, 1)
	require.Equal(t, sourceC, currentRefs[0].SourceKnowledgeID)
}

func TestCitationProfilePostgresWikiSourceUniverseExpansionRetiresPriorRun(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	tenantID := uint64(77005)
	subjectID := "f1-expanded-pg-" + uuid.NewString()
	kbID := uuid.NewString()
	sourceID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           uuid.NewString(),
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          "current",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)
	repo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()

	pageA := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/expanded-a-" + uuid.NewString(),
		Title:           "F1 expanded A",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		Aliases:         types.StringArray{},
		CategoryPath:    types.StringArray{},
		SourceRefs:      types.StringArray{sourceID + "|A"},
		ChunkRefs:       types.StringArray{},
		InLinks:         types.StringArray{},
		OutLinks:        types.StringArray{},
		PageMetadata:    types.JSON(`{}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, wikiRepo.Create(ctx, pageA))
	event := citationProfilePGEvent(tenantID, subjectID, kbID, scope, sourceID, uuid.NewString(), 0, now)
	require.NoError(t, db.Create(&event).Error)
	firstRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, firstRun.Status)
	require.Equal(t, 1, firstRun.OutputCount)

	pageB := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/expanded-b-" + uuid.NewString(),
		Title:           "F1 expanded B",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		Aliases:         types.StringArray{},
		CategoryPath:    types.StringArray{},
		SourceRefs:      types.StringArray{sourceID + "|B"},
		ChunkRefs:       types.StringArray{},
		InLinks:         types.StringArray{},
		OutLinks:        types.StringArray{},
		PageMetadata:    types.JSON(`{}`),
		CreatedAt:       now.Add(time.Minute),
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
	require.Zero(t, currentBeforeRerun, "all prior-run current links must retire before re-resolution")
	var firstRunHistorical int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("resolution_run_id = ? AND relation_state = ?", firstRun.ID, types.EvidenceRelationHistorical).
		Count(&firstRunHistorical).Error)
	require.Equal(t, int64(1), firstRunHistorical)

	secondRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, event.ID)
	require.NoError(t, err)
	require.NotEqual(t, firstRun.ID, secondRun.ID)
	var current []types.EvidenceNodeLink
	require.NoError(t, db.Where("event_id = ? AND relation_state = ?", event.ID, types.EvidenceRelationCurrent).
		Order("page_uuid ASC, id ASC").Find(&current).Error)
	require.Len(t, current, 2, "rerun must publish exactly one current link per current source-ref row")
	expectedPages := []string{pageA.ID, pageB.ID}
	if expectedPages[0] > expectedPages[1] {
		expectedPages[0], expectedPages[1] = expectedPages[1], expectedPages[0]
	}
	require.Equal(t, expectedPages[0], current[0].PageUUID)
	require.Equal(t, expectedPages[1], current[1].PageUUID)
	for _, link := range current {
		require.Equal(t, secondRun.ID, link.ResolutionRunID)
		require.Equal(t, secondRun.RunMappingRevision, link.MappingRevision)
	}
	var refreshedScope types.CitationProfileScope
	require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
	require.Equal(t, secondRun.RunMappingRevision, refreshedScope.MappingRevision)
}

func TestCitationProfilePostgresLinksUseRunMappingRevision(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	tenantID := uint64(77006)
	subjectID := "f1-revision-pg-" + uuid.NewString()
	kbID := uuid.NewString()
	sourceID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := &types.CitationProfileScope{
		ID:                      uuid.NewString(),
		TenantID:                tenantID,
		SubjectID:               subjectID,
		KnowledgeBaseID:         kbID,
		SubjectEpoch:            uuid.NewString(),
		ProfileReadVersion:      1,
		ProfilePolicyVersion:    types.CitationProfilePolicyVersion,
		RetentionPolicyVersion:  types.CitationProfileRetentionPolicyVersion,
		Enabled:                 true,
		MappingRevision:         10,
		SourceUniverseWatermark: "wm-before-rerun",
		ACLCheckState:           "current",
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)
	pageA := uuid.NewString()
	pageB := uuid.NewString()
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageA, "f1/revision-a-"+uuid.NewString(), "F1 revision A", nil, now))
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageB, "f1/revision-b-"+uuid.NewString(), "F1 revision B", nil, now))
	refA := citationProfilePGSourceRef(kbID, sourceID, pageA, 1, 1, "wm-ref-a", now)
	refB := citationProfilePGSourceRef(kbID, sourceID, pageB, 3, 3, "wm-ref-b", now)
	refA.TenantID = tenantID
	refB.TenantID = tenantID
	require.NoError(t, db.Create(&[]types.WikiSourceRefIndex{refA, refB}).Error)
	event := citationProfilePGEvent(tenantID, subjectID, kbID, scope, sourceID, uuid.NewString(), 0, now)
	require.NoError(t, db.Create(&event).Error)

	repo := NewCitationProfileRepository(db)
	run, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, event.ID)
	require.NoError(t, err)
	var refreshedScope types.CitationProfileScope
	require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
	require.Equal(t, uint64(10), run.RunMappingRevision)
	require.Equal(t, run.RunMappingRevision, refreshedScope.MappingRevision)
	var links []types.EvidenceNodeLink
	require.NoError(t, db.Where("resolution_run_id = ?", run.ID).Order("page_uuid ASC").Find(&links).Error)
	require.Len(t, links, 2)
	var refs []types.WikiSourceRefIndex
	require.NoError(t, db.Where(
		"tenant_id = ? AND knowledge_base_id = ? AND source_knowledge_id = ? AND lifecycle_state = ?",
		tenantID, kbID, sourceID, "current",
	).Find(&refs).Error)
	currentKeys := make(map[citationProfileSourceRefKey]struct{}, len(refs))
	for _, ref := range refs {
		currentKeys[citationProfileSourceRefKeyFromIndex(ref)] = struct{}{}
	}
	for _, link := range links {
		require.Equal(t, run.RunMappingRevision, link.MappingRevision)
		require.False(t, citationProfileLinkStale(&refreshedScope, link, currentKeys),
			"a link published at the run snapshot must not be stale merely because source-ref row revisions are lower")
	}
}

func TestCitationProfilePostgresWikiSourceRefSameVersionRemovalInvalidatesAgain(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	tenantID := uint64(77007)
	subjectID := "f1-same-version-pg-" + uuid.NewString()
	kbID := uuid.NewString()
	sourceID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           uuid.NewString(),
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          "current",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)
	page := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "f1/same-version-pg-" + uuid.NewString(),
		Title:           "F1 same version PG",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		Aliases:         types.StringArray{},
		CategoryPath:    types.StringArray{},
		SourceRefs:      types.StringArray{},
		ChunkRefs:       types.StringArray{},
		InLinks:         types.StringArray{},
		OutLinks:        types.StringArray{},
		PageMetadata:    types.JSON(`{}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	wikiRepo := newCitationProfileWikiPageRepository(db)
	require.NoError(t, wikiRepo.Create(context.Background(), page))
	event := citationProfilePGEvent(tenantID, subjectID, kbID, scope, sourceID, uuid.NewString(), 0, now)
	require.NoError(t, db.Create(&event).Error)
	run, err := NewCitationProfileRepository(db).ResolveEvidenceEvent(context.Background(), tenantID, subjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, run.Status)

	page.SourceRefs = types.StringArray{sourceID + "|added"}
	require.NoError(t, wikiRepo.UpdateMeta(context.Background(), page))
	require.Equal(t, 1, page.Version)
	var afterAdd types.CitationProfileScope
	require.NoError(t, db.First(&afterAdd, "id = ?", scope.ID).Error)

	page.SourceRefs = nil
	require.NoError(t, wikiRepo.UpdateMeta(context.Background(), page))
	require.Equal(t, 1, page.Version)
	var afterRemove types.CitationProfileScope
	require.NoError(t, db.First(&afterRemove, "id = ?", scope.ID).Error)
	require.Greater(t, afterRemove.ProfileReadVersion, afterAdd.ProfileReadVersion)
	require.Greater(t, afterRemove.MappingRevision, afterAdd.MappingRevision)
	var currentRefs int64
	require.NoError(t, db.Model(&types.WikiSourceRefIndex{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND page_uuid = ? AND lifecycle_state = ?",
			tenantID, kbID, page.ID, "current").
		Count(&currentRefs).Error)
	require.Zero(t, currentRefs)
	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedEvent.Status)
	require.Empty(t, storedEvent.ActiveRunID)
}

func TestCitationProfilePostgresWikiSourceUniverseRequeuesTerminalOutbox(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	for index, terminalStatus := range []string{
		types.CitationProfileOutboxStatusDeadletter,
		types.CitationProfileOutboxStatusDelivered,
	} {
		t.Run(terminalStatus, func(t *testing.T) {
			tenantID := uint64(77008 + index)
			subjectID := "f1-outbox-pg-" + terminalStatus + "-" + uuid.NewString()
			kbID := uuid.NewString()
			sourceID := uuid.NewString()
			now := time.Now().UTC().Truncate(time.Microsecond)
			old := now.Add(-48 * time.Hour)
			scope := &types.CitationProfileScope{
				ID:                     uuid.NewString(),
				TenantID:               tenantID,
				SubjectID:              subjectID,
				KnowledgeBaseID:        kbID,
				SubjectEpoch:           uuid.NewString(),
				ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
				RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
				Enabled:                true,
				ACLCheckState:          "current",
				CreatedAt:              old,
				UpdatedAt:              old,
			}
			citationProfilePGBindCurrentOwnerScope(scope, now)
			require.NoError(t, db.Create(scope).Error)
			event := citationProfilePGEvent(tenantID, subjectID, kbID, scope, sourceID, uuid.NewString(), 0, old)
			event.Status = types.CitationProfileEventStatusResolvedEmpty
			require.NoError(t, db.Create(&event).Error)
			terminalAt := old.Add(time.Hour)
			outbox := &types.CitationProfileEventOutbox{
				ID:               uuid.NewString(),
				TenantID:         tenantID,
				SubjectID:        subjectID,
				KnowledgeBaseID:  kbID,
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
			require.NoError(t, newCitationProfileWikiPageRepository(db).Create(context.Background(), &types.WikiPage{
				ID:              uuid.NewString(),
				TenantID:        tenantID,
				KnowledgeBaseID: kbID,
				Slug:            "f1/outbox-pg-" + terminalStatus + "-" + uuid.NewString(),
				Title:           "F1 outbox PG " + terminalStatus,
				PageType:        types.WikiPageTypeConcept,
				Status:          types.WikiPageStatusPublished,
				Version:         1,
				Aliases:         types.StringArray{},
				CategoryPath:    types.StringArray{},
				SourceRefs:      types.StringArray{sourceID + "|requeue"},
				ChunkRefs:       types.StringArray{},
				InLinks:         types.StringArray{},
				OutLinks:        types.StringArray{},
				PageMetadata:    types.JSON(`{}`),
				CreatedAt:       requeueAt,
				UpdatedAt:       requeueAt,
			}))
			requeueFinishedAt, err := citationProfileDatabaseNow(db)
			require.NoError(t, err)

			var stored types.CitationProfileEventOutbox
			require.NoError(t, db.First(&stored, "id = ?", outbox.ID).Error)
			require.Equal(t, types.CitationProfileOutboxStatusPending, stored.Status)
			require.Zero(t, stored.AttemptCount)
			require.False(t, stored.NextAttemptAt.Before(requeueStartedAt.Add(-time.Second)))
			require.False(t, stored.NextAttemptAt.After(requeueFinishedAt.Add(time.Second)))
			require.Nil(t, stored.LockedAt)
			require.Empty(t, stored.LockedBy)
			require.Nil(t, stored.DeliveredAt)
			require.Nil(t, stored.DeadletterAt)
			require.Empty(t, stored.LastErrorCode)
			require.Empty(t, stored.LastErrorMessage)
			require.True(t, stored.CreatedAt.After(terminalAt), "requeue must replace the terminal row's exhausted max-age budget")
			require.False(t, stored.CreatedAt.Before(requeueStartedAt.Add(-time.Second)))
			require.False(t, stored.CreatedAt.After(requeueFinishedAt.Add(time.Second)))
		})
	}
}

func TestCitationProfilePostgresMultiScopeInvalidationLocksScopesInDatabaseOrder(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))

	tenantID := uint64(77003)
	kbID := uuid.NewString()
	now := time.Now().UTC().Truncate(time.Microsecond)
	scopeA := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              "f6-pg-subject-a-" + uuid.NewString(),
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           uuid.NewString(),
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          "current",
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	scopeZ := *scopeA
	scopeZ.ID = uuid.NewString()
	scopeZ.SubjectID = "f6-pg-subject-z-" + uuid.NewString()
	if scopeA.ID > scopeZ.ID {
		scopeA.ID, scopeZ.ID = scopeZ.ID, scopeA.ID
	}
	citationProfilePGBindCurrentOwnerScope(scopeA, now)
	citationProfilePGBindCurrentOwnerScope(&scopeZ, now)
	require.NoError(t, db.Create(&[]types.CitationProfileScope{scopeZ, *scopeA}).Error)
	eventZ := citationProfilePGEvent(tenantID, scopeZ.SubjectID, kbID, &scopeZ, "f6-pg-source-z", uuid.NewString(), 0, now)
	eventA := citationProfilePGEvent(tenantID, scopeA.SubjectID, kbID, scopeA, "f6-pg-source-a", uuid.NewString(), 0, now)
	require.NoError(t, db.Create(&[]types.CitationProfileEvent{eventZ, eventA}).Error)

	capture := &citationProfilePGSQLCapture{}
	observedDB := db.Session(&gorm.Session{Logger: gormlogger.New(capture, gormlogger.Config{LogLevel: gormlogger.Info})})
	pageZFirst := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		SourceRefs:      types.StringArray{"f6-pg-source-z", "f6-pg-source-a"},
		UpdatedAt:       now,
	}
	require.NoError(t, observedDB.Transaction(func(tx *gorm.DB) error {
		return markCitationProfileMappingsDirtyForPage(tx, pageZFirst, now)
	}))

	var scopeLockSQL []string
	for _, line := range capture.lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "citation_profile_scopes") && strings.Contains(lower, "for update") {
			scopeLockSQL = append(scopeLockSQL, line)
		}
	}
	require.NotEmpty(t, scopeLockSQL)
	normalizedSQL := strings.ReplaceAll(strings.ToLower(strings.Join(scopeLockSQL, "\n")), `"`, "")
	require.Contains(t, normalizedSQL, "order by id asc")

	pageAFirst := *pageZFirst
	pageAFirst.ID = uuid.NewString()
	pageAFirst.SourceRefs = types.StringArray{"f6-pg-source-a", "f6-pg-source-z"}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, page := range []*types.WikiPage{pageZFirst, &pageAFirst} {
		page := page
		go func() {
			<-start
			results <- db.Transaction(func(tx *gorm.DB) error {
				return markCitationProfileMappingsDirtyForPage(tx, page, now.Add(time.Second))
			})
		}()
	}
	close(start)
	deadline := time.After(10 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case concurrentErr := <-results:
			require.NoError(t, concurrentErr)
		case <-deadline:
			t.Fatal("multi-scope invalidations did not complete; possible lock-order deadlock")
		}
	}

	var refreshedScopes []types.CitationProfileScope
	require.NoError(t, db.Where("id IN ?", []string{scopeA.ID, scopeZ.ID}).Order("id ASC").Find(&refreshedScopes).Error)
	require.Len(t, refreshedScopes, 2)
	require.Greater(t, refreshedScopes[0].ProfileReadVersion, uint64(1))
	require.Greater(t, refreshedScopes[1].ProfileReadVersion, uint64(1))
}

func requireCitationProfilePGFactA(t *testing.T, db *gorm.DB, repo interfaces.CitationProfileRepository, tenantID uint64, subjectID string, now time.Time) {
	t.Helper()
	kbID := uuid.NewString()
	knowledgeID := uuid.NewString()
	pageID := uuid.NewString()
	zero := uint64(0)

	scope, err := repo.SetEnrollment(citationProfilePGOwnerContext(context.Background(), tenantID, subjectID), tenantID, subjectID, kbID, true, &zero, uuid.NewString())
	require.NoError(t, err)
	citationProfilePGAuthorizeScope(t, db, scope)
	require.NoError(t, createCitationProfilePGFactATables(db))
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageID, "fact/a", "Fact A Page", nil, now))
	require.NoError(t, db.Create(citationProfilePGSourceRef(kbID, knowledgeID, pageID, 1, 30, "wm-fact-a", now)).Error)

	messageID := uuid.NewString()
	sessionID := uuid.NewString()
	require.NoError(t, insertCitationProfilePGFactAFixture(db, tenantID, subjectID, kbID, knowledgeID, messageID, sessionID, now))
	message := &types.Message{
		ID:        messageID,
		SessionID: sessionID,
		RequestID: uuid.NewString(),
		Role:      "assistant",
		Content:   "answer grounded in Fact A knowledge",
		KnowledgeReferences: types.References{&types.SearchResult{
			ID:                      "result-fact-a",
			Content:                 "source chunk",
			KnowledgeID:             knowledgeID,
			KnowledgeBaseID:         kbID,
			KnowledgeTitle:          "Fact A Knowledge",
			ChunkIndex:              7,
			StartAt:                 1,
			EndAt:                   12,
			Seq:                     1,
			MatchType:               types.MatchTypeEmbedding,
			ChunkType:               "text",
			ContentRevision:         2,
			KnowledgeFilename:       "fact-a.md",
			KnowledgeSource:         "manual",
			KnowledgeChannel:        "web",
			MatchedContent:          "source chunk",
			KnowledgeCustomMetadata: "topic: fact-a",
		}},
		IsCompleted: true,
		CreatedAt:   now,
		UpdatedAt:   now.Add(time.Second),
	}

	count, err := repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	var event types.CitationProfileEvent
	require.NoError(t, db.Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND message_id = ?", tenantID, subjectID, kbID, messageID).First(&event).Error)
	require.Equal(t, types.CitationProfileEventStatusResolved, event.Status)
	require.NotEmpty(t, event.ActiveRunID)
	require.Equal(t, knowledgeID, event.SourceKnowledgeID)
	require.Equal(t, 7, *event.SourceChunkIndex)

	var proof map[string]interface{}
	require.NoError(t, json.Unmarshal(event.KnowledgeBaseProof, &proof))
	require.Equal(t, kbID, proof["knowledge_base_id"])
	require.Equal(t, float64(tenantID), proof["knowledge_base_tenant_id"])

	var outboxDelivered int64
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).Where("event_id = ? AND delivered_at IS NOT NULL", event.ID).Count(&outboxDelivered).Error)
	require.Equal(t, int64(1), outboxDelivered)

	var link types.EvidenceNodeLink
	require.NoError(t, db.Where("event_id = ? AND resolution_run_id = ?", event.ID, event.ActiveRunID).First(&link).Error)
	require.Equal(t, pageID, link.PageUUID)
	require.Equal(t, types.EvidenceRelationCurrent, link.RelationState)

	status, err := repo.GetScopeStatus(context.Background(), tenantID, subjectID, kbID)
	require.NoError(t, err)
	require.Equal(t, scope.ProfileReadVersion+2, status.ProfileReadVersion)
	require.Equal(t, 0, status.PendingEventCount)
}

func requireCitationProfilePGFactBExactRetryAndFailure(t *testing.T, db *gorm.DB, repo interfaces.CitationProfileRepository, tenantID uint64, subjectID string, now time.Time) {
	t.Helper()
	kbID := uuid.NewString()
	zeroKnowledge := uuid.NewString()
	oneKnowledge := uuid.NewString()
	manyKnowledge := uuid.NewString()
	tooManyKnowledge := uuid.NewString()
	pageOne := uuid.NewString()
	pageManyA := uuid.NewString()
	pageManyB := uuid.NewString()
	zero := uint64(0)

	scope, err := repo.SetEnrollment(citationProfilePGOwnerContext(context.Background(), tenantID, subjectID), tenantID, subjectID, kbID, true, &zero, uuid.NewString())
	require.NoError(t, err)
	citationProfilePGAuthorizeScope(t, db, scope)
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageOne, "fact-b/one", "Fact B One", nil, now))
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageManyA, "fact-b/many-a", "Fact B Many A", nil, now.Add(time.Second)))
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageManyB, "fact-b/many-b", "Fact B Many B", nil, now.Add(2*time.Second)))

	refs := []types.WikiSourceRefIndex{
		citationProfilePGSourceRef(kbID, oneKnowledge, pageOne, 1, 40, "wm-one", now),
		citationProfilePGSourceRef(kbID, manyKnowledge, pageManyA, 1, 41, "wm-many", now),
		citationProfilePGSourceRef(kbID, manyKnowledge, pageManyB, 1, 42, "wm-many", now),
	}
	limits := types.CitationProfileDefaultLimits()
	for i := 0; i <= limits.LinksPerEvent; i++ {
		refs = append(refs, citationProfilePGSourceRef(kbID, tooManyKnowledge, uuid.NewString(), 1, uint64(100+i), "wm-too-many", now))
	}
	require.NoError(t, db.Create(&refs).Error)

	zeroEvent := citationProfilePGEvent(tenantID, subjectID, kbID, scope, zeroKnowledge, "message-zero-pg", 0, now)
	oneEvent := citationProfilePGEvent(tenantID, subjectID, kbID, scope, oneKnowledge, "message-one-pg", 1, now.Add(time.Second))
	manyEvent := citationProfilePGEvent(tenantID, subjectID, kbID, scope, manyKnowledge, "message-many-pg", 2, now.Add(2*time.Second))
	tooManyEvent := citationProfilePGEvent(tenantID, subjectID, kbID, scope, tooManyKnowledge, "message-too-many-pg", 3, now.Add(3*time.Second))
	require.NoError(t, db.Create(&[]types.CitationProfileEvent{zeroEvent, oneEvent, manyEvent, tooManyEvent}).Error)

	zeroRun, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, zeroEvent.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, zeroRun.Status)
	require.Equal(t, 0, zeroRun.OutputCount)
	zeroReplay, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, zeroEvent.ID)
	require.NoError(t, err)
	require.Equal(t, zeroRun.ID, zeroReplay.ID)

	oneRun, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, oneEvent.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, oneRun.Status)
	require.Equal(t, 1, oneRun.OutputCount)
	oneReplay, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, oneEvent.ID)
	require.NoError(t, err)
	require.Equal(t, oneRun.ID, oneReplay.ID)

	manyRun, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, manyEvent.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, manyRun.Status)
	require.Equal(t, 2, manyRun.OutputCount)
	var manyLinks int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("event_id = ?", manyEvent.ID).Count(&manyLinks).Error)
	require.Equal(t, int64(2), manyLinks)

	failedRun, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, tooManyEvent.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusFailed, failedRun.Status)
	require.Equal(t, "too_many_links", failedRun.ErrorCode)
	var failedLinks int64
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("event_id = ?", tooManyEvent.ID).Count(&failedLinks).Error)
	require.Equal(t, int64(0), failedLinks)
	failedReplay, err := repo.ResolveEvidenceEvent(context.Background(), tenantID, subjectID, tooManyEvent.ID)
	require.NoError(t, err)
	require.Equal(t, failedRun.ID, failedReplay.ID)
}
func createCitationProfilePGFactATables(db *gorm.DB) error {
	return db.Exec(`
CREATE TABLE IF NOT EXISTS knowledge_bases (
	id VARCHAR(36) PRIMARY KEY,
	name VARCHAR(255) NOT NULL,
	description TEXT,
	tenant_id BIGINT NOT NULL,
	embedding_model_id VARCHAR(64) NOT NULL DEFAULT '',
	summary_model_id VARCHAR(64) NOT NULL DEFAULT '',
	rerank_model_id VARCHAR(64) NOT NULL DEFAULT '',
	created_at TIMESTAMP WITH TIME ZONE NOT NULL,
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
	deleted_at TIMESTAMP WITH TIME ZONE
);
CREATE TABLE IF NOT EXISTS knowledges (
	id VARCHAR(36) PRIMARY KEY,
	tenant_id BIGINT NOT NULL,
	knowledge_base_id VARCHAR(36) NOT NULL,
	type VARCHAR(50) NOT NULL,
	title VARCHAR(255) NOT NULL,
	description TEXT,
	source VARCHAR(2048) NOT NULL,
	channel VARCHAR(50) NOT NULL DEFAULT 'web',
	parse_status VARCHAR(50) NOT NULL DEFAULT 'completed',
	enable_status VARCHAR(50) NOT NULL DEFAULT 'enabled',
	file_name VARCHAR(255) NOT NULL DEFAULT '',
	file_type VARCHAR(50) NOT NULL DEFAULT '',
	file_size BIGINT NOT NULL DEFAULT 0,
	file_path TEXT NOT NULL DEFAULT '',
	file_hash VARCHAR(64) NOT NULL DEFAULT '',
	storage_size BIGINT NOT NULL DEFAULT 0,
	metadata JSONB NOT NULL DEFAULT '{}',
	custom_metadata JSONB NOT NULL DEFAULT '{}',
	created_at TIMESTAMP WITH TIME ZONE NOT NULL,
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
	processed_at TIMESTAMP WITH TIME ZONE,
	error_message TEXT,
	deleted_at TIMESTAMP WITH TIME ZONE
);
CREATE TABLE IF NOT EXISTS messages (
	id VARCHAR(36) PRIMARY KEY,
	request_id VARCHAR(36) NOT NULL,
	session_id VARCHAR(36) NOT NULL,
	role VARCHAR(50) NOT NULL,
	content TEXT NOT NULL,
	knowledge_references JSONB NOT NULL DEFAULT '[]',
	is_completed BOOLEAN NOT NULL DEFAULT false,
	created_at TIMESTAMP WITH TIME ZONE NOT NULL,
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
	deleted_at TIMESTAMP WITH TIME ZONE
);
CREATE TABLE IF NOT EXISTS sessions (
	id VARCHAR(36) PRIMARY KEY,
	tenant_id BIGINT NOT NULL,
	user_id VARCHAR(512) NOT NULL,
	deleted_at TIMESTAMP WITH TIME ZONE
)`).Error
}

func citationProfilePGAuthorizeScope(t *testing.T, db *gorm.DB, scope *types.CitationProfileScope) {
	t.Helper()
	require.NotNil(t, scope)
	require.Equal(t, types.CitationProfileACLStateUnknown, scope.ACLCheckState)
	authorizedAt := time.Now().UTC().Truncate(time.Microsecond)
	nextACLCheckAt := authorizedAt.Add(time.Hour)
	result := db.Model(&types.CitationProfileScope{}).
		Where("id = ? AND acl_check_state = ?", scope.ID, types.CitationProfileACLStateUnknown).
		Updates(map[string]interface{}{
			"acl_check_state":      types.CitationProfileACLStateCurrent,
			"acl_checked_at":       authorizedAt,
			"next_acl_check_at":    nextACLCheckAt,
			"profile_read_version": scope.ProfileReadVersion + 1,
			"updated_at":           authorizedAt,
		})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)
	scope.ACLCheckState = types.CitationProfileACLStateCurrent
	scope.ACLCheckedAt = &authorizedAt
	scope.NextACLCheckAt = &nextACLCheckAt
	scope.ProfileReadVersion++
}

func insertCitationProfilePGFactAFixture(db *gorm.DB, tenantID uint64, subjectID, kbID, knowledgeID, messageID, sessionID string, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO sessions (id, tenant_id, user_id) VALUES (?, ?, ?)`,
			sessionID, tenantID, subjectID,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(
			`INSERT INTO knowledge_bases (id, name, tenant_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			kbID, "Fact A KB", tenantID, now, now,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(
			`INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, description, source, channel, parse_status, enable_status, file_name, file_type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			knowledgeID, tenantID, kbID, "document", "Fact A Knowledge", "", "manual", "web", "completed", "enabled", "fact-a.md", "md", now, now,
		).Error; err != nil {
			return err
		}
		return tx.Exec(
			`INSERT INTO messages (id, request_id, session_id, role, content, knowledge_references, is_completed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, '[]'::jsonb, false, ?, ?)`,
			messageID, uuid.NewString(), sessionID, "assistant", "pending", now, now,
		).Error
	})
}

func ensureCitationProfilePGSchema(db *gorm.DB) error {
	citationProfilePGSchemaOnce.Do(func() {
		if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error; err != nil {
			citationProfilePGSchemaErr = err
			return
		}
		exists, err := citationProfilePGSchemaExists(db)
		if err != nil {
			citationProfilePGSchemaErr = err
			return
		}
		if !exists {
			migrationPath := filepath.Join("..", "..", "..", "migrations", "versioned", "000091_citation_profile.up.sql")
			migrationSQL, err := os.ReadFile(migrationPath)
			if err != nil {
				citationProfilePGSchemaErr = err
				return
			}
			if err := db.Exec(string(migrationSQL)).Error; err != nil {
				citationProfilePGSchemaErr = err
				return
			}
		}
		migrationPath := filepath.Join("..", "..", "..", "migrations", "versioned", "000092_citation_profile_event_identity.up.sql")
		migrationSQL, err := os.ReadFile(migrationPath)
		if err != nil {
			citationProfilePGSchemaErr = err
			return
		}
		if err := db.Exec(string(migrationSQL)).Error; err != nil {
			citationProfilePGSchemaErr = err
			return
		}
		migrationPath = filepath.Join("..", "..", "..", "migrations", "versioned", "000093_citation_profile_outbox_due_index.up.sql")
		migrationSQL, err = os.ReadFile(migrationPath)
		if err != nil {
			citationProfilePGSchemaErr = err
			return
		}
		if err := db.Exec(string(migrationSQL)).Error; err != nil {
			citationProfilePGSchemaErr = err
			return
		}
		if err := citationProfilePGF7EnsureACLSchema(db); err != nil {
			citationProfilePGSchemaErr = err
			return
		}
		citationProfilePGSchemaErr = createCitationProfilePGWikiPagesTable(db)
	})
	return citationProfilePGSchemaErr
}

func citationProfilePGBindCurrentOwnerScope(scope *types.CitationProfileScope, now time.Time) {
	if scope == nil {
		return
	}
	checkedAt := now.Add(-time.Minute).UTC()
	nextCheckAt := now.Add(time.Hour).UTC()
	scope.ACLCheckState = types.CitationProfileACLStateCurrent
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &nextCheckAt
	scope.ACLCheckLeaseUntil = nil
	scope.ACLCheckLeaseToken = ""
	scope.ACLPrincipalType = types.PrincipalWebUser
	scope.ACLPrincipalID = scope.SubjectID
	scope.ACLAuthenticatedTenantID = scope.TenantID
	scope.ACLAPIKeyID = 0
	scope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
	scope.ACLAccessPathID = ""
	if scope.ACLGeneration == 0 {
		scope.ACLGeneration = 1
	}
}

func citationProfilePGSchemaExists(db *gorm.DB) (bool, error) {
	var exists bool
	err := db.Raw(`SELECT EXISTS (
		SELECT 1
		FROM information_schema.tables
		WHERE table_schema = current_schema()
			AND table_name = 'citation_profile_scopes'
	)`).Scan(&exists).Error
	return exists, err
}

func createCitationProfilePGWikiPagesTable(db *gorm.DB) error {
	return db.Exec(`
CREATE TABLE IF NOT EXISTS wiki_pages (
	id VARCHAR(36) PRIMARY KEY,
	tenant_id BIGINT NOT NULL,
	knowledge_base_id VARCHAR(36) NOT NULL,
	slug VARCHAR(255) NOT NULL,
	title VARCHAR(512) NOT NULL,
	page_type VARCHAR(32) NOT NULL,
	status VARCHAR(32) NOT NULL,
	content TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	aliases JSON NOT NULL DEFAULT '[]',
	parent_slug VARCHAR(255) NOT NULL DEFAULT '',
	folder_id VARCHAR(36) NOT NULL DEFAULT '',
	category_path JSON NOT NULL DEFAULT '[]',
	wiki_path VARCHAR(1024) NOT NULL DEFAULT '',
	depth INTEGER NOT NULL DEFAULT 0,
	sort_order INTEGER NOT NULL DEFAULT 0,
	source_refs JSON NOT NULL DEFAULT '[]',
	chunk_refs JSON NOT NULL DEFAULT '[]',
	in_links JSON NOT NULL DEFAULT '[]',
	out_links JSON NOT NULL DEFAULT '[]',
	page_metadata JSON NOT NULL DEFAULT '{}',
	version INTEGER NOT NULL DEFAULT 1,
	last_edit_source VARCHAR(16) NOT NULL DEFAULT '',
	last_editor_id VARCHAR(64) NOT NULL DEFAULT '',
	created_at TIMESTAMP WITH TIME ZONE NOT NULL,
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
	deleted_at TIMESTAMP WITH TIME ZONE
)`).Error
}

func insertCitationProfilePGPage(db *gorm.DB, tenantID uint64, kbID, pageID, slug, title string, outLinks types.StringArray, now time.Time) error {
	outLinksJSON, err := json.Marshal(outLinks)
	if err != nil {
		return err
	}
	return db.Exec(
		`INSERT INTO wiki_pages (id, tenant_id, knowledge_base_id, slug, title, page_type, status, out_links, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?::json, ?, ?)`,
		pageID, tenantID, kbID, slug, title, types.WikiPageTypeConcept, types.WikiPageStatusPublished, string(outLinksJSON), now, now,
	).Error
}

func citationProfilePGSourceRef(kbID, knowledgeID, pageID string, pageVersion int, mappingRevision uint64, watermark string, now time.Time) types.WikiSourceRefIndex {
	return types.WikiSourceRefIndex{
		ID:                uuid.NewString(),
		TenantID:          77,
		KnowledgeBaseID:   kbID,
		SourceKnowledgeID: knowledgeID,
		PageUUID:          pageID,
		PageVersion:       pageVersion,
		PageSlug:          "doc/b",
		PageTitle:         "Page B",
		NormalizedRef:     knowledgeID,
		MappingRevision:   mappingRevision,
		LifecycleState:    "current",
		IndexWatermark:    watermark,
		IndexedAt:         now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func citationProfilePGEvent(tenantID uint64, subjectID, kbID string, scope *types.CitationProfileScope, knowledgeID, messageID string, referenceIndex int, now time.Time) types.CitationProfileEvent {
	return types.CitationProfileEvent{
		ID:                   uuid.NewString(),
		TenantID:             tenantID,
		SubjectID:            subjectID,
		KnowledgeBaseID:      kbID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		SessionID:            uuid.NewString(),
		MessageID:            messageID,
		MessageVersion:       "message-v1",
		MessageCompletedAt:   &now,
		OriginReferenceIndex: referenceIndex,
		SourceKnowledgeID:    knowledgeID,
		SourceResultID:       "source-result",
		SourceRefNormalized:  knowledgeID,
		SourceRefsSnapshot:   json.RawMessage(`{}`),
		KnowledgeSnapshot:    json.RawMessage(`{}`),
		KnowledgeBaseProof:   json.RawMessage(`{"knowledge_base_id":"` + kbID + `"}`),
		ProducerEventKey:     messageID + ":" + knowledgeID,
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func citationProfilePGFindNode(t *testing.T, nodes []types.CitationProfileNodeDTO, pageUUID string) types.CitationProfileNodeDTO {
	t.Helper()
	for _, node := range nodes {
		if node.PageUUID == pageUUID {
			return node
		}
	}
	t.Fatalf("node %s not found in %+v", pageUUID, nodes)
	return types.CitationProfileNodeDTO{}
}
