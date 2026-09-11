//go:build t4pg

package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	postgresmigrate "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/stretchr/testify/require"
)

func TestPostgresMigrationFailureLeavesDirtyStateWithoutAutoRecovery(t *testing.T) {
	dsn := newT4MigrationTestDatabase(t)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "versioned")
	require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "versioned"), migrationsRoot))
	require.NoError(t, os.WriteFile(
		filepath.Join(migrationsRoot, "999999_intentionally_broken.up.sql"),
		[]byte("CREATE TABLE intentionally_broken (id integer;\n"),
		0o600,
	))

	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(testRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	err = RunMigrationsWithOptions(dsn, MigrationOptions{AutoRecoverDirty: false})
	require.Error(t, err)

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, 999999, version)
	require.True(t, dirty)

	err = RunMigrationsWithOptions(dsn, MigrationOptions{AutoRecoverDirty: false})
	require.Error(t, err, "dirty state must block a retry when auto-recovery is disabled")
	var retryVersion int
	var retryDirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&retryVersion, &retryDirty))
	require.Equal(t, version, retryVersion)
	require.True(t, retryDirty)
}

func TestPostgresMigrationsSupportDownUpRoundTrip(t *testing.T) {
	dsn := newT4MigrationTestDatabase(t)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "versioned")
	require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "versioned"), migrationsRoot))
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(testRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver, err := postgresmigrate.WithInstance(db, &postgresmigrate.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://migrations/versioned", "postgres", driver)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	require.NoError(t, m.Migrate(92))

	version, dirty, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(92), version)
	require.False(t, dirty)
	assertPostgresTerminalIdentityIndexContract(t, db,
		"citation_profile_events", "uq_citation_profile_event_producer", []string{
			"tenant_id", "subject_id", "knowledge_base_id", "subject_epoch",
			"message_id", "origin_reference_index", "source_knowledge_id",
			"source_result_id", "COALESCE(source_chunk_index, '-1'::integer)",
		})
	assertPostgresTerminalIdentityIndexContract(t, db,
		"citation_profile_events", "uq_citation_profile_event_key", []string{
			"tenant_id", "subject_id", "knowledge_base_id", "subject_epoch", "producer_event_key",
		})
	assertPostgresTerminalIdentityIndexContract(t, db,
		"citation_profile_event_outbox", "uq_citation_profile_outbox_event", []string{"event_id"})
	require.False(t, postgresCitationProfileOutboxDueIndexExists(t, db),
		"version 92 must not have the version 93 due-work index")

	legacyAt := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)
	legacyNextCheckAt := legacyAt.Add(72 * time.Hour)
	legacyLeaseUntil := legacyAt.Add(10 * time.Minute)
	const (
		legacyScopeID      = "94000000-0000-4000-8000-000000000001"
		legacyKBID         = "94000000-0000-4000-8000-000000000002"
		legacyEpoch        = "94000000-0000-4000-8000-000000000003"
		pendingOutboxID    = "94000000-0000-4000-8000-000000000004"
		deliveredOutboxID  = "94000000-0000-4000-8000-000000000005"
		deadletterOutboxID = "94000000-0000-4000-8000-000000000006"
		mismatchedOutboxID = "94000000-0000-4000-8000-000000000007"
		pendingEventID     = "94000000-0000-4000-8000-000000000014"
		deliveredEventID   = "94000000-0000-4000-8000-000000000015"
		deadletterEventID  = "94000000-0000-4000-8000-000000000016"
		mismatchedEventID  = "94000000-0000-4000-8000-000000000017"
		legacyTenantID     = int64(94001)
		mismatchedTenantID = int64(94002)
		legacySubjectID    = "pre-94-subject"
	)
	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch,
			 profile_read_version, enabled, acl_check_state, next_acl_check_at,
			 acl_check_lease_until, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 11, TRUE, 'current', $6, $7, $8, $8)`,
		legacyScopeID, legacyTenantID, legacySubjectID, legacyKBID, legacyEpoch,
		legacyNextCheckAt, legacyLeaseUntil, legacyAt,
	)
	require.NoError(t, err)
	_, err = db.Exec(`
		INSERT INTO citation_profile_event_outbox
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
			 event_id, status, attempt_count, next_attempt_at, locked_at, locked_by,
			 delivered_at, deadletter_at, created_at, updated_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, 'delivering', 3, $8, $8, 'legacy-worker', NULL, NULL, $8, $8),
			($9, $2, $3, $4, $5, $6, $10, 'delivered', 3, $8, NULL, '', $8, NULL, $8, $8),
			($11, $2, $3, $4, $5, $6, $12, 'deadletter', 3, $8, NULL, '', NULL, $8, $8, $8),
			($13, $14, $3, $4, $5, $6, $15, 'delivering', 3, $8, $8, 'mismatched-worker', NULL, NULL, $8, $8)`,
		pendingOutboxID, legacyTenantID, legacySubjectID, legacyKBID, legacyEpoch,
		legacyScopeID, pendingEventID, legacyAt,
		deliveredOutboxID, deliveredEventID,
		deadletterOutboxID, deadletterEventID,
		mismatchedOutboxID, mismatchedTenantID, mismatchedEventID,
	)
	require.NoError(t, err)

	require.NoError(t, m.Steps(1))
	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(93), version)
	require.False(t, dirty)
	assertPostgresCitationProfileLegacyOutboxDueIndexContract(t, db)
	assertPostgresCitationProfileLegacyACLQueueIndexContract(t, db)
	require.NoError(t, m.Steps(-1))
	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(92), version)
	require.False(t, dirty)
	require.False(t, postgresCitationProfileOutboxDueIndexExists(t, db),
		"000093 down must remove the due-work index")
	require.NoError(t, m.Steps(1))
	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(93), version)
	require.False(t, dirty)
	assertPostgresCitationProfileLegacyOutboxDueIndexContract(t, db)
	assertPostgresCitationProfileLegacyACLQueueIndexContract(t, db)

	require.NoError(t, m.Steps(1))
	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(94), version)
	require.False(t, dirty)
	assertPostgresCitationProfileOutboxDueIndexContract(t, db)
	assertPostgresCitationProfileCorrectionIndexContract(t, db)
	assertPostgresCitationProfileF8QueueIndexContracts(t, db)
	assertPostgresMigration094LeavesNoPersistentRestoreRelations(t, db)

	var aclState string
	var aclGeneration, readVersion int64
	var suspendedNextCheckAt sql.NullTime
	var suspendedLeaseUntil sql.NullTime
	require.NoError(t, db.QueryRow(`
		SELECT acl_check_state, acl_generation, profile_read_version,
		       next_acl_check_at, acl_check_lease_until
		FROM citation_profile_scopes WHERE id = $1`, legacyScopeID,
	).Scan(&aclState, &aclGeneration, &readVersion, &suspendedNextCheckAt, &suspendedLeaseUntil))
	require.Equal(t, "unknown", aclState)
	require.Equal(t, int64(1), aclGeneration)
	require.Equal(t, int64(12), readVersion)
	require.True(t, suspendedNextCheckAt.Valid)
	require.False(t, suspendedLeaseUntil.Valid, "migration suspension must revoke a legacy ACL lease")

	var pendingPausedAt sql.NullTime
	var pendingStatus string
	var pendingLockedAt, pendingLeaseUntil sql.NullTime
	var pendingLockedBy string
	require.NoError(t, db.QueryRow(`
		SELECT retry_budget_paused_at, status, locked_at, lease_until, locked_by
		FROM citation_profile_event_outbox WHERE id = $1`, pendingOutboxID,
	).Scan(&pendingPausedAt, &pendingStatus, &pendingLockedAt, &pendingLeaseUntil, &pendingLockedBy))
	require.True(t, pendingPausedAt.Valid,
		"000094 must atomically pause every nonterminal outbox for a legacy scope made UNKNOWN")
	require.Equal(t, "pending", pendingStatus)
	require.False(t, pendingLockedAt.Valid)
	require.False(t, pendingLeaseUntil.Valid)
	require.Empty(t, pendingLockedBy)

	_, err = db.Exec(`
		INSERT INTO citation_profile_event_outbox
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
			 event_id, status, locked_at, locked_by)
		VALUES
			('94000000-0000-4000-8000-000000000022', $1, $2, $3, $4, $5,
			 '94000000-0000-4000-8000-000000000023', 'delivering', $6, 'legacy-worker')`,
		legacyTenantID, legacySubjectID, legacyKBID, legacyEpoch, legacyScopeID, legacyAt,
	)
	require.Error(t, err,
		"a rolling old PostgreSQL writer must fail closed instead of creating delivering work without a deadline")

	_, err = db.Exec(`
		INSERT INTO citation_profile_event_outbox
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
			 event_id, status, lease_until)
		VALUES
			('94000000-0000-4000-8000-000000000018', $1, $2, $3, $4, $5,
			 '94000000-0000-4000-8000-000000000019', 'pending', $6)`,
		legacyTenantID, legacySubjectID, legacyKBID, legacyEpoch, legacyScopeID, legacyNextCheckAt,
	)
	require.Error(t, err, "pending PostgreSQL work must not retain a delivering lease deadline")
	_, err = db.Exec(`
		INSERT INTO citation_profile_event_outbox
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
			 event_id, status, locked_at, lease_until, locked_by)
		VALUES
			('94000000-0000-4000-8000-000000000020', $1, $2, $3, $4, $5,
			 '94000000-0000-4000-8000-000000000021', 'delivering', $6, $7, 'worker-94')`,
		legacyTenantID, legacySubjectID, legacyKBID, legacyEpoch, legacyScopeID, legacyAt, legacyNextCheckAt,
	)
	require.NoError(t, err, "live delivering PostgreSQL work may carry its durable deadline")
	_, err = db.Exec(`
		UPDATE citation_profile_event_outbox
		SET status = 'pending'
		WHERE id = '94000000-0000-4000-8000-000000000020'`)
	require.Error(t, err, "retry must clear the PostgreSQL deadline atomically with status")

	for _, terminal := range []struct {
		id             string
		status         string
		wantDelivered  bool
		wantDeadletter bool
	}{
		{id: deliveredOutboxID, status: "delivered", wantDelivered: true},
		{id: deadletterOutboxID, status: "deadletter", wantDeadletter: true},
	} {
		var terminalStatus string
		var terminalPausedAt sql.NullTime
		var deliveredAt sql.NullTime
		var deadletterAt sql.NullTime
		require.NoError(t, db.QueryRow(`
			SELECT status, retry_budget_paused_at, delivered_at, deadletter_at
			FROM citation_profile_event_outbox WHERE id = $1`, terminal.id,
		).Scan(&terminalStatus, &terminalPausedAt, &deliveredAt, &deadletterAt))
		require.Equal(t, terminal.status, terminalStatus)
		require.Equal(t, terminal.wantDelivered, deliveredAt.Valid)
		require.Equal(t, terminal.wantDeadletter, deadletterAt.Valid)
		require.False(t, terminalPausedAt.Valid, "terminal outbox %s must remain untouched", terminal.id)
	}

	var mismatchedPausedAt sql.NullTime
	var mismatchedStatus, mismatchedLockedBy string
	var mismatchedLockedAt, mismatchedLeaseUntil sql.NullTime
	require.NoError(t, db.QueryRow(`
		SELECT retry_budget_paused_at, status, locked_at, lease_until, locked_by
		FROM citation_profile_event_outbox WHERE id = $1`, mismatchedOutboxID,
	).Scan(&mismatchedPausedAt, &mismatchedStatus, &mismatchedLockedAt, &mismatchedLeaseUntil, &mismatchedLockedBy))
	require.False(t, mismatchedPausedAt.Valid,
		"migration must use the complete scope identity, not scope_id alone")
	require.Equal(t, "pending", mismatchedStatus,
		"every legacy delivering row must become safely reclaimable before the lease invariant is enforced")
	require.False(t, mismatchedLockedAt.Valid)
	require.False(t, mismatchedLeaseUntil.Valid)
	require.Empty(t, mismatchedLockedBy)

	var aclStateDefault string
	require.NoError(t, db.QueryRow(`
		SELECT column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'citation_profile_scopes'
		  AND column_name = 'acl_check_state'`,
	).Scan(&aclStateDefault))
	require.Contains(t, aclStateDefault, "unknown",
		"new and rolling-upgrade writers must default to fail-closed UNKNOWN")

	const defaultUnknownScopeID = "94000000-0000-4000-8000-000000000008"
	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch)
		VALUES ($1, $2, 'post-94-default', $3, $4)`,
		defaultUnknownScopeID, legacyTenantID, legacyKBID, legacyEpoch,
	)
	require.NoError(t, err)
	var defaultState string
	require.NoError(t, db.QueryRow(
		"SELECT acl_check_state FROM citation_profile_scopes WHERE id = $1",
		defaultUnknownScopeID,
	).Scan(&defaultState))
	require.Equal(t, "unknown", defaultState)

	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state)
		VALUES ('94000000-0000-4000-8000-000000000009', $1, 'old-writer-current', $2, $3, 'current')`,
		legacyTenantID, legacyKBID, legacyEpoch,
	)
	require.Error(t, err,
		"an old rolling-upgrade writer must not create bindingless CURRENT authority")

	const zeroGenerationScopeID = "94000000-0000-4000-8000-000000000010"
	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state,
			 acl_checked_at, next_acl_check_at, acl_principal_type, acl_principal_id,
			 acl_authenticated_tenant_id, acl_api_key_id, acl_access_path, acl_access_path_id)
		VALUES ($1, $2, 'post-94-valid-current', $3, $4, 'current',
		        $5, $6, 'web_user', 'user-post-94', $2, 0, 'owner', '')`,
		zeroGenerationScopeID, legacyTenantID, legacyKBID, legacyEpoch, legacyAt, legacyNextCheckAt,
	)
	require.Error(t, err, "CURRENT must carry a positive mutation fence generation")

	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state,
			 acl_checked_at, next_acl_check_at, acl_principal_type, acl_principal_id,
			 acl_authenticated_tenant_id, acl_api_key_id, acl_access_path, acl_access_path_id,
			 acl_generation)
		VALUES ('94000000-0000-4000-8000-000000000011', $1, 'post-94-owner-mismatch', $2, $3, 'current',
		        $4, $5, 'web_user', 'user-post-94', $1 + 1, 0, 'owner', '', 1)`,
		legacyTenantID, legacyKBID, legacyEpoch, legacyAt, legacyNextCheckAt,
	)
	require.Error(t, err, "owner CURRENT must bind the authenticated tenant to the source tenant")

	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state,
			 acl_check_lease_token, acl_check_lease_until, acl_checked_at, next_acl_check_at,
			 acl_principal_type, acl_principal_id, acl_authenticated_tenant_id,
			 acl_api_key_id, acl_access_path, acl_access_path_id, acl_generation)
		VALUES ('94000000-0000-4000-8000-000000000012', $1, 'post-94-current-with-lease', $2, $3, 'current',
		        'stale-lease', $5, $4, $5, 'web_user', 'user-post-94', $1, 0, 'owner', '', 1)`,
		legacyTenantID, legacyKBID, legacyEpoch, legacyAt, legacyNextCheckAt,
	)
	require.Error(t, err, "CURRENT must not coexist with an in-flight ACL authority lease")

	const validCurrentScopeID = "94000000-0000-4000-8000-000000000013"
	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state,
			 acl_checked_at, next_acl_check_at, acl_principal_type, acl_principal_id,
			 acl_authenticated_tenant_id, acl_api_key_id, acl_access_path, acl_access_path_id,
			 acl_generation)
		VALUES ($1, $2, 'post-94-valid-current', $3, $4, 'current',
		        $5, $6, 'web_user', 'user-post-94', $2, 0, 'owner', '', 1)`,
		validCurrentScopeID, legacyTenantID, legacyKBID, legacyEpoch, legacyAt, legacyNextCheckAt,
	)
	require.NoError(t, err, "a complete fresh server-derived binding with a generation may be CURRENT")
	_, err = db.Exec("DELETE FROM citation_profile_scopes WHERE id IN ($1, $2)", defaultUnknownScopeID, validCurrentScopeID)
	require.NoError(t, err)

	err = m.Steps(-1)
	require.Error(t, err, "000094 down must always refuse irreversible authority-state loss")
	require.ErrorContains(t, err, "migration 000094 down refused: irreversible ACL authority migration")
	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(93), version)
	require.True(t, dirty, "a refused 000094 down migration must remain operator-diagnosable")

	var preservedState string
	var preservedGeneration, preservedReadVersion int64
	var preservedNextCheckAt sql.NullTime
	var preservedLeaseUntil sql.NullTime
	require.NoError(t, db.QueryRow(`
		SELECT acl_check_state, acl_generation, profile_read_version,
		       next_acl_check_at, acl_check_lease_until
		FROM citation_profile_scopes WHERE id = $1`, legacyScopeID,
	).Scan(&preservedState, &preservedGeneration, &preservedReadVersion, &preservedNextCheckAt, &preservedLeaseUntil))
	require.Equal(t, "unknown", preservedState)
	require.Equal(t, int64(1), preservedGeneration)
	require.Equal(t, int64(12), preservedReadVersion)
	require.True(t, preservedNextCheckAt.Valid)
	require.False(t, preservedLeaseUntil.Valid,
		"refused rollback must not revive a pre-migration authority lease")

	var preservedOutboxStatus, preservedOutboxLockedBy string
	var preservedOutboxPausedAt sql.NullTime
	var preservedOutboxLockedAt sql.NullTime
	require.NoError(t, db.QueryRow(`
		SELECT status, retry_budget_paused_at, locked_at, locked_by
		FROM citation_profile_event_outbox WHERE id = $1`, pendingOutboxID,
	).Scan(&preservedOutboxStatus, &preservedOutboxPausedAt, &preservedOutboxLockedAt, &preservedOutboxLockedBy))
	require.Equal(t, "pending", preservedOutboxStatus)
	require.True(t, preservedOutboxPausedAt.Valid)
	require.False(t, preservedOutboxLockedAt.Valid)
	require.Empty(t, preservedOutboxLockedBy)
}

func TestPostgresMigration094RollbackRefusesPostMigrationAuthorityData(t *testing.T) {
	dsn := newT4MigrationTestDatabase(t)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "versioned")
	require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "versioned"), migrationsRoot))
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(testRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{AutoRecoverDirty: false}))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	checkedAt := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	nextCheckAt := checkedAt.Add(time.Hour)
	_, err = db.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch,
			 acl_check_state, acl_checked_at, next_acl_check_at,
			 acl_principal_type, acl_principal_id, acl_authenticated_tenant_id,
			 acl_api_key_id, acl_access_path, acl_access_path_id, acl_generation)
		VALUES
			('94000000-0000-4000-8000-000000000101', 94101,
			 'post-94-guard-subject', '94000000-0000-4000-8000-000000000102',
			 '94000000-0000-4000-8000-000000000103', 'current', $1, $2,
			 'web_user', 'post-94-guard-subject', 94101, 0, 'owner', '', 1)`,
		checkedAt, nextCheckAt,
	)
	require.NoError(t, err)

	driver, err := postgresmigrate.WithInstance(db, &postgresmigrate.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://migrations/versioned", "postgres", driver)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })

	err = m.Steps(-1)
	require.Error(t, err, "000094 down must refuse to erase post-migration authority data")
	require.ErrorContains(t, err, "migration 000094 down refused: irreversible ACL authority migration")

	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	// golang-migrate records the target version before executing a down
	// migration, so a refused 94 -> 93 rollback is diagnosed as 93/dirty.
	require.Equal(t, 93, version)
	require.True(t, dirty, "a refused guarded rollback must remain operator-diagnosable")
}

