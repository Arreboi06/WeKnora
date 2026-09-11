//go:build cgo

package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCitationProfileSQLiteReadFenceObservesConcurrentACLInvalidation(t *testing.T) {
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "citation-profile-read-fence.db"))
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	readerDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	writerDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	for _, db := range []*gorm.DB{readerDB, writerDB} {
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
		require.NoError(t, db.Exec("PRAGMA busy_timeout=5000").Error)
	}
	require.NoError(t, readerDB.AutoMigrate(
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

	const (
		tenantID  = uint64(7)
		subjectID = "user-f7-sqlite-read-fence"
		kbID      = "kb-f7-sqlite-read-fence"
	)
	scope := citationProfileTestScope(
		"scope-f7-sqlite-read-fence",
		subjectID,
		kbID,
		"epoch-f7-sqlite-read-fence",
		7,
		0,
	)
	require.NoError(t, readerDB.Create(scope).Error)
	require.NoError(t, readerDB.Create(
		citationProfileF1TestPage(
			"page-f7-sqlite-read-fence",
			scope,
			"knowledge-f7-sqlite-read-fence",
			time.Now().UTC(),
		),
	).Error)

	firstScopeRead := make(chan struct{})
	writerCommitted := make(chan struct{})
	callbackName := "p36_sqlite_read_fence_after_initial_scope"
	var once sync.Once
	require.NoError(t, readerDB.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (types.CitationProfileScope{}).TableName() {
			return
		}
		once.Do(func() {
			close(firstScopeRead)
			<-writerCommitted
		})
	}))
	t.Cleanup(func() { _ = readerDB.Callback().Query().Remove(callbackName) })

	type readResult struct {
		response *types.CitationProfileNodeListResponse
		err      error
	}
	readDone := make(chan readResult, 1)
	go func() {
		response, readErr := (&citationProfileRepository{db: readerDB}).ListNodes(
			context.Background(), tenantID, subjectID, kbID, nil, 20,
		)
		readDone <- readResult{response: response, err: readErr}
	}()

	select {
	case <-firstScopeRead:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not establish the initial SQLite snapshot")
	}
	writerErr := (&citationProfileRepository{db: writerDB}).InvalidateCitationProfileACL(
		context.Background(),
		types.CitationProfileACLMutation{
			PrincipalType: types.PrincipalWebUser,
			PrincipalID:   subjectID,
		},
	)
	close(writerCommitted)
	require.NoError(t, writerErr)

	select {
	case got := <-readDone:
		require.Nil(t, got.response)
		require.ErrorIs(t, got.err, types.ErrCitationProfileUnavailable,
			"a revocation committed during a SQLite read must be visible before data is returned")
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not finish after concurrent ACL invalidation")
	}
}

func TestCitationProfileSQLiteExportFenceObservesConcurrentACLInvalidation(t *testing.T) {
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "citation-profile-export-fence.db"))
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	readerDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	writerDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	for _, db := range []*gorm.DB{readerDB, writerDB} {
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
		require.NoError(t, db.Exec("PRAGMA busy_timeout=5000").Error)
	}
	require.NoError(t, readerDB.AutoMigrate(
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

	scope := citationProfileTestScope(
		"scope-f7-sqlite-export-fence",
		"user-f7-sqlite-export-fence",
		"kb-f7-sqlite-export-fence",
		"epoch-f7-sqlite-export-fence",
		7,
		0,
	)
	export := citationProfileACLTestExport(
		scope,
		"export-f7-sqlite-export-fence",
		types.CitationProfileOperationStatusReady,
		time.Now().UTC(),
	)
	require.NoError(t, readerDB.Create(scope).Error)
	require.NoError(t, readerDB.Create(&export).Error)

	firstScopeRead := make(chan struct{})
	writerCommitted := make(chan struct{})
	callbackName := "p36_sqlite_export_fence_after_initial_scope"
	var once sync.Once
	require.NoError(t, readerDB.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (types.CitationProfileScope{}).TableName() {
			return
		}
		once.Do(func() {
			close(firstScopeRead)
			<-writerCommitted
		})
	}))
	t.Cleanup(func() { _ = readerDB.Callback().Query().Remove(callbackName) })

	type exportResult struct {
		operation *types.CitationProfileOperation
		err       error
	}
	readDone := make(chan exportResult, 1)
	go func() {
		operation, readErr := (&citationProfileRepository{db: readerDB}).GetExportOperation(
			context.Background(),
			scope.TenantID,
			scope.SubjectID,
			scope.KnowledgeBaseID,
			export.ID,
		)
		readDone <- exportResult{operation: operation, err: readErr}
	}()

	select {
	case <-firstScopeRead:
	case <-time.After(10 * time.Second):
		t.Fatal("export reader did not establish the initial SQLite snapshot")
	}
	writerErr := (&citationProfileRepository{db: writerDB}).InvalidateCitationProfileACL(
		context.Background(),
		types.CitationProfileACLMutation{
			PrincipalType: types.PrincipalWebUser,
			PrincipalID:   scope.SubjectID,
		},
	)
	close(writerCommitted)
	require.NoError(t, writerErr)

	select {
	case got := <-readDone:
		require.Nil(t, got.operation)
		require.ErrorIs(t, got.err, types.ErrCitationProfileUnavailable,
			"an export read must observe a concurrent SQLite revocation before returning its payload")
	case <-time.After(10 * time.Second):
		t.Fatal("export reader did not finish after concurrent ACL invalidation")
	}
}

