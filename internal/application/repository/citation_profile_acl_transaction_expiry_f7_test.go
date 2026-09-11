//go:build cgo

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type citationProfileACLTestClock struct {
	now time.Time
}

func newCitationProfileACLExpiringScope(
	id string,
	subjectID string,
	kbID string,
	epoch string,
	readVersion uint64,
	pendingEvents int,
	clock *citationProfileACLTestClock,
) (*types.CitationProfileScope, time.Time) {
	scope := citationProfileTestScope(id, subjectID, kbID, epoch, readVersion, pendingEvents)
	checkedAt := clock.now.Add(-time.Minute)
	expiresAt := clock.now.Add(time.Minute)
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &expiresAt
	return scope, expiresAt
}

func advanceCitationProfileACLClockAfterCreate(
	t *testing.T,
	db *gorm.DB,
	table string,
	scopeID string,
	clock *citationProfileACLTestClock,
	expiresAt time.Time,
) *bool {
	t.Helper()
	advanced := false
	name := "p36_acl_expiry_after_create_" + table
	require.NoError(t, db.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
		if advanced || tx.Statement == nil || tx.Statement.Table != table {
			return
		}
		advanced = true
		clock.now = expiresAt
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Exec(
			"UPDATE citation_profile_scopes SET next_acl_check_at = ? WHERE id = ?",
			time.Now().UTC().Add(-time.Minute),
			scopeID,
		).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(name) })
	return &advanced
}

func advanceCitationProfileACLClockAfterQuery(
	t *testing.T,
	db *gorm.DB,
	table string,
	scopeID string,
	clock *citationProfileACLTestClock,
	expiresAt time.Time,
) *bool {
	t.Helper()
	advanced := false
	name := "p36_acl_expiry_after_query_" + table
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if advanced || tx.Statement == nil || tx.Statement.Table != table {
			return
		}
		advanced = true
		clock.now = expiresAt
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Exec(
			"UPDATE citation_profile_scopes SET next_acl_check_at = ? WHERE id = ?",
			time.Now().UTC().Add(-time.Minute),
			scopeID,
		).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(name) })
	return &advanced
}

func TestCitationProfileResolverRollsBackWhenACLExpiresBeforePublish(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	clock := &citationProfileACLTestClock{now: time.Now().UTC()}
	repo := &citationProfileRepository{db: db}
	scope, expiresAt := newCitationProfileACLExpiringScope(
		"scope-f7-resolver-expiry", "user-f7-resolver-expiry", "kb-f7-resolver-expiry",
		"epoch-f7-resolver-expiry", 7, 1, clock,
	)
	event := citationProfileACLTestEvent(scope, "event-f7-resolver-expiry", clock.now)
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&event).Error)
	advanced := advanceCitationProfileACLClockAfterCreate(
		t, db, (types.EvidenceResolutionRun{}).TableName(), scope.ID, clock, expiresAt,
	)

	_, err := repo.ResolveEvidenceEvent(context.Background(), scope.TenantID, scope.SubjectID, event.ID)
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
	require.True(t, *advanced, "test must cross the ACL deadline after resolver work begins")
	var runCount, linkCount int64
	require.NoError(t, db.Model(&types.EvidenceResolutionRun{}).Where("event_id = ?", event.ID).Count(&runCount).Error)
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("event_id = ?", event.ID).Count(&linkCount).Error)
	require.Zero(t, runCount)
	require.Zero(t, linkCount)
}

func TestCitationProfileCreateExportRollsBackWhenACLExpiresBeforeReadyPublish(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	clock := &citationProfileACLTestClock{now: time.Now().UTC()}
	repo := &citationProfileRepository{db: db}
	scope, expiresAt := newCitationProfileACLExpiringScope(
		"scope-f7-export-create-expiry", "user-f7-export-create-expiry", "kb-f7-export-create-expiry",
		"epoch-f7-export-create-expiry", 7, 0, clock,
	)
	require.NoError(t, db.Create(scope).Error)
	advanced := advanceCitationProfileACLClockAfterCreate(
		t, db, (types.CitationProfileOperation{}).TableName(), scope.ID, clock, expiresAt,
	)

	_, err := repo.CreateExportOperation(
		context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID,
		scope.ProfileReadVersion, "idem-f7-export-create-expiry", types.CitationProfileExportFormatJSON,
	)
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
	require.True(t, *advanced, "test must cross the ACL deadline after export serialization begins")
	var operationCount int64
	require.NoError(t, db.Model(&types.CitationProfileOperation{}).Where("scope_id = ?", scope.ID).Count(&operationCount).Error)
	require.Zero(t, operationCount)
}

