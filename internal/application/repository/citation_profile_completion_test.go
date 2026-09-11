//go:build cgo

package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestCitationProfileCompletionRejectsCrossSubjectSessionWithoutWriting(t *testing.T) {
	db := newCitationProfileCompletionTestDB(t)
	repo := NewCitationProfileRepository(db)
	now := time.Now().UTC()

	seedCitationProfileSession(t, db, 7, "owner", "session-owner", now)
	message := seedCitationProfileMessage(t, db, "session-owner", "message-owner", now)
	message.Content = "attacker update"
	message.IsCompleted = true

	count, err := repo.CompleteAssistantMessageWithEvents(context.Background(), 7, "attacker", message)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Zero(t, count)

	var persisted types.Message
	require.NoError(t, db.First(&persisted, "id = ?", message.ID).Error)
	require.Equal(t, "pending", persisted.Content)
	require.False(t, persisted.IsCompleted)

	var eventCount, outboxCount int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).Count(&eventCount).Error)
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).Count(&outboxCount).Error)
	require.Zero(t, eventCount)
	require.Zero(t, outboxCount)
}

func TestCitationProfileCompletionRollsBackScopeIdentityDriftBeforeCounterUpdate(t *testing.T) {
	db := newCitationProfileCompletionTestDB(t)
	repo := NewCitationProfileRepository(db)
	now := time.Now().UTC()
	tenantID := uint64(7)
	subjectID := "subject-scope-drift"
	kbID := "kb-scope-drift"
	knowledgeID := "knowledge-scope-drift"

	seedCitationProfileSession(t, db, tenantID, subjectID, "session-scope-drift", now)
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbID, now)
	seedCitationProfileKnowledge(t, db, tenantID, kbID, knowledgeID, now)
	scope := citationProfileTestScope("scope-drift", subjectID, kbID, "epoch-original", 10, 0)
	require.NoError(t, db.Create(scope).Error)
	message := seedCitationProfileMessage(t, db, "session-scope-drift", "message-scope-drift", now)
	message.IsCompleted = true
	message.Content = "must roll back"
	message.KnowledgeReferences = types.References{{
		ID: "ref-scope-drift", KnowledgeID: knowledgeID, KnowledgeBaseID: kbID, ChunkIndex: 1,
	}}

	callbackName := "topic4_force_scope_identity_drift_after_outbox"
	mutated := false
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if mutated || tx.Statement == nil || tx.Statement.Table != (types.CitationProfileEventOutbox{}).TableName() {
			return
		}
		mutated = true
		err := tx.Session(&gorm.Session{NewDB: true}).Exec(
			"UPDATE citation_profile_scopes SET subject_epoch = ? WHERE id = ?",
			"epoch-drifted",
			scope.ID,
		).Error
		if err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callbackName) })

	count, err := repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
	require.Error(t, err)
	require.Zero(t, count)
	require.True(t, mutated)

	var persisted types.Message
	require.NoError(t, db.First(&persisted, "id = ?", message.ID).Error)
	require.Equal(t, "pending", persisted.Content)
	require.False(t, persisted.IsCompleted)

	var eventCount, outboxCount int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).Count(&eventCount).Error)
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).Count(&outboxCount).Error)
	require.Zero(t, eventCount)
	require.Zero(t, outboxCount)

	var storedScope types.CitationProfileScope
	require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
	require.Equal(t, "epoch-original", storedScope.SubjectEpoch)
}

func TestCitationProfileCompletionCommitsMessageAndSkipsInvalidAdmissions(t *testing.T) {
	db := newCitationProfileCompletionTestDB(t)
	repo := NewCitationProfileRepository(db)
	now := time.Now().UTC()
	tenantID := uint64(7)
	subjectID := "subject-valid"
	kbID := "kb-valid"
	validKnowledgeID := "knowledge-valid"
	deletedKnowledgeID := "knowledge-deleted"

	seedCitationProfileSession(t, db, tenantID, subjectID, "session-valid", now)
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbID, now)
	seedCitationProfileKnowledge(t, db, tenantID, kbID, validKnowledgeID, now)
	deletedKnowledge := seedCitationProfileKnowledge(t, db, tenantID, kbID, deletedKnowledgeID, now)
	require.NoError(t, db.Delete(deletedKnowledge).Error)

	scope := citationProfileTestScope("scope-valid", subjectID, kbID, "epoch-valid", 10, 0)
	require.NoError(t, db.Create(scope).Error)
	message := seedCitationProfileMessage(t, db, "session-valid", "message-valid", now)
	message.IsCompleted = true
	message.Content = "answer"
	message.KnowledgeReferences = types.References{
		{ID: "ref-valid", KnowledgeID: validKnowledgeID, KnowledgeBaseID: kbID, ChunkIndex: 1},
		{ID: "ref-wrong-kb", KnowledgeID: validKnowledgeID, KnowledgeBaseID: "kb-other", ChunkIndex: 2},
		{ID: "ref-missing", KnowledgeID: "knowledge-missing", KnowledgeBaseID: kbID, ChunkIndex: 3},
		{ID: "ref-deleted", KnowledgeID: deletedKnowledgeID, KnowledgeBaseID: kbID, ChunkIndex: 4},
	}

	count, err := repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	var persisted types.Message
	require.NoError(t, db.First(&persisted, "id = ?", message.ID).Error)
	require.True(t, persisted.IsCompleted)
	require.Equal(t, message.Content, persisted.Content)
	require.Equal(t, message.KnowledgeReferences, persisted.KnowledgeReferences)

	var events []types.CitationProfileEvent
	require.NoError(t, db.Where("message_id = ?", message.ID).Find(&events).Error)
	require.Len(t, events, 1)
	require.Equal(t, validKnowledgeID, events[0].SourceKnowledgeID)
	require.Equal(t, kbID, events[0].KnowledgeBaseID)
}