func TestCitationProfileSQLiteEnrollmentReplayFenceObservesConcurrentACLInvalidation(t *testing.T) {
	dbPath := filepath.ToSlash(filepath.Join(t.TempDir(), "citation-profile-enrollment-replay-fence.db"))
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	readerDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	writerDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	for _, db := range []*gorm.DB{readerDB, writerDB} {
		sqlDB, dbErr := db.DB()
		require.NoError(t, dbErr)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
		require.NoError(t, db.Exec("PRAGMA busy_timeout=5000").Error)
	}
	require.NoError(t, readerDB.AutoMigrate(
		&types.CitationProfileScope{},
		&types.CitationProfileEvent{},
		&types.CitationProfileEventOutbox{},
		&types.CitationProfileOperation{},
	))

	const (
		tenantID  = uint64(7)
		subjectID = "api_tenant_key:7:42"
		kbID      = "kb-f7-sqlite-enrollment-replay-fence"
		idemKey   = "f7-sqlite-enrollment-replay-fence"
	)
	scope := citationProfileTestScope(
		"scope-f7-sqlite-enrollment-replay-fence",
		subjectID,
		kbID,
		"epoch-f7-sqlite-enrollment-replay-fence",
		7,
		0,
	)
	scope.ACLPrincipalType = types.PrincipalAPITenant
	scope.ACLPrincipalID = "7"
	scope.ACLAuthenticatedTenantID = tenantID
	scope.ACLAPIKeyID = 42
	scope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
	scope.ACLAccessPathID = ""
	require.NoError(t, readerDB.Create(scope).Error)
	ctx := citationProfileEnrollmentF7Context(
		tenantID,
		types.Principal{Type: types.PrincipalAPITenant, ID: "7"},
		tenantID,
		42,
		types.CitationProfileACLAccessPathOwner,
		"",
	)
	expected := scope.ProfileReadVersion
	seeded, err := (&citationProfileRepository{db: readerDB}).SetEnrollment(
		ctx, tenantID, subjectID, kbID, true, &expected, idemKey,
	)
	require.NoError(t, err)
	require.NotNil(t, seeded)

	firstScopeRead := make(chan struct{})
	writerCommitted := make(chan struct{})
	callbackName := "p36_sqlite_enrollment_replay_fence_after_initial_scope"
	var once sync.Once
	require.NoError(t, readerDB.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != (types.CitationProfileScope{}).TableName() {
			return
		}
		once.Do(func() {
			close(firstScopeRead)
			<-writerCommitted
		})
	}))
	t.Cleanup(func() { _ = readerDB.Callback().Query().Remove(callbackName) })

	type enrollmentResult struct {
		scope *types.CitationProfileScope
		err   error
	}
	readDone := make(chan enrollmentResult, 1)
	go func() {
		got, readErr := (&citationProfileRepository{db: readerDB}).SetEnrollment(
			ctx, tenantID, subjectID, kbID, true, &expected, idemKey,
		)
		readDone <- enrollmentResult{scope: got, err: readErr}
	}()

	select {
	case <-firstScopeRead:
	case <-time.After(10 * time.Second):
		t.Fatal("enrollment replay did not establish the initial SQLite snapshot")
	}
	writerErr := (&citationProfileRepository{db: writerDB}).InvalidateCitationProfileACL(
		context.Background(),
		types.CitationProfileACLMutation{
			PrincipalType: types.PrincipalAPITenant,
			PrincipalID:   "7",
			APIKeyID:      42,
		},
	)
	close(writerCommitted)
	require.NoError(t, writerErr)

	select {
	case got := <-readDone:
		require.Nil(t, got.scope)
		require.ErrorIs(t, got.err, types.ErrCitationProfileUnavailable,
			"an idempotent enrollment replay must not return a business snapshot revoked on another SQLite connection")
	case <-time.After(10 * time.Second):
		t.Fatal("enrollment replay did not finish after concurrent ACL invalidation")
	}
}
