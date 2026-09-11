//go:build cgo

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCitationProfileDurableACLTransitionsIgnoreSkewedCallerClocks(t *testing.T) {
	for _, skew := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			db := newCitationProfileRepositoryTestDB(t)
			scope := citationProfileTestScope(
				"scope-f7-durable-invalidation-"+skew.String(),
				"user-f7-durable-invalidation-"+skew.String(),
				"kb-f7-durable-invalidation-"+skew.String(),
				"epoch-f7-durable-invalidation-"+skew.String(),
				7,
				0,
			)
			require.NoError(t, db.Create(scope).Error)

			before := time.Now().UTC()
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return invalidateCitationProfileACLTx(tx, types.CitationProfileACLMutation{
					PrincipalType: types.PrincipalWebUser,
					PrincipalID:   scope.SubjectID,
				}, before.Add(skew))
			}))
			after := time.Now().UTC()

			var invalidated types.CitationProfileScope
			require.NoError(t, db.First(&invalidated, "id = ?", scope.ID).Error)
			require.NotNil(t, invalidated.NextACLCheckAt)
			requireDatabaseClockWindow(t, *invalidated.NextACLCheckAt, before, after)
			requireDatabaseClockWindow(t, invalidated.UpdatedAt, before, after)

			bindingScope := citationProfileTestScope(
				"scope-f7-durable-binding-"+skew.String(),
				"api_tenant_key:7:42",
				"kb-f7-durable-binding-"+skew.String(),
				"epoch-f7-durable-binding-"+skew.String(),
				9,
				0,
			)
			require.NoError(t, db.Create(bindingScope).Error)
			binding := types.CitationProfileACLBinding{
				PrincipalType:         types.PrincipalAPITenant,
				PrincipalID:           "7",
				AuthenticatedTenantID: 7,
				APIKeyID:              42,
				AccessPath:            types.CitationProfileACLAccessPathOwner,
			}
			before = time.Now().UTC()
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				return (&citationProfileRepository{db: db}).refreshCitationProfileACLBinding(
					tx, bindingScope, binding, before.Add(skew),
				)
			}))
			after = time.Now().UTC()

			var rebound types.CitationProfileScope
			require.NoError(t, db.First(&rebound, "id = ?", bindingScope.ID).Error)
			require.Equal(t, types.CitationProfileACLStateUnknown, rebound.ACLCheckState)
			require.NotNil(t, rebound.NextACLCheckAt)
			requireDatabaseClockWindow(t, *rebound.NextACLCheckAt, before, after)
			requireDatabaseClockWindow(t, rebound.UpdatedAt, before, after)
		})
	}
}

func TestCitationProfileEnrollmentAndExportUseDatabaseClock(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	const (
		tenantID  = uint64(7)
		subjectID = "api_tenant_key:7:42"
		kbID      = "kb-f7-durable-enrollment-export"
	)
	ctx := citationProfileEnrollmentF7Context(
		tenantID,
		types.Principal{Type: types.PrincipalAPITenant, ID: "7"},
		tenantID,
		42,
		types.CitationProfileACLAccessPathOwner,
		"",
	)
	zero := uint64(0)
	before := time.Now().UTC()
	scope, err := repo.SetEnrollment(ctx, tenantID, subjectID, kbID, true, &zero, "f7-durable-enrollment")
	after := time.Now().UTC()
	require.NoError(t, err)
	require.NotNil(t, scope)
	require.Equal(t, types.CitationProfileACLStateUnknown, scope.ACLCheckState)
	require.NotNil(t, scope.NextACLCheckAt)
	requireDatabaseClockWindow(t, *scope.NextACLCheckAt, before, after)
	requireDatabaseClockWindow(t, scope.CreatedAt, before, after)

	// Reuse the timestamp returned from the enrollment transaction instead of
	// the caller's wall clock. The production ACL gate is deliberately evaluated
	// with the database clock, so the fixture must not create a boundary from a
	// potentially skewed application timestamp between two transactions.
	require.NotNil(t, scope.NextACLCheckAt)
	checkedAt := *scope.NextACLCheckAt
	validUntil := checkedAt.Add(time.Hour)
	require.NoError(t, db.Model(&types.CitationProfileScope{}).Where("id = ?", scope.ID).
		Updates(map[string]interface{}{
			"acl_check_state":   types.CitationProfileACLStateCurrent,
			"acl_checked_at":    checkedAt,
			"next_acl_check_at": validUntil,
		}).Error)
	scope.ACLCheckState = types.CitationProfileACLStateCurrent
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &validUntil

	before = time.Now().UTC()
	operation, err := repo.CreateExportOperation(
		ctx, tenantID, subjectID, kbID, scope.ProfileReadVersion,
		"f7-durable-export", types.CitationProfileExportFormatJSON,
	)
	after = time.Now().UTC()
	require.NoError(t, err)
	require.NotNil(t, operation)
	require.NotNil(t, operation.ExpiresAt)
	requireDatabaseClockWindow(t, operation.CreatedAt, before, after)
	require.WithinDuration(t, operation.CreatedAt.Add(time.Hour), *operation.ExpiresAt, time.Millisecond)

	var payload struct {
		Snapshot struct {
			CapturedAt string `json:"captured_at"`
		} `json:"snapshot"`
	}
	require.NoError(t, json.Unmarshal(operation.ResultSummary, &payload))
	capturedAt, err := time.Parse(time.RFC3339Nano, payload.Snapshot.CapturedAt)
	require.NoError(t, err)
	requireDatabaseClockWindow(t, capturedAt, before, after)
}

