package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	sqlite3migrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/stretchr/testify/require"
)

// versionedSQLiteTables is the set of tables that SQLite migrations must
// create to stay in sync with the versioned (PostgreSQL) migrations:
// 000041 task queue, 000053 system settings, 000055 processing spans,
// 000063 knowledge multi-tags.
var versionedSQLiteTables = []string{
	"task_pending_ops",
	"task_dead_letters",
	"system_settings",
	"knowledge_processing_spans",
	"knowledge_tag_relations",
	"citation_profile_scopes",
	"citation_profile_acl_runtime_state",
	"citation_profile_events",
	"citation_profile_event_outbox",
	"wiki_source_ref_index",
	"evidence_resolution_runs",
	"evidence_node_links",
	"citation_profile_corrections",
	"citation_profile_operations",
}

// versionedSQLiteColumns maps each existing table to the columns that the
// versioned migrations add and the SQLite baseline was missing.
var versionedSQLiteColumns = map[string][]string{
	"tenants":            {"api_principal_config"},           // 000064
	"users":              {"is_system_admin"},                // 000053
	"knowledges":         {"pending_subtasks_count"},         // 000056
	"messages":           {"attachments", "usage"},           // 000034, 000085
	"tenant_invitations": {"token", "accepted_count"},        // 000054
	"embed_channels":     {"allow_memory"},                   // 000060
	"mcp_oauth_tokens":   {"principal_type", "principal_id"}, // 000064
}

const expectedSQLiteMigrationVersion = 13

const (
	citationProfileOutboxDueIndexName      = "idx_citation_profile_outbox_due"
	citationProfileOutboxDueIndexTable     = "citation_profile_event_outbox"
	citationProfileOutboxDueIndexPredicate = "delivered_at is null and deadletter_at is null"
	citationProfileCorrectionIdemIndexName = "uq_citation_profile_correction_idem"
	citationProfileCorrectionIdemTable     = "citation_profile_corrections"
	citationProfileCorrectionIdemPredicate = "idempotency_key <> ''"
	citationProfileACLQueueIndexName       = "idx_citation_profile_scopes_acl_queue"
	citationProfileACLQueueIndexTable      = "citation_profile_scopes"
	citationProfileACLQueueSQLitePredicate = "enabled = 1 and deleted_at is null and fenced_at is null"
	citationProfileACLQueuePGPredicate     = "enabled = true and deleted_at is null and fenced_at is null"
	citationProfileActiveRunIndexName      = "idx_citation_profile_events_active_run"
	citationProfileActiveRunIndexTable     = "citation_profile_events"
	citationProfileActiveRunPredicate      = "active_run_id is not null"
	citationProfileOperationsIndexName     = "idx_citation_profile_operations_pending"
	citationProfileOperationsIndexTable    = "citation_profile_operations"
	citationProfileOperationsPredicate     = "completed_at is null"
	citationProfileDownRefusalMarker       = "migration_000013_down_refused_citation_profile_schema_contains_user_data"
)

var citationProfileOutboxDueIndexColumns = []string{
	"status",
	"next_attempt_at",
	"lease_until",
	"created_at",
	"id",
}

var citationProfileLegacyOutboxDueIndexColumns = []string{
	"status",
	"next_attempt_at",
	"locked_at",
	"created_at",
	"id",
}

var citationProfileCorrectionIdemColumns = []string{
	"tenant_id",
	"subject_id",
	"knowledge_base_id",
	"subject_epoch",
	"idempotency_key",
}

var citationProfileActiveRunColumns = []string{"active_run_id"}

var citationProfileOperationsColumns = []string{
	"operation_type",
	"status",
	"next_attempt_at",
	"lease_until",
}

func TestSQLiteMigrationsCreateVersionedSchema(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	chdirAndRestore(t, repoRoot)

	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	db := openSQLiteDB(t, dbPath)
	version, dirty := sqliteMigrationState(t, db)
	require.Equal(t, expectedSQLiteMigrationVersion, version)
	require.False(t, dirty)

	for _, table := range versionedSQLiteTables {
		require.Truef(t, sqliteTableExists(t, db, table), "SQLite migrations must create table %s", table)
	}
	for table, columns := range versionedSQLiteColumns {
		for _, column := range columns {
			require.Truef(
				t,
				sqliteColumnExists(t, db, table, column),
				"SQLite migrations must add column %s.%s",
				table,
				column,
			)
		}
	}
	assertSQLiteCitationProfileACLRuntimeStateContract(t, db)

	assertSQLiteShareLinkInvitationsWork(t, db)
	assertSQLiteMCPOAuthPrincipalUpsertWorks(t, db)
	require.False(t, sqliteColumnExists(t, db, "knowledges", "tag_id"),
		"SQLite migrations must drop legacy knowledges.tag_id after multi-tag migration")
}

