//go:build t4pg

package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type citationProfilePGF6FirstScopeLockBarrier struct {
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newCitationProfilePGF6FirstScopeLockBarrier() *citationProfilePGF6FirstScopeLockBarrier {
	return &citationProfilePGF6FirstScopeLockBarrier{release: make(chan struct{})}
}

func (b *citationProfilePGF6FirstScopeLockBarrier) arrive() {
	b.mu.Lock()
	b.arrived++
	if b.arrived == 2 {
		close(b.release)
	}
	b.mu.Unlock()

	select {
	case <-b.release:
	case <-time.After(5 * time.Second):
	}
}

type citationProfilePGF6ScopeLockWriter struct {
	barrier *citationProfilePGF6FirstScopeLockBarrier
	once    sync.Once
}

func (w *citationProfilePGF6ScopeLockWriter) Printf(format string, args ...interface{}) {
	line := strings.ToLower(fmt.Sprintf(format, args...))
	line = strings.NewReplacer(`"`, "", "`", "").Replace(line)
	if !strings.Contains(line, "citation_profile_scopes") ||
		!strings.Contains(line, "for update") ||
		!strings.Contains(line, "knowledge_base_id =") {
		return
	}
	w.once.Do(w.barrier.arrive)
}

func TestCitationProfilePostgresCompletionOppositeScopeOrderAvoidsDeadlock(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}

	setupDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	setupSQLDB, err := setupDB.DB()
	require.NoError(t, err)
	defer setupSQLDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(setupDB))
	require.NoError(t, createCitationProfilePGFactATables(setupDB))

	now := time.Now().UTC().Truncate(time.Microsecond)
	tenantID := uint64(now.UnixNano())
	subjectID := "f6-completion-subject-" + uuid.NewString()
	kbA, kbB := uuid.NewString(), uuid.NewString()
	knowledgeA, knowledgeB := uuid.NewString(), uuid.NewString()
	messageForwardID, messageReverseID := uuid.NewString(), uuid.NewString()
	sessionForwardID, sessionReverseID := uuid.NewString(), uuid.NewString()
	require.NoError(t, insertCitationProfilePGFactAFixture(
		setupDB, tenantID, subjectID, kbA, knowledgeA, messageForwardID, sessionForwardID, now,
	))
	require.NoError(t, insertCitationProfilePGFactAFixture(
		setupDB, tenantID, subjectID, kbB, knowledgeB, messageReverseID, sessionReverseID, now,
	))

	scopeA := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbA,
		SubjectEpoch:           uuid.NewString(),
		ProfileReadVersion:     1,
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          types.CitationProfileACLStateCurrent,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	scopeB := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbB,
		SubjectEpoch:           uuid.NewString(),
		ProfileReadVersion:     1,
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          types.CitationProfileACLStateCurrent,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scopeA, now)
	citationProfilePGBindCurrentOwnerScope(scopeB, now)
	require.NoError(t, setupDB.Create(&[]types.CitationProfileScope{*scopeB, *scopeA}).Error)

	messageForward := &types.Message{
		ID:        messageForwardID,
		SessionID: sessionForwardID,
		RequestID: uuid.NewString(),
		Role:      "assistant",
		Content:   "forward completion",
		KnowledgeReferences: types.References{
			{ID: "forward-a", KnowledgeID: knowledgeA, KnowledgeBaseID: kbA, ChunkIndex: 0},
			{ID: "forward-b", KnowledgeID: knowledgeB, KnowledgeBaseID: kbB, ChunkIndex: 1},
		},
		IsCompleted: true,
		CreatedAt:   now,
		UpdatedAt:   now.Add(time.Second),
	}
	messageReverse := &types.Message{
		ID:        messageReverseID,
		SessionID: sessionReverseID,
		RequestID: uuid.NewString(),
		Role:      "assistant",
		Content:   "reverse completion",
		KnowledgeReferences: types.References{
			{ID: "reverse-b", KnowledgeID: knowledgeB, KnowledgeBaseID: kbB, ChunkIndex: 0},
			{ID: "reverse-a", KnowledgeID: knowledgeA, KnowledgeBaseID: kbA, ChunkIndex: 1},
		},
		IsCompleted: true,
		CreatedAt:   now,
		UpdatedAt:   now.Add(time.Second),
	}

	barrier := newCitationProfilePGF6FirstScopeLockBarrier()
	openWorkerDB := func(writer *citationProfilePGF6ScopeLockWriter) *gorm.DB {
		t.Helper()
		workerDB, openErr := gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: gormlogger.New(writer, gormlogger.Config{LogLevel: gormlogger.Info}),
		})
		require.NoError(t, openErr)
		workerSQLDB, sqlErr := workerDB.DB()
		require.NoError(t, sqlErr)
		t.Cleanup(func() { require.NoError(t, workerSQLDB.Close()) })
		return workerDB
	}
	forwardDB := openWorkerDB(&citationProfilePGF6ScopeLockWriter{barrier: barrier})
	reverseDB := openWorkerDB(&citationProfilePGF6ScopeLockWriter{barrier: barrier})
	forwardRepo := NewCitationProfileRepository(forwardDB)
	reverseRepo := NewCitationProfileRepository(reverseDB)

	type completionResult struct {
		name  string
		count int
		err   error
	}
	start := make(chan struct{})
	results := make(chan completionResult, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		<-start
		count, completeErr := forwardRepo.CompleteAssistantMessageWithEvents(ctx, tenantID, subjectID, messageForward)
		results <- completionResult{name: "forward", count: count, err: completeErr}
	}()
	go func() {
		<-start
		count, completeErr := reverseRepo.CompleteAssistantMessageWithEvents(ctx, tenantID, subjectID, messageReverse)
		results <- completionResult{name: "reverse", count: count, err: completeErr}
	}()
	close(start)

	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			var pgErr *pgconn.PgError
			if errors.As(result.err, &pgErr) {
				require.NotEqual(t, "40P01", pgErr.Code, "%s completion deadlocked", result.name)
			}
			require.NoError(t, result.err, "%s completion failed", result.name)
			require.Equal(t, 2, result.count, "%s completion must publish one event per KB", result.name)
		case <-ctx.Done():
			t.Fatalf("opposite-order completions did not finish within 10s: %v", ctx.Err())
		}
	}

	var events []types.CitationProfileEvent
	require.NoError(t, setupDB.Where(
		"tenant_id = ? AND subject_id = ? AND message_id IN ?",
		tenantID, subjectID, []string{messageForwardID, messageReverseID},
	).Order("message_id ASC, origin_reference_index ASC").Find(&events).Error)
	require.Len(t, events, 4)
	expected := map[string]map[int]string{
		messageForwardID: {0: kbA, 1: kbB},
		messageReverseID: {0: kbB, 1: kbA},
	}
	for _, event := range events {
		require.Equal(t, expected[event.MessageID][event.OriginReferenceIndex], event.KnowledgeBaseID)
		require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, event.Status)
		require.NotEmpty(t, event.ActiveRunID)
	}
}