func TestCitationProfileCompletionDoesNotRecreateRetractedProducerEvent(t *testing.T) {
	db := newCitationProfileCompletionTestDB(t)
	repo := NewCitationProfileRepository(db)
	now := time.Now().UTC()
	tenantID := uint64(7)
	subjectID := "subject-retry"
	kbID := "kb-retry"
	knowledgeID := "knowledge-retry"

	seedCitationProfileSession(t, db, tenantID, subjectID, "session-retry", now)
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbID, now)
	seedCitationProfileKnowledge(t, db, tenantID, kbID, knowledgeID, now)
	require.NoError(t, db.Create(citationProfileTestScope("scope-retry", subjectID, kbID, "epoch-retry", 10, 0)).Error)

	message := seedCitationProfileMessage(t, db, "session-retry", "message-retry", now)
	message.IsCompleted = true
	message.KnowledgeReferences = types.References{{
		ID: "ref-retry", KnowledgeID: knowledgeID, KnowledgeBaseID: kbID, ChunkIndex: 1,
	}}
	count, err := repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	var event types.CitationProfileEvent
	require.NoError(t, db.Where("message_id = ?", message.ID).First(&event).Error)
	retractedAt := now.Add(time.Minute)
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("id = ?", event.ID).
		Updates(map[string]interface{}{"retracted_at": retractedAt, "updated_at": retractedAt}).Error)

	message.UpdatedAt = now.Add(2 * time.Minute)
	count, err = repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
	require.NoError(t, err)
	require.Zero(t, count)

	var total int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("tenant_id = ? AND subject_id = ? AND message_id = ?", tenantID, subjectID, message.ID).
		Count(&total).Error)
	require.Equal(t, int64(1), total)
}

func TestCitationProfileCompletionRetriesFrozenEventWithoutMutableKnowledgeLookup(t *testing.T) {
	db := newCitationProfileCompletionTestDB(t)
	repo := NewCitationProfileRepository(db)
	now := time.Now().UTC()
	tenantID := uint64(7)
	subjectID := "subject-frozen-retry"
	kbID := "kb-frozen-retry"
	knowledgeID := "knowledge-frozen-retry"

	seedCitationProfileSession(t, db, tenantID, subjectID, "session-frozen-retry", now)
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbID, now)
	knowledge := seedCitationProfileKnowledge(t, db, tenantID, kbID, knowledgeID, now)
	require.NoError(t, db.Create(citationProfileTestScope("scope-frozen-retry", subjectID, kbID, "epoch-frozen-retry", 10, 0)).Error)

	message := seedCitationProfileMessage(t, db, "session-frozen-retry", "message-frozen-retry", now)
	message.IsCompleted = true
	message.KnowledgeReferences = types.References{{
		ID: "ref-frozen-retry", KnowledgeID: knowledgeID, KnowledgeBaseID: kbID, ChunkIndex: 1,
	}}
	count, err := repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	var event types.CitationProfileEvent
	require.NoError(t, db.Where("message_id = ?", message.ID).First(&event).Error)
	var outbox types.CitationProfileEventOutbox
	require.NoError(t, db.Where("event_id = ?", event.ID).First(&outbox).Error)
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("id = ?", event.ID).
		Updates(map[string]interface{}{
			"active_run_id": nil,
			"status":        types.CitationProfileEventStatusPendingResolution,
			"resolved_at":   nil,
		}).Error)
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).
		Where("id = ?", outbox.ID).
		Updates(map[string]interface{}{
			"status":        types.CitationProfileOutboxStatusPending,
			"delivered_at":  nil,
			"deadletter_at": nil,
		}).Error)
	require.NoError(t, db.Delete(knowledge).Error)

	message.UpdatedAt = now.Add(time.Minute)
	count, err = repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
	require.NoError(t, err)
	require.Zero(t, count)

	var retried types.CitationProfileEvent
	require.NoError(t, db.First(&retried, "id = ?", event.ID).Error)
	require.NotEmpty(t, retried.ActiveRunID)
	require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, retried.Status)
	var delivered types.CitationProfileEventOutbox
	require.NoError(t, db.First(&delivered, "id = ?", outbox.ID).Error)
	require.NotNil(t, delivered.DeliveredAt)
}