func assertSQLiteCitationProfileACLRuntimeStateContract(t *testing.T, db *sql.DB) {
	t.Helper()
	var tableSQL string
	require.NoError(t, db.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'citation_profile_acl_runtime_state'",
	).Scan(&tableSQL))
	normalized := strings.ToLower(strings.Join(strings.Fields(tableSQL), " "))
	for _, contract := range []string{
		"id integer primary key check (id = 1)",
		"enabled integer not null check (enabled in (0, 1))",
		"transition_generation integer not null check (transition_generation >= 0)",
		"changed_at datetime not null",
		"updated_at datetime not null",
	} {
		require.Contains(t, normalized, contract)
	}
	var enabled int
	var generation int64
	require.NoError(t, db.QueryRow(
		"SELECT enabled, transition_generation FROM citation_profile_acl_runtime_state WHERE id = 1",
	).Scan(&enabled, &generation))
	require.Zero(t, enabled)
	require.Zero(t, generation)
	_, err := db.Exec(`INSERT INTO citation_profile_acl_runtime_state
		(id, enabled, transition_generation, changed_at, updated_at)
		VALUES (2, 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	require.Error(t, err, "the runtime-state table must remain a singleton")
	_, err = db.Exec("UPDATE citation_profile_acl_runtime_state SET enabled = 2 WHERE id = 1")
	require.Error(t, err, "SQLite must preserve PostgreSQL boolean-domain semantics")
	_, err = db.Exec("UPDATE citation_profile_acl_runtime_state SET transition_generation = -1 WHERE id = 1")
	require.Error(t, err, "feature transition generations must remain monotonic and nonnegative")
}

func TestSQLiteCitationProfileOutboxDueIndexMatchesPostgresContract(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	chdirAndRestore(t, repoRoot)

	dbPath := filepath.Join(t.TempDir(), "citation-profile-outbox-index.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	db := openSQLiteDB(t, dbPath)
	assertSQLiteCitationProfileOutboxDueIndexContract(t, db)
}

func TestSQLiteCitationProfileF8QueueIndexesMatchPostgresAndFairClaimPlan(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	chdirAndRestore(t, repoRoot)

	dbPath := filepath.Join(t.TempDir(), "citation-profile-f8-queue-indexes.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db := openSQLiteDB(t, dbPath)

	assertSQLitePartialIndexContract(
		t, db,
		citationProfileActiveRunIndexName,
		citationProfileActiveRunIndexTable,
		citationProfileActiveRunColumns,
		citationProfileActiveRunPredicate,
	)
	assertSQLitePartialIndexContract(
		t, db,
		citationProfileOperationsIndexName,
		citationProfileOperationsIndexTable,
		citationProfileOperationsColumns,
		citationProfileOperationsPredicate,
	)

	var queueTable string
	var queueSQL string
	require.NoError(t, db.QueryRow(
		"SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		citationProfileACLQueueIndexName,
	).Scan(&queueTable, &queueSQL))
	require.Equal(t, citationProfileACLQueueIndexTable, queueTable)
	normalizedQueueSQL := normalizeIndexPredicate(queueSQL)
	require.Contains(t, normalizedQueueSQL,
		"case when next_acl_check_at is null then 0 else 1 end, next_acl_check_at, tenant_id, id",
		"the ACL claim index keys must exactly lead with the fair ORDER BY expression")
	whereAt := strings.Index(strings.ToLower(queueSQL), "where")
	require.NotEqual(t, -1, whereAt, "the ACL claim index must exclude non-live or disabled scopes")
	require.Equal(t, citationProfileACLQueueSQLitePredicate,
		normalizeIndexPredicate(queueSQL[whereAt+len("where"):]))

	rows, err := db.Query(`
		EXPLAIN QUERY PLAN
		SELECT id
		FROM citation_profile_scopes
		WHERE enabled = ?
		  AND deleted_at IS NULL
		  AND fenced_at IS NULL
		  AND (next_acl_check_at IS NULL OR next_acl_check_at <= ?)
		  AND (acl_check_lease_until IS NULL OR acl_check_lease_until <= ?)
		ORDER BY CASE WHEN next_acl_check_at IS NULL THEN 0 ELSE 1 END ASC,
		         next_acl_check_at ASC,
		         tenant_id ASC,
		         id ASC
		LIMIT 32`, 1, "2026-09-11 08:00:00", "2026-09-11 08:00:00")
	require.NoError(t, err)
	defer rows.Close()
	var planLines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		planLines = append(planLines, detail)
	}
	require.NoError(t, rows.Err())
	plan := strings.ToLower(strings.Join(planLines, "\n"))
	require.Contains(t, plan, "using index "+citationProfileACLQueueIndexName,
		"SQLite fair claim must be driven by the dedicated ordered partial index")
	require.NotContains(t, plan, "use temp b-tree for order by",
		"the fair claim index must satisfy ORDER BY without an unbounded sort")
}

func TestSQLiteCitationProfileF8ConstraintsAndCorrectionIndexMatchPostgres(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	chdirAndRestore(t, repoRoot)

	dbPath := filepath.Join(t.TempDir(), "citation-profile-f8-contract.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))
	db := openSQLiteDB(t, dbPath)

	t.Run("new scopes default to unknown and CURRENT requires authority binding", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO citation_profile_scopes
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch)
			VALUES (?, ?, ?, ?, ?)`,
			"scope-f8-default-unknown", 7, "subject-f8-default", "kb-f8-default", "epoch-f8-default",
		)
		require.NoError(t, err)
		var state string
		require.NoError(t, db.QueryRow(
			"SELECT acl_check_state FROM citation_profile_scopes WHERE id = ?",
			"scope-f8-default-unknown",
		).Scan(&state))
		require.Equal(t, "unknown", state)

		_, err = db.Exec(`
			INSERT INTO citation_profile_scopes
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state)
			VALUES (?, ?, ?, ?, ?, 'current')`,
			"scope-f8-bindingless-current", 7, "subject-f8-old-writer", "kb-f8-old-writer", "epoch-f8-old-writer",
		)
		require.Error(t, err, "SQLite must reject a rolling old writer's bindingless CURRENT row")

		_, err = db.Exec(`
			INSERT INTO citation_profile_scopes
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, enabled, acl_check_state)
			VALUES (?, ?, ?, ?, ?, 2, 'current')`,
			"scope-f8-non-boolean-enabled", 7, "subject-f8-enabled", "kb-f8-enabled", "epoch-f8-enabled",
		)
		require.Error(t, err,
			"SQLite enabled must have PostgreSQL BOOLEAN domain semantics and cannot bypass CURRENT checks")

		_, err = db.Exec(`
			INSERT INTO citation_profile_scopes
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state,
				 acl_checked_at, next_acl_check_at, acl_principal_type, acl_principal_id,
				 acl_authenticated_tenant_id, acl_api_key_id, acl_access_path,
				 acl_access_path_id, acl_generation)
			VALUES (?, ?, ?, ?, ?, 'current', 'not-a-date-1', 'not-a-date-2',
			        'web_user', ?, ?, 0, 'owner', '', 1)`,
			"scope-f8-invalid-current-time", 7, "subject-f8-invalid-time", "kb-f8-invalid-time",
			"epoch-f8-invalid-time", "subject-f8-invalid-time", 7,
		)
		require.Error(t, err,
			"SQLite CURRENT timestamps must be valid instants, matching PostgreSQL TIMESTAMPTZ")

		_, err = db.Exec(`
			INSERT INTO citation_profile_scopes
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_check_state,
				 acl_checked_at, next_acl_check_at, acl_principal_type, acl_principal_id,
				 acl_authenticated_tenant_id, acl_api_key_id, acl_access_path,
				 acl_access_path_id, acl_generation)
			VALUES (?, ?, ?, ?, ?, 'current', ?, ?, 'web_user', ?, ?, 0, 'owner', '', 1)`,
			"scope-f8-valid-current", 7, "subject-f8-valid", "kb-f8-valid", "epoch-f8-valid",
			"2026-09-11 00:00:00", "2099-09-11 00:00:00", "subject-f8-valid", 7,
		)
		require.NoError(t, err, "a complete fresh server-derived binding may be CURRENT")
	})

	t.Run("ACL generation is nonnegative", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO citation_profile_scopes
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, acl_generation)
			VALUES (?, ?, ?, ?, ?, ?)`,
			"scope-f8-negative-generation", 7, "subject-f8", "kb-f8", "epoch-f8", -1,
		)
		require.Error(t, err, "SQLite must reject the same negative ACL generation that PostgreSQL rejects")
	})

	t.Run("outbox paused seconds is nonnegative", func(t *testing.T) {
		_, err := db.Exec(`
			INSERT INTO citation_profile_event_outbox
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id, event_id, retry_budget_paused_seconds)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			"outbox-f8-negative-pause", 7, "subject-f8", "kb-f8", "epoch-f8",
			"scope-f8", "event-f8", -1,
		)
		require.Error(t, err, "SQLite must reject the same negative pause budget that PostgreSQL rejects")
	})

	t.Run("outbox lease deadline belongs only to live delivering work", func(t *testing.T) {
		insert := `
			INSERT INTO citation_profile_event_outbox
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
				 event_id, status, lease_until)
			VALUES (?, 7, 'subject-f8-lease', 'kb-f8-lease', 'epoch-f8-lease',
			        'scope-f8-lease', ?, ?, ?)`
		_, err := db.Exec(insert,
			"outbox-f8-pending-with-lease", "event-f8-pending-with-lease", "pending", "2099-09-11 00:00:00")
		require.Error(t, err, "pending work must not retain a delivering lease deadline")
		_, err = db.Exec(insert,
			"outbox-f8-terminal-with-lease", "event-f8-terminal-with-lease", "delivered", "2099-09-11 00:00:00")
		require.Error(t, err, "terminal work must not retain a delivering lease deadline")
		_, err = db.Exec(`
			INSERT INTO citation_profile_event_outbox
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
				 event_id, status, locked_at, locked_by)
			VALUES ('outbox-f8-delivering-without-lease', 7, 'subject-f8-lease',
			        'kb-f8-lease', 'epoch-f8-lease', 'scope-f8-lease',
			        'event-f8-delivering-without-lease', 'delivering',
			        '2099-09-10 23:59:00', 'legacy-worker')`)
		require.Error(t, err,
			"a rolling old writer must fail closed instead of creating immediately reclaimable delivering work")
		_, err = db.Exec(`
			INSERT INTO citation_profile_event_outbox
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
				 event_id, status, locked_at, lease_until, locked_by)
			VALUES ('outbox-f8-delivering-with-lease', 7, 'subject-f8-lease',
			        'kb-f8-lease', 'epoch-f8-lease', 'scope-f8-lease',
			        'event-f8-delivering-with-lease', 'delivering',
			        '2099-09-10 23:59:00', '2099-09-11 00:00:00', 'worker-f8')`)
		require.NoError(t, err, "live delivering work may carry its durable deadline")
		_, err = db.Exec(`
			UPDATE citation_profile_event_outbox
			SET status = 'pending'
			WHERE id = 'outbox-f8-delivering-with-lease'`)
		require.Error(t, err, "retry must clear the durable deadline atomically with the status change")
	})

	t.Run("empty correction idempotency keys are not unique", func(t *testing.T) {
		insert := `
			INSERT INTO citation_profile_corrections
				(id, tenant_id, subject_id, knowledge_base_id, subject_epoch, scope_id,
				 event_id, correction_type, actor_id, expected_read_version,
				 resulting_read_version, idempotency_key)
			VALUES (?, 7, 'subject-f8-idem', 'kb-f8-idem', 'epoch-f8-idem', 'scope-f8-idem',
				'event-f8-idem', 'detach', 'actor-f8', 1, 2, ?)`
		_, err := db.Exec(insert, "correction-f8-empty-1", "")
		require.NoError(t, err)
		_, err = db.Exec(insert, "correction-f8-empty-2", "")
		require.NoError(t, err,
			"empty idempotency keys are outside the PostgreSQL partial unique index")
		_, err = db.Exec(insert, "correction-f8-keyed-1", "stable-f8-key")
		require.NoError(t, err)
		_, err = db.Exec(insert, "correction-f8-keyed-2", "stable-f8-key")
		require.Error(t, err, "nonempty correction idempotency keys must remain unique")
	})

	var correctionIndexTable string
	var correctionIndexSQL string
	require.NoError(t, db.QueryRow(
		"SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		citationProfileCorrectionIdemIndexName,
	).Scan(&correctionIndexTable, &correctionIndexSQL))
	require.Equal(t, citationProfileCorrectionIdemTable, correctionIndexTable)
	require.Equal(t, citationProfileCorrectionIdemColumns,
		sqliteIndexColumns(t, db, citationProfileCorrectionIdemIndexName))
	whereAt := strings.Index(strings.ToLower(correctionIndexSQL), "where")
	require.NotEqual(t, -1, whereAt, "SQLite correction idempotency index must be partial like PostgreSQL")
	require.Equal(t, citationProfileCorrectionIdemPredicate,
		normalizeIndexPredicate(correctionIndexSQL[whereAt+len("where"):]))
}

