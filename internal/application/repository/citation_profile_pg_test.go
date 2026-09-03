//go:build t4pg

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	citationProfilePGSchemaOnce sync.Once
	citationProfilePGSchemaErr  error
)

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
	ctx := context.Background()
	tenantID := uint64(77)
	subjectID := "subject-pg"
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

func requireCitationProfilePGFactA(t *testing.T, db *gorm.DB, repo interfaces.CitationProfileRepository, tenantID uint64, subjectID string, now time.Time) {
	t.Helper()
	kbID := uuid.NewString()
	knowledgeID := uuid.NewString()
	pageID := uuid.NewString()
	zero := uint64(0)

	scope, err := repo.SetEnrollment(context.Background(), tenantID, subjectID, kbID, true, &zero, uuid.NewString())
	require.NoError(t, err)
	require.NoError(t, createCitationProfilePGFactATables(db))
	require.NoError(t, insertCitationProfilePGPage(db, tenantID, kbID, pageID, "fact/a", "Fact A Page", nil, now))
	require.NoError(t, db.Create(citationProfilePGSourceRef(kbID, knowledgeID, pageID, 1, 30, "wm-fact-a", now)).Error)

	messageID := uuid.NewString()
	sessionID := uuid.NewString()
	require.NoError(t, insertCitationProfilePGFactAFixture(db, tenantID, kbID, knowledgeID, messageID, sessionID, now))
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

	scope, err := repo.SetEnrollment(context.Background(), tenantID, subjectID, kbID, true, &zero, uuid.NewString())
	require.NoError(t, err)
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
)`).Error
}

func insertCitationProfilePGFactAFixture(db *gorm.DB, tenantID uint64, kbID, knowledgeID, messageID, sessionID string, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
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
		citationProfilePGSchemaErr = createCitationProfilePGWikiPagesTable(db)
	})
	return citationProfilePGSchemaErr
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