func TestCitationProfileGetExportReturnsNothingWhenACLExpiresDuringLoad(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	clock := &citationProfileACLTestClock{now: time.Now().UTC()}
	repo := &citationProfileRepository{db: db}
	scope, expiresAt := newCitationProfileACLExpiringScope(
		"scope-f7-export-get-expiry", "user-f7-export-get-expiry", "kb-f7-export-get-expiry",
		"epoch-f7-export-get-expiry", 7, 0, clock,
	)
	export := citationProfileACLTestExport(scope, "export-f7-get-expiry", types.CitationProfileOperationStatusReady, clock.now)
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&export).Error)
	advanced := advanceCitationProfileACLClockAfterQuery(
		t, db, (types.CitationProfileOperation{}).TableName(), scope.ID, clock, expiresAt,
	)

	got, err := repo.GetExportOperation(
		context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, export.ID,
	)
	require.Nil(t, got)
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
	require.True(t, *advanced, "test must cross the ACL deadline while loading the export")
}

func TestCitationProfileCorrectionRollsBackWhenACLExpiresBeforeVersionPublish(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	clock := &citationProfileACLTestClock{now: time.Now().UTC()}
	repo := &citationProfileRepository{db: db}
	scope, expiresAt := newCitationProfileACLExpiringScope(
		"scope-f7-correction-expiry", "user-f7-correction-expiry", "kb-f7-correction-expiry",
		"epoch-f7-correction-expiry", 7, 0, clock,
	)
	event := citationProfileACLTestEvent(scope, "event-f7-correction-expiry", clock.now)
	event.Status = types.CitationProfileEventStatusResolved
	event.ActiveRunID = "run-f7-correction-expiry"
	resolvedAt := clock.now
	event.ResolvedAt = &resolvedAt
	link := &types.EvidenceNodeLink{
		ID: "link-f7-correction-expiry", TenantID: scope.TenantID, SubjectID: scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID, SubjectEpoch: scope.SubjectEpoch, ScopeID: scope.ID,
		EventID: event.ID, ResolutionRunID: event.ActiveRunID, SourceKnowledgeID: event.SourceKnowledgeID,
		PageUUID: "page-f7-correction-expiry", PageVersion: 1, NormalizedRef: event.SourceKnowledgeID,
		RelationState: types.EvidenceRelationCurrent, RelationSource: "source_ref_index", MappingRevision: 1,
		CreatedAt: clock.now,
	}
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(link).Error)
	advanced := advanceCitationProfileACLClockAfterCreate(
		t, db, (types.CitationProfileCorrection{}).TableName(), scope.ID, clock, expiresAt,
	)

	_, err := repo.ApplyCorrection(
		context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID,
		scope.ProfileReadVersion, "idem-f7-correction-expiry", types.CitationCorrectionRejectMapping,
		event.ID, link.PageUUID, "acl_expired_before_publish",
	)
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
	require.True(t, *advanced, "test must cross the ACL deadline after correction mutation begins")
	var correctionCount int64
	require.NoError(t, db.Model(&types.CitationProfileCorrection{}).Where("scope_id = ?", scope.ID).Count(&correctionCount).Error)
	require.Zero(t, correctionCount)
	var storedLink types.EvidenceNodeLink
	require.NoError(t, db.First(&storedLink, "id = ?", link.ID).Error)
	require.Equal(t, types.EvidenceRelationCurrent, storedLink.RelationState)
}