func TestCitationProfileCompletionLocksAllScopesInOneDatabaseOrder(t *testing.T) {
	db := newCitationProfileCompletionTestDB(t)
	now := time.Now().UTC()
	tenantID := uint64(7)
	subjectID := "subject-sorted-scopes"
	kbA := "kb-sorted-a"
	kbZ := "kb-sorted-z"
	knowledgeA := "knowledge-sorted-a"
	knowledgeZ := "knowledge-sorted-z"
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbA, now)
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbZ, now)
	seedCitationProfileKnowledge(t, db, tenantID, kbA, knowledgeA, now)
	seedCitationProfileKnowledge(t, db, tenantID, kbZ, knowledgeZ, now)
	require.NoError(t, db.Create(&[]types.CitationProfileScope{
		*citationProfileTestScope("scope-sorted-z", subjectID, kbZ, "epoch-sorted-z", 1, 0),
		*citationProfileTestScope("scope-sorted-a", subjectID, kbA, "epoch-sorted-a", 1, 0),
	}).Error)

	message := &types.Message{
		ID:        "message-sorted-scopes",
		SessionID: "session-sorted-scopes",
		Content:   "answer",
		KnowledgeReferences: types.References{
			{ID: "ref-z", KnowledgeID: knowledgeZ, KnowledgeBaseID: kbZ, ChunkIndex: 0},
			{ID: "ref-a", KnowledgeID: knowledgeA, KnowledgeBaseID: kbA, ChunkIndex: 1},
		},
		UpdatedAt: now,
	}
	capture := &citationProfileSQLCapture{}
	observedDB := db.Session(&gorm.Session{Logger: gormlogger.New(capture, gormlogger.Config{LogLevel: gormlogger.Info})})
	repo := &citationProfileRepository{db: observedDB}
	require.NoError(t, observedDB.Transaction(func(tx *gorm.DB) error {
		_, _, _, err := repo.buildCompletedAnswerEvents(tx, tenantID, subjectID, message)
		return err
	}))

	var scopeSelects []string
	for _, line := range capture.lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "select") && strings.Contains(lower, "citation_profile_scopes") {
			scopeSelects = append(scopeSelects, lower)
		}
	}
	require.Len(t, scopeSelects, 1, "all candidate scopes must be locked by one ordered database query")
	require.Contains(t, scopeSelects[0], "order by id asc")
}

func newCitationProfileCompletionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newCitationProfileRepositoryTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.Session{}, &types.Message{}, &types.Knowledge{}))
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS knowledge_bases (
	id TEXT PRIMARY KEY,
	tenant_id INTEGER NOT NULL,
	updated_at DATETIME NOT NULL,
	deleted_at DATETIME
)`).Error)
	return db
}

func seedCitationProfileSession(t *testing.T, db *gorm.DB, tenantID uint64, subjectID, sessionID string, now time.Time) {
	t.Helper()
	session := &types.Session{
		ID:        sessionID,
		TenantID:  tenantID,
		UserID:    subjectID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(session).Error)
}

func seedCitationProfileMessage(t *testing.T, db *gorm.DB, sessionID, messageID string, now time.Time) *types.Message {
	t.Helper()
	message := &types.Message{
		ID:                  messageID,
		SessionID:           sessionID,
		RequestID:           uuid.NewString(),
		Role:                "assistant",
		Content:             "pending",
		KnowledgeReferences: types.References{},
		AgentSteps:          types.AgentSteps{},
		MentionedItems:      types.MentionedItems{},
		Images:              types.MessageImages{},
		Attachments:         types.MessageAttachments{},
		Artifacts:           types.MessageArtifacts{},
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(message).Error)
	return message
}

func seedCitationProfileKnowledgeBase(t *testing.T, db *gorm.DB, tenantID uint64, kbID string, now time.Time) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO knowledge_bases (id, tenant_id, updated_at) VALUES (?, ?, ?)",
		kbID, tenantID, now,
	).Error)
}

func seedCitationProfileKnowledge(t *testing.T, db *gorm.DB, tenantID uint64, kbID, knowledgeID string, now time.Time) *types.Knowledge {
	t.Helper()
	knowledge := &types.Knowledge{
		ID:              knowledgeID,
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Type:            "document",
		Title:           "test knowledge",
		Source:          "manual",
		Channel:         "web",
		ParseStatus:     "completed",
		EnableStatus:    "enabled",
		Metadata:        types.JSON(`{}`),
		CustomMetadata:  types.JSON(`{}`),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(knowledge).Error)
	return knowledge
}