func TestCitationProfileCompletionQueueDeadlinesUseDatabaseClockUnderMessageSkew(t *testing.T) {
	for _, skew := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			db := newCitationProfileCompletionTestDB(t)
			repo := NewCitationProfileRepository(db)
			now := time.Now().UTC()
			const tenantID = uint64(7)
			suffix := skew.String()
			subjectID := "subject-f7-completion-clock-" + suffix
			kbID := "kb-f7-completion-clock-" + suffix
			knowledgeID := "knowledge-f7-completion-clock-" + suffix
			sessionID := "session-f7-completion-clock-" + suffix
			messageID := "message-f7-completion-clock-" + suffix

			seedCitationProfileSession(t, db, tenantID, subjectID, sessionID, now)
			seedCitationProfileKnowledgeBase(t, db, tenantID, kbID, now)
			seedCitationProfileKnowledge(t, db, tenantID, kbID, knowledgeID, now)
			scope := citationProfileTestScope("scope-f7-completion-clock-"+suffix, subjectID, kbID, "epoch-f7-completion-clock-"+suffix, 5, 0)
			require.NoError(t, db.Create(scope).Error)
			message := seedCitationProfileMessage(t, db, sessionID, messageID, now)
			message.IsCompleted = true
			message.UpdatedAt = now.Add(skew)
			message.KnowledgeReferences = types.References{{
				ID: "ref-f7-completion-clock-" + suffix, KnowledgeID: knowledgeID, KnowledgeBaseID: kbID, ChunkIndex: 1,
			}}

			before := time.Now().UTC()
			count, err := repo.CompleteAssistantMessageWithEvents(context.Background(), tenantID, subjectID, message)
			after := time.Now().UTC()
			require.NoError(t, err)
			require.Equal(t, 1, count)

			var event types.CitationProfileEvent
			require.NoError(t, db.Where("message_id = ?", message.ID).First(&event).Error)
			require.NotNil(t, event.MessageCompletedAt)
			require.WithinDuration(t, message.UpdatedAt, *event.MessageCompletedAt, time.Millisecond,
				"message completion remains domain provenance, not a queue deadline")
			var outbox types.CitationProfileEventOutbox
			require.NoError(t, db.Where("event_id = ?", event.ID).First(&outbox).Error)
			requireDatabaseClockWindow(t, outbox.NextAttemptAt, before, after)
			requireDatabaseClockWindow(t, outbox.CreatedAt, before, after)
			requireDatabaseClockWindow(t, outbox.UpdatedAt, before, after)
			var storedScope types.CitationProfileScope
			require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
			requireDatabaseClockWindow(t, storedScope.UpdatedAt, before, after)
		})
	}
}

func requireDatabaseClockWindow(t *testing.T, got, before, after time.Time) {
	t.Helper()
	require.False(t, got.Before(before.Add(-time.Second)), "timestamp %s predates DB call window %s", got, before)
	require.False(t, got.After(after.Add(time.Second)), "timestamp %s exceeds DB call window %s", got, after)
}