func TestSQLiteMigration013RejectsPreexistingCitationProfileSchemaDrift(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	testCases := []struct {
		name  string
		setup string
	}{
		{
			name: "runtime state table without singleton constraints",
			setup: `CREATE TABLE citation_profile_acl_runtime_state (
				id INTEGER PRIMARY KEY,
				enabled INTEGER NOT NULL,
				transition_generation INTEGER NOT NULL,
				changed_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
		},
		{
			name:  "outbox due index on wrong table",
			setup: `CREATE INDEX idx_citation_profile_outbox_due ON tenants (id)`,
		},
		{
			name:  "terminal event key index nonunique and on wrong table",
			setup: `CREATE INDEX uq_citation_profile_event_key ON tenants (id)`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testRoot := t.TempDir()
			migrationsRoot := filepath.Join(testRoot, "migrations", "sqlite")
			require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "sqlite"), migrationsRoot))
			chdirAndRestore(t, testRoot)
			dbPath := filepath.Join(testRoot, "drift.db")

			seedDB, err := sql.Open("sqlite3", dbPath)
			require.NoError(t, err)
			driver, err := sqlite3migrate.WithInstance(seedDB, &sqlite3migrate.Config{})
			require.NoError(t, err)
			m, err := migrate.NewWithDatabaseInstance("file://migrations/sqlite", "sqlite3", driver)
			require.NoError(t, err)
			require.NoError(t, m.Migrate(expectedSQLiteMigrationVersion-1))
			_, _ = m.Close()

			injectDB, err := sql.Open("sqlite3", dbPath)
			require.NoError(t, err)
			_, err = injectDB.Exec(testCase.setup)
			require.NoError(t, err)
			require.NoError(t, injectDB.Close())

			err = RunMigrationsWithOptions(
				"sqlite3://unused",
				MigrationOptions{SQLiteDBPath: dbPath, AutoRecoverDirty: false},
			)
			require.Error(t, err,
				"SQLite v13 must fail closed rather than accept a same-named drifted object")

			verifyDB := openSQLiteDB(t, dbPath)
			version, dirty := sqliteMigrationState(t, verifyDB)
			require.Equal(t, expectedSQLiteMigrationVersion, version)
			require.True(t, dirty, "same-name schema drift must remain operator-diagnosable")
		})
	}
}

func assertSQLiteCitationProfileOutboxDueIndexContract(t *testing.T, db *sql.DB) {
	t.Helper()
	var tableName string
	var createSQL string
	require.NoError(t, db.QueryRow(
		"SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		citationProfileOutboxDueIndexName,
	).Scan(&tableName, &createSQL))
	require.Equal(t, citationProfileOutboxDueIndexTable, tableName)
	require.Equal(t, citationProfileOutboxDueIndexColumns, sqliteIndexColumns(t, db, citationProfileOutboxDueIndexName))

	whereAt := strings.Index(strings.ToLower(createSQL), "where")
	require.NotEqual(t, -1, whereAt,
		"SQLite due-work index must exclude delivered and deadletter rows like PostgreSQL")
	require.Equal(
		t,
		citationProfileOutboxDueIndexPredicate,
		normalizeIndexPredicate(createSQL[whereAt+len("where"):]),
		"SQLite and PostgreSQL due-work indexes must have the same terminal-row predicate",
	)
}

func TestSQLiteMigrationsUpgradeV4PreservesData(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)

	// Build a legacy v4 migration root (000000_init .. 000004_memory) so we
	// can prove the new migrations upgrade an existing Lite database without
	// replaying the baseline.
	legacyRoot := copySQLiteMigrationsV4(t, repoRoot)
	chdirAndRestore(t, legacyRoot)

	dbPath := filepath.Join(t.TempDir(), "upgrade.db")
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	db := openSQLiteDB(t, dbPath)
	versionBefore, dirtyBefore := sqliteMigrationState(t, db)
	require.Equal(t, 4, versionBefore)
	require.False(t, dirtyBefore)
	_, err := db.Exec("INSERT INTO tenants (name, business) VALUES (?, ?)", "upgrade-sentinel", "migration-test")
	require.NoError(t, err)
	_, err = db.Exec(
		"INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, source, tag_id) "+
			"VALUES (?, 1, ?, 'document', 'tagged-doc', 'manual', ?)",
		"legacy-knowledge-1", "legacy-kb-1", "legacy-tag-1",
	)
	require.NoError(t, err)

	// Run the full migration set from the repo root.
	chdirAndRestore(t, repoRoot)
	require.NoError(t, RunMigrationsWithOptions("sqlite3://unused", MigrationOptions{SQLiteDBPath: dbPath}))

	db = openSQLiteDB(t, dbPath)
	versionAfter, dirtyAfter := sqliteMigrationState(t, db)
	require.Equal(t, expectedSQLiteMigrationVersion, versionAfter)
	require.False(t, dirtyAfter)

	for _, table := range versionedSQLiteTables {
		require.Truef(t, sqliteTableExists(t, db, table), "upgraded SQLite DB must have table %s", table)
	}
	for table, columns := range versionedSQLiteColumns {
		for _, column := range columns {
			require.Truef(
				t,
				sqliteColumnExists(t, db, table, column),
				"upgraded SQLite DB must have column %s.%s",
				table,
				column,
			)
		}
	}

	var sentinelName string
	require.NoError(t, db.QueryRow("SELECT name FROM tenants WHERE business = ?", "migration-test").Scan(&sentinelName))
	require.Equal(t, "upgrade-sentinel", sentinelName)

	var relationCount int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM knowledge_tag_relations WHERE knowledge_id = ? AND tag_id = ?",
		"legacy-knowledge-1", "legacy-tag-1",
	).Scan(&relationCount))
	require.Equal(t, 1, relationCount)
	require.False(t, sqliteColumnExists(t, db, "knowledges", "tag_id"))
}

func TestSQLiteMigrationFailureLeavesDirtyStateWithoutAutoRecovery(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "sqlite")
	require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "sqlite"), migrationsRoot))
	require.NoError(t, os.WriteFile(
		filepath.Join(migrationsRoot, "999999_intentionally_broken.up.sql"),
		[]byte("CREATE TABLE intentionally_broken (id INTEGER;\n"),
		0o600,
	))
	chdirAndRestore(t, testRoot)

	dbPath := filepath.Join(testRoot, "failed.db")
	err := RunMigrationsWithOptions(
		"sqlite3://unused",
		MigrationOptions{SQLiteDBPath: dbPath, AutoRecoverDirty: false},
	)
	require.Error(t, err)

	db := openSQLiteDB(t, dbPath)
	version, dirty := sqliteMigrationState(t, db)
	require.Equal(t, 999999, version)
	require.True(t, dirty, "a failed migration must remain dirty until an operator repairs it")

	err = RunMigrationsWithOptions(
		"sqlite3://unused",
		MigrationOptions{SQLiteDBPath: dbPath, AutoRecoverDirty: false},
	)
	require.Error(t, err, "dirty state must block a retry when auto-recovery is disabled")
	versionAfterRetry, dirtyAfterRetry := sqliteMigrationState(t, db)
	require.Equal(t, version, versionAfterRetry)
	require.True(t, dirtyAfterRetry)
}

func TestSQLiteMigrationFinalDirtyStateReturnsError(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "sqlite")
	require.NoError(t, copyMigrationFiles(filepath.Join(repoRoot, "migrations", "sqlite"), migrationsRoot))
	require.NoError(t, os.WriteFile(
		filepath.Join(migrationsRoot, "999998_force_final_dirty.up.sql"),
		[]byte(`CREATE TRIGGER force_final_migration_dirty
AFTER INSERT ON schema_migrations
WHEN NEW.version = 999998 AND NEW.dirty = 0
BEGIN
  UPDATE schema_migrations SET dirty = 1 WHERE version = NEW.version;
END;
`),
		0o600,
	))
	chdirAndRestore(t, testRoot)

	dbPath := filepath.Join(testRoot, "final-dirty.db")
	err := RunMigrationsWithOptions(
		"sqlite3://unused",
		MigrationOptions{SQLiteDBPath: dbPath, AutoRecoverDirty: false},
	)
	require.Error(t, err, "a migration run that finishes dirty must fail closed")
	require.ErrorContains(t, err, "dirty state at version 999998")

	db := openSQLiteDB(t, dbPath)
	version, dirty := sqliteMigrationState(t, db)
	require.Equal(t, 999998, version)
	require.True(t, dirty)
	cachedVersion, cachedDirty, known := CachedMigrationVersion()
	require.True(t, known)
	require.Equal(t, uint(999998), cachedVersion)
	require.True(t, cachedDirty)
	require.NotEmpty(t, CachedMigrationError())
}

func TestSQLiteMigration013DownRefusesDataLossAndPreservesSchema(t *testing.T) {
	repoRoot := sqliteRepoRoot(t)
	chdirAndRestore(t, repoRoot)
	dbPath := filepath.Join(t.TempDir(), "round-trip.db")
	require.NoError(t, RunMigrationsWithOptions(
		"sqlite3://unused",
		MigrationOptions{SQLiteDBPath: dbPath},
	))

	sqlDB, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	driver, err := sqlite3migrate.WithInstance(sqlDB, &sqlite3migrate.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithDatabaseInstance("file://migrations/sqlite", "sqlite3", driver)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = m.Close() })

	version, dirty, err := m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(expectedSQLiteMigrationVersion), version)
	require.False(t, dirty)
	assertSQLiteCitationProfileOutboxDueIndexContract(t, sqlDB)
	_, err = sqlDB.Exec(`
		INSERT INTO citation_profile_scopes
			(id, tenant_id, subject_id, knowledge_base_id, subject_epoch)
		VALUES ('sqlite-down-sentinel', 13, 'sqlite-down-subject', 'sqlite-down-kb', 'sqlite-down-epoch')`)
	require.NoError(t, err)
	_, err = sqlDB.Exec("CREATE TABLE " + citationProfileDownRefusalMarker + " (bypass INTEGER)")
	require.NoError(t, err,
		"the adversarial fixture must precreate the old sentinel relation before rollback")

	err = m.Steps(-1)
	require.Error(t, err, "000013 down must refuse irreversible citation-profile data loss")
	require.ErrorContains(t, err, "integer overflow")
	version, dirty, err = m.Version()
	require.NoError(t, err)
	require.Equal(t, uint(expectedSQLiteMigrationVersion-1), version)
	require.True(t, dirty, "a refused down migration must remain operator-diagnosable")
	for _, table := range versionedSQLiteTables[len(versionedSQLiteTables)-8:] {
		require.Truef(t, sqliteTableExists(t, sqlDB, table),
			"SQLite v13 refused down migration must preserve Topic 4 table %s", table)
	}
	require.True(t, sqliteIndexExists(t, sqlDB, citationProfileOutboxDueIndexName),
		"SQLite v13 refused down migration must preserve the due-work index")
	var sentinelCount int
	require.NoError(t, sqlDB.QueryRow(
		"SELECT COUNT(*) FROM citation_profile_scopes WHERE id = 'sqlite-down-sentinel'",
	).Scan(&sentinelCount))
	require.Equal(t, 1, sentinelCount, "refusal must preserve citation-profile user data")
	assertSQLiteCitationProfileOutboxDueIndexContract(t, sqlDB)
}

func assertSQLitePartialIndexContract(
	t *testing.T,
	db *sql.DB,
	indexName string,
	tableName string,
	wantColumns []string,
	wantPredicate string,
) {
	t.Helper()
	var gotTable string
	var createSQL string
	require.NoError(t, db.QueryRow(
		"SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		indexName,
	).Scan(&gotTable, &createSQL))
	require.Equal(t, tableName, gotTable)
	require.Equal(t, wantColumns, sqliteIndexColumns(t, db, indexName))
	whereAt := strings.Index(strings.ToLower(createSQL), "where")
	require.NotEqual(t, -1, whereAt, "index %s must be partial", indexName)
	require.Equal(t, wantPredicate, normalizeIndexPredicate(createSQL[whereAt+len("where"):]))
}

func sqliteRepoRoot(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return repoRoot
}

func chdirAndRestore(t *testing.T, dir string) {
	t.Helper()
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })
}

func openSQLiteDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func sqliteMigrationState(t *testing.T, db *sql.DB) (version int, dirty bool) {
	t.Helper()
	require.NoError(t, db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty))
	return version, dirty
}

func sqliteTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		table,
	).Scan(&n))
	return n == 1
}

func sqliteColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
		table,
		column,
	).Scan(&n))
	return n == 1
}

func sqliteIndexColumns(t *testing.T, db *sql.DB, indexName string) []string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM pragma_index_info(?) ORDER BY seqno", indexName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rows.Close() })

	var columns []string
	for rows.Next() {
		var column string
		require.NoError(t, rows.Scan(&column))
		columns = append(columns, column)
	}
	require.NoError(t, rows.Err())
	return columns
}

func sqliteIndexExists(t *testing.T, db *sql.DB, indexName string) bool {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?",
		indexName,
	).Scan(&count))
	return count == 1
}

func normalizeIndexPredicate(predicate string) string {
	withoutSyntaxNoise := strings.NewReplacer("(", "", ")", "", `"`, "").Replace(predicate)
	return strings.Join(strings.Fields(strings.ToLower(withoutSyntaxNoise)), " ")
}

func assertSQLiteShareLinkInvitationsWork(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec("INSERT INTO tenants (name, business) VALUES (?, ?)", "share-link-tenant", "share-link-test")
	require.NoError(t, err)

	expiresAt := "2099-01-01 00:00:00"
	shareLinkInsert := "INSERT INTO tenant_invitations " +
		"(tenant_id, invitee_user_id, token, role, status, expires_at) " +
		"VALUES (1, '', ?, 'member', 'pending', ?)"
	_, err = db.Exec(shareLinkInsert, "token-a", expiresAt)
	require.NoError(t, err)
	_, err = db.Exec(shareLinkInsert, "token-b", expiresAt)
	require.NoError(t, err)

	var count int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM tenant_invitations WHERE tenant_id = 1 AND invitee_user_id = '' AND status = 'pending'",
	).Scan(&count))
	require.Equal(t, 2, count)
}