func TestCitationProfileCompletionRollsBackAllScopesWhenEarlierACLExpiresBeforeCommit(t *testing.T) {
	db := newCitationProfileCompletionTestDB(t)
	clock := &citationProfileACLTestClock{now: time.Now().UTC()}
	repo := &citationProfileRepository{db: db}
	const (
		tenantID  = uint64(7)
		subjectID = "user-f7-completion-expiry"
	)
	seedCitationProfileSession(t, db, tenantID, subjectID, "session-f7-completion-expiry", clock.now)

	kbA, kbZ := "kb-f7-completion-expiry-a", "kb-f7-completion-expiry-z"
	knowledgeA, knowledgeZ := "knowledge-f7-completion-expiry-a", "knowledge-f7-completion-expiry-z"
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbA, clock.now)
	seedCitationProfileKnowledgeBase(t, db, tenantID, kbZ, clock.now)
	seedCitationProfileKnowledge(t, db, tenantID, kbA, knowledgeA, clock.now)
	seedCitationProfileKnowledge(t, db, tenantID, kbZ, knowledgeZ, clock.now)

	scopeA, expiresAt := newCitationProfileACLExpiringScope(
		"scope-f7-completion-expiry-a", subjectID, kbA, "epoch-f7-completion-expiry-a", 7, 0, clock,
	)
	scopeZ := citationProfileTestScope(
		"scope-f7-completion-expiry-z", subjectID, kbZ, "epoch-f7-completion-expiry-z", 11, 0,
	)
	checkedAt := clock.now.Add(-time.Minute)
	longExpiry := clock.now.Add(time.Hour)
	scopeZ.ACLCheckedAt = &checkedAt
	scopeZ.NextACLCheckAt = &longExpiry
	require.NoError(t, db.Create(&[]types.CitationProfileScope{*scopeA, *scopeZ}).Error)

	message := seedCitationProfileMessage(
		t, db, "session-f7-completion-expiry", "message-f7-completion-expiry", clock.now,
	)
	message.IsCompleted = true
	message.Content = "must atomically fail closed across every cited scope"
	message.KnowledgeReferences = types.References{
		{ID: "ref-f7-completion-expiry-a", KnowledgeID: knowledgeA, KnowledgeBaseID: kbA, ChunkIndex: 0},
		{ID: "ref-f7-completion-expiry-z", KnowledgeID: knowledgeZ, KnowledgeBaseID: kbZ, ChunkIndex: 1},
	}

	advanced := false
	callbackName := "p36_acl_expiry_after_first_completion_scope_update"
	require.NoError(t, db.Callback().Update().After("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if advanced || tx.Statement == nil || tx.Statement.Table != (types.CitationProfileScope{}).TableName() {
			return
		}
		advanced = true
		clock.now = expiresAt
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Exec(
			"UPDATE citation_profile_scopes SET next_acl_check_at = ? WHERE id = ?",
			time.Now().UTC().Add(-time.Minute),
			scopeA.ID,
		).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callbackName) })

	count, err := repo.CompleteAssistantMessageWithEvents(
		context.Background(), tenantID, subjectID, message,
	)
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
	require.Zero(t, count)
	require.True(t, advanced, "test must cross the first scope's ACL deadline during the multi-scope transaction")

	var persisted types.Message
	require.NoError(t, db.First(&persisted, "id = ?", message.ID).Error)
	require.False(t, persisted.IsCompleted)
	require.Equal(t, "pending", persisted.Content)
	var eventCount, outboxCount int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).Where("message_id = ?", message.ID).Count(&eventCount).Error)
	require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).Where("subject_id = ?", subjectID).Count(&outboxCount).Error)
	require.Zero(t, eventCount)
	require.Zero(t, outboxCount)
}

func TestCitationProfileFinalGateUsesDatabaseClockWhenHostClockIsSlow(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	databaseNow := time.Now().UTC()
	slowHostNow := databaseNow.Add(-2 * time.Minute)
	expiredAtDatabase := databaseNow.Add(-time.Minute)
	checkedAt := slowHostNow.Add(-time.Minute)
	scope := citationProfileTestScope(
		"scope-f7-db-clock", "user-f7-db-clock", "kb-f7-db-clock", "epoch-f7-db-clock", 7, 0,
	)
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &expiredAtDatabase
	require.NoError(t, db.Create(scope).Error)
	repo := &citationProfileRepository{db: db}

	// The legacy host-clock predicate would still consider this row current.
	legacyWhere, legacyArgs := citationProfileACLCurrentSQL("citation_profile_scopes", slowHostNow)
	var legacyCount int64
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("id = ?", scope.ID).
		Where(legacyWhere, legacyArgs...).
		Count(&legacyCount).Error)
	require.Equal(t, int64(1), legacyCount)

	err := db.Transaction(func(tx *gorm.DB) error {
		return repo.ensureCitationProfileACLCurrentTx(tx, scope)
	})
	require.ErrorIs(t, err, types.ErrCitationProfileUnavailable,
		"a slow application node must not extend an authorization beyond the database's time")
}

func TestCitationProfileGetExportUsesDatabaseClockForArtifactExpiry(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	scope := citationProfileTestScope(
		"scope-f7-export-db-expiry",
		"user-f7-export-db-expiry",
		"kb-f7-export-db-expiry",
		"epoch-f7-export-db-expiry",
		7,
		0,
	)
	export := citationProfileACLTestExport(
		scope,
		"export-f7-db-expiry",
		types.CitationProfileOperationStatusReady,
		time.Now().UTC(),
	)
	expiredAt := time.Now().UTC().Add(-time.Minute)
	export.ExpiresAt = &expiredAt
	require.NoError(t, db.Create(scope).Error)
	require.NoError(t, db.Create(&export).Error)

	got, err := (&citationProfileRepository{db: db}).GetExportOperation(
		context.Background(),
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		export.ID,
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, types.CitationProfileOperationStatusExpired, got.Status,
		"download authorization must not depend on the service node's wall clock")
}