func TestPostgresMigration094DownAlwaysRefusesWithoutApplicationRows(t *testing.T) {
	dsn := newT4MigrationTestDatabase(t)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "versioned")
	require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "versioned"), migrationsRoot))
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(testRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{AutoRecoverDirty: false}))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	assertPostgresMigration094LeavesNoPersistentRestoreRelations(t, db)

	var scopeCount int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM citation_profile_scopes").Scan(&scopeCount))
	require.Zero(t, scopeCount, "fixture must prove rollback refusal does not depend on application rows")

	driver, err := postgresmigrate.WithInstance(db, &postgresmigrate.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://migrations/versioned", "postgres", driver)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })

	err = m.Steps(-1)
	require.Error(t, err, "000094 down must refuse even when no application rows currently exist")
	require.ErrorContains(t, err, "migration 000094 down refused: irreversible ACL authority migration")

	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, 93, version)
	require.True(t, dirty)
	var aclGenerationColumnCount int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*)
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'citation_profile_scopes'
		  AND column_name = 'acl_generation'`).Scan(&aclGenerationColumnCount))
	require.Equal(t, 1, aclGenerationColumnCount,
		"refusal must leave the authority schema intact for forward recovery")
}

func TestPostgresMigration094RejectsPreexistingACLColumnDrift(t *testing.T) {
	dsn := newT4MigrationTestDatabase(t)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "versioned")
	require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "versioned"), migrationsRoot))
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(testRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	driver, err := postgresmigrate.WithInstance(db, &postgresmigrate.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://migrations/versioned", "postgres", driver)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })
	require.NoError(t, m.Migrate(93))

	_, err = db.Exec(`
		ALTER TABLE citation_profile_scopes
		ADD COLUMN acl_check_lease_token TEXT NOT NULL DEFAULT ''`)
	require.NoError(t, err)

	err = m.Steps(1)
	require.Error(t, err, "000094 must fail closed instead of accepting a preexisting incompatible ACL column")
	require.ErrorContains(t, err, "already exists")

	var version int
	var dirty bool
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	require.Equal(t, 94, version)
	require.True(t, dirty, "schema drift must leave migration 000094 dirty and diagnosable")
}

func TestPostgresMigration092RejectsPreexistingV2IndexDrift(t *testing.T) {
	testCases := []struct {
		name string
		sql  string
	}{
		{
			name: "non_unique",
			sql: `CREATE INDEX uq_citation_profile_event_producer_v2
				ON citation_profile_events (
					tenant_id, subject_id, knowledge_base_id, subject_epoch,
					message_id, origin_reference_index, source_knowledge_id,
					source_result_id, COALESCE(source_chunk_index, -1)
				)`,
		},
		{
			name: "wrong_table",
			sql: `CREATE UNIQUE INDEX uq_citation_profile_event_producer_v2
				ON citation_profile_event_outbox (event_id)`,
		},
		{
			name: "partial",
			sql: `CREATE UNIQUE INDEX uq_citation_profile_event_producer_v2
				ON citation_profile_events (
					tenant_id, subject_id, knowledge_base_id, subject_epoch,
					message_id, origin_reference_index, source_knowledge_id,
					source_result_id, COALESCE(source_chunk_index, -1)
				) WHERE source_chunk_index IS NOT NULL`,
		},
		{
			name: "include_instead_of_key",
			sql: `CREATE UNIQUE INDEX uq_citation_profile_event_producer_v2
				ON citation_profile_events (
					tenant_id, subject_id, knowledge_base_id, subject_epoch,
					message_id, origin_reference_index, source_knowledge_id,
					source_result_id
				) INCLUDE (source_chunk_index)`,
		},
		{
			name: "wrong_expression",
			sql: `CREATE UNIQUE INDEX uq_citation_profile_event_producer_v2
				ON citation_profile_events (
					tenant_id, subject_id, knowledge_base_id, subject_epoch,
					message_id, origin_reference_index, source_knowledge_id,
					source_result_id, COALESCE(source_chunk_index, -2)
				)`,
		},
		{
			name: "deferrable_event_key_constraint",
			sql: `ALTER TABLE citation_profile_events
				ADD CONSTRAINT uq_citation_profile_event_key_v2
				UNIQUE (tenant_id, subject_id, knowledge_base_id, subject_epoch, producer_event_key)
				DEFERRABLE INITIALLY IMMEDIATE`,
		},
		{
			name: "deferrable_outbox_event_constraint",
			sql: `ALTER TABLE citation_profile_event_outbox
				ADD CONSTRAINT uq_citation_profile_outbox_event_v2
				UNIQUE (event_id)
				DEFERRABLE INITIALLY IMMEDIATE`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			dsn := newT4MigrationTestDatabase(t)
			repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
			require.NoError(t, err)
			testRoot := t.TempDir()
			migrationsRoot := filepath.Join(testRoot, "migrations", "versioned")
			require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "versioned"), migrationsRoot))
			previousDir, err := os.Getwd()
			require.NoError(t, err)
			require.NoError(t, os.Chdir(testRoot))
			t.Cleanup(func() { _ = os.Chdir(previousDir) })

			db, err := sql.Open("postgres", dsn)
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			driver, err := postgresmigrate.WithInstance(db, &postgresmigrate.Config{})
			require.NoError(t, err)
			m, err := migrate.NewWithDatabaseInstance("file://migrations/versioned", "postgres", driver)
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = m.Close() })
			require.NoError(t, m.Migrate(91))
			_, err = db.Exec(testCase.sql)
			require.NoError(t, err)

			err = m.Steps(1)
			require.Error(t, err, "000092 must fail closed instead of promoting a drifted staging index")

			var version int
			var dirty bool
			require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
			require.Equal(t, 92, version)
			require.True(t, dirty, "staging-index drift must remain operator-diagnosable")

			var legacyIndexCount int
			require.NoError(t, db.QueryRow(`
				SELECT COUNT(*)
				FROM pg_class AS relation
				JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
				WHERE namespace.nspname = current_schema()
				  AND relation.relname = 'uq_citation_profile_event_producer'`).Scan(&legacyIndexCount))
			require.Equal(t, 1, legacyIndexCount,
				"validation must fail before dropping the deployed identity index")
		})
	}
}

func assertPostgresCitationProfileOutboxDueIndexContract(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`
		SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		CROSS JOIN LATERAL generate_series(1, index_meta.indnkeyatts) AS key_positions(key_position)
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2
		ORDER BY key_position`,
		citationProfileOutboxDueIndexTable,
		citationProfileOutboxDueIndexName,
	)
	require.NoError(t, err)
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, citationProfileOutboxDueIndexColumns, columns)

	var predicate string
	require.NoError(t, db.QueryRow(`
		SELECT pg_get_expr(index_meta.indpred, index_meta.indrelid, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2`,
		citationProfileOutboxDueIndexTable,
		citationProfileOutboxDueIndexName,
	).Scan(&predicate))
	require.Equal(t, citationProfileOutboxDueIndexPredicate, normalizeIndexPredicate(predicate))
}

func assertPostgresTerminalIdentityIndexContract(
	t *testing.T,
	db *sql.DB,
	tableName string,
	indexName string,
	wantKeys []string,
) {
	t.Helper()
	var (
		actualTable                                                             string
		accessMethod                                                            string
		unique, valid, ready, live, immediate, primary, exclusion, hasPredicate bool
		keyAttributeCount, totalAttributeCount                                  int
	)
	require.NoError(t, db.QueryRow(`
		SELECT table_relation.relname,
		       access_method.amname,
		       index_meta.indisunique,
		       index_meta.indisvalid,
		       index_meta.indisready,
		       index_meta.indislive,
		       index_meta.indimmediate,
		       index_meta.indisprimary,
		       index_meta.indisexclusion,
		       index_meta.indpred IS NOT NULL,
		       index_meta.indnkeyatts,
		       index_meta.indnatts
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_namespace AS index_namespace ON index_namespace.oid = index_relation.relnamespace
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		JOIN pg_am AS access_method ON access_method.oid = index_relation.relam
		WHERE index_namespace.nspname = current_schema()
		  AND table_namespace.nspname = current_schema()
		  AND index_relation.relname = $1`, indexName).Scan(
		&actualTable,
		&accessMethod,
		&unique,
		&valid,
		&ready,
		&live,
		&immediate,
		&primary,
		&exclusion,
		&hasPredicate,
		&keyAttributeCount,
		&totalAttributeCount,
	))
	require.Equal(t, tableName, actualTable)
	require.Equal(t, "btree", accessMethod)
	require.True(t, unique)
	require.True(t, valid)
	require.True(t, ready)
	require.True(t, live)
	require.True(t, immediate)
	require.False(t, primary)
	require.False(t, exclusion)
	require.False(t, hasPredicate)
	require.Equal(t, len(wantKeys), keyAttributeCount)
	require.Equal(t, len(wantKeys), totalAttributeCount, "terminal identity indexes must not hide INCLUDE columns")

	rows, err := db.Query(`
		SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_namespace AS index_namespace ON index_namespace.oid = index_relation.relnamespace
		CROSS JOIN LATERAL generate_series(1, index_meta.indnatts) AS positions(key_position)
		WHERE index_namespace.nspname = current_schema()
		  AND index_relation.relname = $1
		ORDER BY key_position`, indexName)
	require.NoError(t, err)
	defer rows.Close()
	actualKeys := make([]string, 0, len(wantKeys))
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		actualKeys = append(actualKeys, key)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, wantKeys, actualKeys)
}

func assertPostgresCitationProfileLegacyOutboxDueIndexContract(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`
		SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		CROSS JOIN LATERAL generate_series(1, index_meta.indnkeyatts) AS key_positions(key_position)
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2
		ORDER BY key_position`,
		citationProfileOutboxDueIndexTable,
		citationProfileOutboxDueIndexName,
	)
	require.NoError(t, err)
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, citationProfileLegacyOutboxDueIndexColumns, columns,
		"version 93 must retain the deployed locked_at index until 000094 upgrades it")
}

func assertPostgresCitationProfileCorrectionIndexContract(t *testing.T, db *sql.DB) {
	t.Helper()
	var tableName string
	var unique bool
	var predicate string
	require.NoError(t, db.QueryRow(`
		SELECT table_relation.relname,
		       index_meta.indisunique,
		       pg_get_expr(index_meta.indpred, index_meta.indrelid, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		WHERE table_namespace.nspname = current_schema()
		  AND index_relation.relname = $1`,
		citationProfileCorrectionIdemIndexName,
	).Scan(&tableName, &unique, &predicate))
	require.Equal(t, citationProfileCorrectionIdemTable, tableName)
	require.True(t, unique)

	rows, err := db.Query(`
		SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_namespace AS table_namespace
		  ON table_namespace.oid = index_relation.relnamespace
		CROSS JOIN LATERAL generate_series(1, index_meta.indnkeyatts) AS key_positions(key_position)
		WHERE table_namespace.nspname = current_schema()
		  AND index_relation.relname = $1
		ORDER BY key_position`,
		citationProfileCorrectionIdemIndexName,
	)
	require.NoError(t, err)
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, citationProfileCorrectionIdemColumns, columns)
	require.Equal(
		t,
		citationProfileCorrectionIdemPredicate,
		normalizePostgresIndexPredicate(predicate),
	)
}

func assertPostgresCitationProfileF8QueueIndexContracts(t *testing.T, db *sql.DB) {
	t.Helper()
	assertPostgresPartialIndexContract(
		t, db,
		citationProfileActiveRunIndexName,
		citationProfileActiveRunIndexTable,
		citationProfileActiveRunColumns,
		citationProfileActiveRunPredicate,
	)
	assertPostgresPartialIndexContract(
		t, db,
		citationProfileOperationsIndexName,
		citationProfileOperationsIndexTable,
		citationProfileOperationsColumns,
		citationProfileOperationsPredicate,
	)

	rows, err := db.Query(`
		SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		CROSS JOIN LATERAL generate_series(1, index_meta.indnkeyatts) AS key_positions(key_position)
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2
		ORDER BY key_position`,
		citationProfileACLQueueIndexTable,
		citationProfileACLQueueIndexName,
	)
	require.NoError(t, err)
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, normalizePostgresIndexPredicate(key))
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{
		"case when next_acl_check_at is null then 0 else 1 end",
		"next_acl_check_at",
		"tenant_id",
		"id",
	}, keys, "PostgreSQL ACL claim index keys must exactly match the fair ORDER BY")

	var predicate string
	require.NoError(t, db.QueryRow(`
		SELECT pg_get_expr(index_meta.indpred, index_meta.indrelid, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2`,
		citationProfileACLQueueIndexTable,
		citationProfileACLQueueIndexName,
	).Scan(&predicate))
	require.Equal(t, citationProfileACLQueuePGPredicate, normalizePostgresIndexPredicate(predicate))

	tx, err := db.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec("SET LOCAL enable_seqscan = off")
	require.NoError(t, err)
	planRows, err := tx.Query(`
		EXPLAIN (COSTS OFF)
		SELECT id
		FROM citation_profile_scopes
		WHERE enabled = $2
		  AND deleted_at IS NULL
		  AND fenced_at IS NULL
		  AND (next_acl_check_at IS NULL OR next_acl_check_at <= $1)
		  AND (acl_check_lease_until IS NULL OR acl_check_lease_until <= $1)
		ORDER BY CASE WHEN next_acl_check_at IS NULL THEN 0 ELSE 1 END ASC,
		         next_acl_check_at ASC,
		         tenant_id ASC,
		         id ASC
		LIMIT 32`, time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC), true)
	require.NoError(t, err)
	defer planRows.Close()
	var planLines []string
	for planRows.Next() {
		var line string
		require.NoError(t, planRows.Scan(&line))
		planLines = append(planLines, line)
	}
	require.NoError(t, planRows.Err())
	plan := strings.ToLower(strings.Join(planLines, "\n"))
	require.Contains(t, plan, "using "+citationProfileACLQueueIndexName,
		"PostgreSQL fair claim must use the dedicated ordered partial index")
	require.NotContains(t, plan, "sort key:",
		"the fair claim index must satisfy ORDER BY without an unbounded sort")
}

func assertPostgresCitationProfileLegacyACLQueueIndexContract(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`
		SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		CROSS JOIN LATERAL generate_series(1, index_meta.indnkeyatts) AS key_positions(key_position)
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2
		ORDER BY key_position`,
		citationProfileACLQueueIndexTable,
		citationProfileACLQueueIndexName,
	)
	require.NoError(t, err)
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"acl_check_state", "next_acl_check_at", "acl_check_lease_until"}, keys,
		"versions 91-93 must retain the deployed index shape so 000094 proves the upgrade")

	var predicate string
	require.NoError(t, db.QueryRow(`
		SELECT pg_get_expr(index_meta.indpred, index_meta.indrelid, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = index_relation.relnamespace
		WHERE table_namespace.nspname = current_schema()
		  AND index_relation.relname = $1`, citationProfileACLQueueIndexName).Scan(&predicate))
	require.Equal(t, "deleted_at is null and fenced_at is null", normalizePostgresIndexPredicate(predicate))
}

func assertPostgresPartialIndexContract(
	t *testing.T,
	db *sql.DB,
	indexName string,
	tableName string,
	wantColumns []string,
	wantPredicate string,
) {
	t.Helper()
	rows, err := db.Query(`
		SELECT pg_get_indexdef(index_meta.indexrelid, key_position, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		CROSS JOIN LATERAL generate_series(1, index_meta.indnkeyatts) AS key_positions(key_position)
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2
		ORDER BY key_position`, tableName, indexName)
	require.NoError(t, err)
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, wantColumns, columns)

	var predicate string
	require.NoError(t, db.QueryRow(`
		SELECT pg_get_expr(index_meta.indpred, index_meta.indrelid, TRUE)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = index_relation.relnamespace
		WHERE table_namespace.nspname = current_schema()
		  AND index_relation.relname = $1`, indexName).Scan(&predicate))
	require.Equal(t, wantPredicate, normalizePostgresIndexPredicate(predicate))
}

func assertPostgresMigration094LeavesNoPersistentRestoreRelations(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_class AS relation
		JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
		  AND relation.relname IN (
			'citation_profile_acl_sync_v94_scope_restore',
			'citation_profile_acl_sync_v94_outbox_restore'
		  )`).Scan(&count))
	require.Zero(t, count,
		"migration 000094 must not persist shadow copies of citation-profile principal or evidence data")
}

func normalizePostgresIndexPredicate(predicate string) string {
	normalized := normalizeIndexPredicate(predicate)
	normalized = strings.ReplaceAll(normalized, "::character varying", "")
	normalized = strings.ReplaceAll(normalized, "::text", "")
	return normalizeIndexPredicate(normalized)
}

func postgresCitationProfileOutboxDueIndexExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*)
		FROM pg_index AS index_meta
		JOIN pg_class AS index_relation ON index_relation.oid = index_meta.indexrelid
		JOIN pg_class AS table_relation ON table_relation.oid = index_meta.indrelid
		JOIN pg_namespace AS table_namespace ON table_namespace.oid = table_relation.relnamespace
		WHERE table_namespace.nspname = current_schema()
		  AND table_relation.relname = $1
		  AND index_relation.relname = $2`,
		citationProfileOutboxDueIndexTable,
		citationProfileOutboxDueIndexName,
	).Scan(&count))
	return count == 1
}

func newT4MigrationTestDatabase(t *testing.T) string {
	t.Helper()
	baseDSN := strings.TrimSpace(os.Getenv("T4_MIGRATION_URL"))
	if baseDSN == "" {
		t.Skip("T4_MIGRATION_URL is required for isolated PostgreSQL migration verification")
	}

	baseURL, err := url.Parse(baseDSN)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, strings.ToLower(baseURL.Scheme))
	require.NotEmpty(t, baseURL.Host, "T4_MIGRATION_URL must be a PostgreSQL URL")

	databaseName := fmt.Sprintf("weknora_t4_migration_%d_%d", os.Getpid(), time.Now().UnixNano())
	quotedDatabaseName := quotePostgresIdentifier(databaseName)
	adminDB, err := sql.Open("postgres", baseDSN)
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := adminDB.Close(); closeErr != nil {
			t.Errorf("close PostgreSQL migration admin connection: %v", closeErr)
		}
	})
	require.NoError(t, adminDB.Ping())
	_, err = adminDB.Exec("CREATE DATABASE " + quotedDatabaseName)
	require.NoErrorf(t, err, "create isolated PostgreSQL migration database %s", databaseName)
	t.Cleanup(func() {
		if _, terminateErr := adminDB.Exec(`
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname = $1 AND pid <> pg_backend_pid()`, databaseName); terminateErr != nil {
			t.Errorf("terminate connections to PostgreSQL migration database %s: %v", databaseName, terminateErr)
		}
		if _, dropErr := adminDB.Exec("DROP DATABASE " + quotedDatabaseName); dropErr != nil {
			t.Errorf("drop isolated PostgreSQL migration database %s: %v", databaseName, dropErr)
		}
	})

	isolatedURL := *baseURL
	isolatedURL.Path = "/" + databaseName
	isolatedURL.RawPath = ""
	return isolatedURL.String()
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