func assertSQLiteMCPOAuthPrincipalUpsertWorks(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(
		"INSERT INTO mcp_services (id, tenant_id, name, transport_type) VALUES (?, 1, 'svc', 'http')",
		"svc-migration-1",
	)
	require.NoError(t, err)

	tokenInsertPrefix := "INSERT INTO mcp_oauth_tokens " +
		"(id, tenant_id, user_id, service_id, principal_type, principal_id, access_token) "
	_, err = db.Exec(
		tokenInsertPrefix +
			"VALUES ('tok-1', 1, 'u1', 'svc-migration-1', 'web_user', 'u1', 'token-1')",
	)
	require.NoError(t, err)

	_, err = db.Exec(
		tokenInsertPrefix +
			"VALUES ('tok-2', 1, 'u1', 'svc-migration-1', 'web_user', 'u1', 'token-2') " +
			"ON CONFLICT(tenant_id, principal_type, principal_id, service_id) " +
			"DO UPDATE SET access_token = excluded.access_token",
	)
	require.NoError(t, err)

	var accessToken string
	require.NoError(t, db.QueryRow(
		"SELECT access_token FROM mcp_oauth_tokens "+
			"WHERE tenant_id = 1 AND principal_type = 'web_user' "+
			"AND principal_id = 'u1' AND service_id = 'svc-migration-1'",
	).Scan(&accessToken))
	require.Equal(t, "token-2", accessToken)

	var rowCount int
	require.NoError(t, db.QueryRow(
		"SELECT COUNT(*) FROM mcp_oauth_tokens WHERE tenant_id = 1 AND service_id = 'svc-migration-1'",
	).Scan(&rowCount))
	require.Equal(t, 1, rowCount)
}

func copySQLiteMigrationsV4(t *testing.T, repoRoot string) string {
	t.Helper()
	dest := t.TempDir()
	srcDir := filepath.Join(repoRoot, "migrations", "sqlite")
	destDir := filepath.Join(dest, "migrations", "sqlite")
	require.NoError(t, os.MkdirAll(destDir, 0o755))

	legacy := []string{
		"000000_init.up.sql",
		"000001_remove_wiki_log.up.sql",
		"000002_knowledge_folder_path.up.sql",
		"000003_knowledge_base_auto_tag_config.up.sql",
		"000004_memory.up.sql",
	}
	for _, name := range legacy {
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(destDir, name), data, 0o600))
	}
	return dest
}

func copyMigrationFiles(srcDir, destDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(srcDir, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destDir, entry.Name()), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}
