package database

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestT2M01AWorkbenchMigrationIsPostgresOnlyAndNoopDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	upPath := filepath.Join(repoRoot, "migrations", "versioned", "000093_workbench_sessions.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "versioned", "000093_workbench_sessions.down.sql")
	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	require.Contains(t, string(up), "CREATE TABLE IF NOT EXISTS workbench_sessions")
	require.Contains(t, string(up), "incarnation_id VARCHAR(36) NOT NULL")
	require.Contains(t, string(up), "backend_type VARCHAR(32) NOT NULL")
	require.Contains(t, string(up), "JSONB NOT NULL DEFAULT '{}'::jsonb")
	require.Contains(t, string(up), "uq_workbench_sessions_tenant_chat_purpose_active")
	require.Contains(t, string(up), "WHERE closed_at IS NULL")
	require.Contains(t, string(down), "schema-retaining")
	require.Contains(t, string(down), "SELECT 1")

	sqlitePath := filepath.Join(repoRoot, "migrations", "sqlite", "000093_workbench_sessions.up.sql")
	require.NoFileExists(t, sqlitePath)
}

func TestT2L10WorkbenchMigrationsAvoidOfficialMCPMetadataVersion(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	versioned := filepath.Join(repoRoot, "migrations", "versioned")

	require.NoFileExists(t, filepath.Join(versioned, "000092_workbench_sessions.up.sql"))
	for _, name := range []string{
		"000093_workbench_sessions.up.sql",
		"000094_workbench_control.up.sql",
		"000095_workbench_runner_identity.up.sql",
		"000096_workbench_artifacts.up.sql",
		"000097_workbench_skill_runs.up.sql",
		"000098_workbench_legacy_numbering_reconcile.up.sql",
	} {
		require.FileExists(t, filepath.Join(versioned, name), name)
	}

	reconcilePath := filepath.Join(versioned, "000099_official_mcp_metadata_reconcile.up.sql")
	reconcile, err := os.ReadFile(reconcilePath)
	require.NoError(t, err)
	reconcileSQL := string(reconcile)
	require.Contains(t, reconcileSQL, "ALTER TABLE mcp_services")
	require.Contains(t, reconcileSQL, "ADD COLUMN IF NOT EXISTS usage_instructions TEXT NOT NULL DEFAULT ''")
	require.Contains(t, reconcileSQL, "CREATE TABLE IF NOT EXISTS mcp_metadata")
	require.Contains(t, reconcileSQL, "PRIMARY KEY (tenant_id, service_id, principal)")
	require.NoFileExists(t, filepath.Join(repoRoot, "migrations", "sqlite", "000099_official_mcp_metadata_reconcile.up.sql"))
}

func TestT2L07WorkbenchMigrationReconcilesLegacyNumberingCollision(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	upPath := filepath.Join(repoRoot, "migrations", "versioned", "000098_workbench_legacy_numbering_reconcile.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "versioned", "000098_workbench_legacy_numbering_reconcile.down.sql")
	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	require.Contains(t, string(up), "ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true")
	require.Contains(t, string(up), "workbench_sessions")
	require.Contains(t, string(up), "workbench_skill_runs")
	require.Contains(t, string(down), "schema-retaining")
	require.Contains(t, string(down), "SELECT 1")
	require.NoFileExists(t, filepath.Join(repoRoot, "migrations", "sqlite", "000098_workbench_legacy_numbering_reconcile.up.sql"))
}
func TestT2M01ARealPostgresMigrationLifecycle(t *testing.T) {
	if os.Getenv("WEKNORA_T2_M01A_POSTGRES") != "1" {
		return
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	defer func() { require.NoError(t, os.Chdir(previousDir)) }()

	dsn := t2m01aPostgresURL(os.Getenv("DB_NAME"))
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
	db, err := gorm.Open(postgres.Open(t2m01aGormDSN(os.Getenv("DB_NAME"))), &gorm.Config{})
	require.NoError(t, err)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	version, dirty := t2m01aMigrationVersion(t, db)
	require.GreaterOrEqual(t, version, 99)
	require.False(t, dirty)
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}), "repeated/no-change migration must succeed")

	require.NoError(t, db.Exec("UPDATE schema_migrations SET version = 90, dirty = false").Error)
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}), "bound version 90 to next upgrade must succeed")
	version, dirty = t2m01aMigrationVersion(t, db)
	require.GreaterOrEqual(t, version, 99)
	require.False(t, dirty)

	m, err := migrate.New("file://migrations/versioned", dsn)
	require.NoError(t, err)
	require.NoError(t, m.Migrate(91), "all Workbench down migrations must retain schema and evidence")
	_, _ = m.Close()
	var exists bool
	require.NoError(t, db.Raw("SELECT to_regclass('public.workbench_sessions') IS NOT NULL").Scan(&exists).Error)
	require.True(t, exists, "down migration must retain schema/evidence")
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))

	require.NoError(t, db.Exec("ALTER TABLE mcp_tool_approvals DROP COLUMN IF EXISTS enabled").Error)
	require.NoError(t, db.Exec("UPDATE schema_migrations SET version = 95, dirty = false").Error)
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}), "legacy Topic 2 version 95 must reconcile the official version 91 semantics")
	var enabledColumnExists bool
	require.NoError(t, db.Raw("SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'mcp_tool_approvals' AND column_name = 'enabled')").Scan(&enabledColumnExists).Error)
	require.True(t, enabledColumnExists)
	version, dirty = t2m01aMigrationVersion(t, db)
	require.GreaterOrEqual(t, version, 99)
	require.False(t, dirty)

	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_metadata").Error)
	require.NoError(t, db.Exec("ALTER TABLE mcp_services DROP COLUMN IF EXISTS usage_instructions").Error)
	require.NoError(t, db.Exec("UPDATE schema_migrations SET version = 98, dirty = false").Error)
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}), "databases that skipped official version 92 must receive its additive schema")
	var metadataTableExists bool
	require.NoError(t, db.Raw("SELECT to_regclass('public.mcp_metadata') IS NOT NULL").Scan(&metadataTableExists).Error)
	require.True(t, metadataTableExists)
	var usageInstructionsExists bool
	require.NoError(t, db.Raw("SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'mcp_services' AND column_name = 'usage_instructions')").Scan(&usageInstructionsExists).Error)
	require.True(t, usageInstructionsExists)

	require.NoError(t, db.Exec("UPDATE schema_migrations SET dirty = true").Error)
	err = RunMigrationsWithOptions(dsn, MigrationOptions{})
	require.Error(t, err, "dirty migration state must deny startup migration")
	require.Contains(t, err.Error(), "dirty state")
	require.NoError(t, db.Exec("UPDATE schema_migrations SET dirty = false").Error)
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
}

func TestT2L01WorkbenchControlMigrationIsPostgresOnlyAndNoopDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	upPath := filepath.Join(repoRoot, "migrations", "versioned", "000094_workbench_control.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "versioned", "000094_workbench_control.down.sql")
	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	upSQL := string(up)
	require.Contains(t, upSQL, "CREATE TABLE IF NOT EXISTS workbench_jobs")
	require.Contains(t, upSQL, "CREATE TABLE IF NOT EXISTS workbench_commands")
	require.Contains(t, upSQL, "CREATE TABLE IF NOT EXISTS workbench_runner_events")
	require.Contains(t, upSQL, "CREATE TABLE IF NOT EXISTS workbench_audit_outbox")
	require.Contains(t, upSQL, "start_nonce_hash VARCHAR(64) NOT NULL")
	require.Contains(t, upSQL, "UNIQUE (tenant_id, workbench_session_id, lease_epoch, start_nonce_hash)")
	require.Contains(t, upSQL, "UNIQUE (tenant_id, workbench_job_id, sequence)")
	require.Contains(t, upSQL, "UNIQUE (tenant_id, workbench_job_id, seq)")
	require.Contains(t, upSQL, "idx_workbench_audit_outbox_tenant_state_created_at")
	require.Contains(t, string(down), "schema-retaining")
	require.Contains(t, string(down), "SELECT 1")

	sqlitePath := filepath.Join(repoRoot, "migrations", "sqlite", "000094_workbench_control.up.sql")
	require.NoFileExists(t, sqlitePath)
}

func TestT2L03WorkbenchRunnerIdentityMigrationIsPostgresOnlyAndNoopDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	upPath := filepath.Join(repoRoot, "migrations", "versioned", "000095_workbench_runner_identity.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "versioned", "000095_workbench_runner_identity.down.sql")
	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	upSQL := string(up)
	require.Contains(t, upSQL, "DROP CONSTRAINT IF EXISTS chk_workbench_jobs_backend_identity_nonempty")
	require.Contains(t, upSQL, "chk_workbench_jobs_backend_identity_after_bind")
	require.Contains(t, upSQL, "state IN ('QUEUED', 'STARTING', 'CANCELLED', 'LOST')")
	require.Contains(t, upSQL, "length(trim(backend_identity)) > 0")
	require.Contains(t, string(down), "schema-retaining")
	require.Contains(t, string(down), "SELECT 1")

	sqlitePath := filepath.Join(repoRoot, "migrations", "sqlite", "000095_workbench_runner_identity.up.sql")
	require.NoFileExists(t, sqlitePath)
}

func TestT2L05WorkbenchArtifactsMigrationIsPostgresOnlyAndNoopDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	upPath := filepath.Join(repoRoot, "migrations", "versioned", "000096_workbench_artifacts.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "versioned", "000096_workbench_artifacts.down.sql")
	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	upSQL := string(up)
	require.Contains(t, upSQL, "CREATE TABLE IF NOT EXISTS workbench_artifact_versions")
	require.Contains(t, upSQL, "source_file_ref JSONB NOT NULL")
	require.Contains(t, upSQL, "content_sha256 VARCHAR(64) NOT NULL")
	require.Contains(t, upSQL, "content BYTEA NOT NULL")
	require.Contains(t, upSQL, "UNIQUE (tenant_id, artifact_id, version)")
	require.Contains(t, upSQL, "fk_workbench_artifacts_command")
	require.Contains(t, upSQL, "FOREIGN KEY (tenant_id, command_id) REFERENCES workbench_commands(tenant_id, id)")
	require.Contains(t, upSQL, "chk_workbench_artifacts_command_nonempty")
	require.NotContains(t, upSQL, "FOREIGN KEY (tenant_id, command_id) REFERENCES workbench_commands(tenant_id, id) ON DELETE CASCADE")
	require.Contains(t, upSQL, "idx_workbench_artifacts_tenant_chat_created_at")
	require.Contains(t, string(down), "schema-retaining")
	require.Contains(t, string(down), "SELECT 1")

	sqlitePath := filepath.Join(repoRoot, "migrations", "sqlite", "000096_workbench_artifacts.up.sql")
	require.NoFileExists(t, sqlitePath)
}

func TestT2L06WorkbenchSkillRunsMigrationIsPostgresOnlyAndNoopDown(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	upPath := filepath.Join(repoRoot, "migrations", "versioned", "000097_workbench_skill_runs.up.sql")
	downPath := filepath.Join(repoRoot, "migrations", "versioned", "000097_workbench_skill_runs.down.sql")
	up, err := os.ReadFile(upPath)
	require.NoError(t, err)
	down, err := os.ReadFile(downPath)
	require.NoError(t, err)

	upSQL := string(up)
	require.Contains(t, upSQL, "CREATE TABLE IF NOT EXISTS workbench_skill_runs")
	require.Contains(t, upSQL, "skill_name VARCHAR(64) NOT NULL")
	require.Contains(t, upSQL, "skill_operation VARCHAR(64) NOT NULL")
	require.Contains(t, upSQL, "output_file_ref JSONB NOT NULL")
	require.Contains(t, upSQL, "UNIQUE (tenant_id, command_id)")
	require.Contains(t, upSQL, "FOREIGN KEY (tenant_id, command_id) REFERENCES workbench_commands(tenant_id, id)")
	require.Contains(t, upSQL, "chk_workbench_skill_runs_presentation_only")
	require.Contains(t, upSQL, "idx_workbench_skill_runs_tenant_skill_state_created_at")
	require.Contains(t, string(down), "schema-retaining")
	require.Contains(t, string(down), "SELECT 1")

	sqlitePath := filepath.Join(repoRoot, "migrations", "sqlite", "000097_workbench_skill_runs.up.sql")
	require.NoFileExists(t, sqlitePath)
}
func TestT2L01RealPostgresWorkbenchControlMigrationLifecycle(t *testing.T) {
	if os.Getenv("WEKNORA_T2_M01A_POSTGRES") != "1" {
		return
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	defer func() { require.NoError(t, os.Chdir(previousDir)) }()

	dsn := t2m01aPostgresURL(os.Getenv("DB_NAME"))
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
	db, err := gorm.Open(postgres.Open(t2m01aGormDSN(os.Getenv("DB_NAME"))), &gorm.Config{})
	require.NoError(t, err)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	version, dirty := t2m01aMigrationVersion(t, db)
	require.GreaterOrEqual(t, version, 99)
	require.False(t, dirty)

	for _, table := range []string{"workbench_jobs", "workbench_commands", "workbench_runner_events", "workbench_audit_outbox"} {
		var exists bool
		require.NoError(t, db.Raw("SELECT to_regclass(?) IS NOT NULL", "public."+table).Scan(&exists).Error)
		require.True(t, exists, "%s table must exist after T2-L01 migration", table)
	}

	require.NoError(t, db.Exec("INSERT INTO sessions (id, tenant_id, title, user_id) VALUES ('t2l01-chat', 71, 'T2L01', 'actor-a') ON CONFLICT (id) DO NOTHING").Error)
	require.NoError(t, db.Exec(`INSERT INTO workbench_sessions (id, tenant_id, chat_session_id, purpose, incarnation_id, sandbox_config_id, backend_type, state, state_version, lease_epoch, capability_snapshot, policy_snapshot, created_by) VALUES ('t2l01-workbench', 71, 't2l01-chat', 'workbench', 'incarnation-1', 'sandbox-config-a', 'docker', 'READY', 1, 3, '{}'::jsonb, '{}'::jsonb, 'actor-a')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO workbench_jobs (id, tenant_id, workbench_session_id, chat_session_id, incarnation_id, lease_epoch, start_nonce_hash, backend_type, backend_identity, state, state_version, resource_policy_snapshot, created_by) VALUES ('t2l01-job', 71, 't2l01-workbench', 't2l01-chat', 'incarnation-1', 3, repeat('a', 64), 'docker', 'docker://local/t2l01', 'QUEUED', 0, '{}'::jsonb, 'actor-a')`).Error)
	err = db.Exec(`INSERT INTO workbench_jobs (id, tenant_id, workbench_session_id, chat_session_id, incarnation_id, lease_epoch, start_nonce_hash, backend_type, backend_identity, state, state_version, resource_policy_snapshot, created_by) VALUES ('t2l01-job-dup', 71, 't2l01-workbench', 't2l01-chat', 'incarnation-1', 3, repeat('a', 64), 'docker', 'docker://local/t2l01', 'QUEUED', 0, '{}'::jsonb, 'actor-a')`).Error
	require.Error(t, err, "start nonce must be single-use per tenant/workbench/lease epoch")

	require.NoError(t, db.Exec(`INSERT INTO workbench_commands (id, tenant_id, workbench_job_id, workbench_session_id, sequence, kind, payload, state, state_version, created_by) VALUES ('t2l01-command', 71, 't2l01-job', 't2l01-workbench', 1, 'exec', '{"argv":["echo","ok"]}'::jsonb, 'QUEUED', 0, 'actor-a')`).Error)
	err = db.Exec(`INSERT INTO workbench_commands (id, tenant_id, workbench_job_id, workbench_session_id, sequence, kind, payload, state, state_version, created_by) VALUES ('t2l01-command-dup', 71, 't2l01-job', 't2l01-workbench', 1, 'exec', '{}'::jsonb, 'QUEUED', 0, 'actor-a')`).Error
	require.Error(t, err, "command sequence must be unique per tenant/job")

	require.NoError(t, db.Exec(`INSERT INTO workbench_runner_events (id, tenant_id, workbench_job_id, workbench_session_id, command_id, seq, event_type, payload) VALUES ('t2l01-event', 71, 't2l01-job', 't2l01-workbench', 't2l01-command', 1, 'stdout', '{"chunk":"ok"}'::jsonb)`).Error)
	err = db.Exec(`INSERT INTO workbench_runner_events (id, tenant_id, workbench_job_id, workbench_session_id, command_id, seq, event_type, payload) VALUES ('t2l01-event-dup', 71, 't2l01-job', 't2l01-workbench', 't2l01-command', 1, 'stdout', '{}'::jsonb)`).Error
	require.Error(t, err, "runner event sequence must be unique per tenant/job")

	require.NoError(t, db.Exec(`INSERT INTO workbench_audit_outbox (id, tenant_id, workbench_session_id, workbench_job_id, command_id, action, actor_user_id, outcome, payload, idempotency_key) VALUES ('t2l01-audit', 71, 't2l01-workbench', 't2l01-job', 't2l01-command', 'workbench.command.queued', 'actor-a', 'success', '{}'::jsonb, 't2l01-command-queued')`).Error)

	m, err := migrate.New("file://migrations/versioned", dsn)
	require.NoError(t, err)
	require.NoError(t, m.Steps(-1), "T2-L01 down migration must retain control-plane evidence")
	_, _ = m.Close()
	var jobsExist bool
	require.NoError(t, db.Raw("SELECT to_regclass('public.workbench_jobs') IS NOT NULL").Scan(&jobsExist).Error)
	require.True(t, jobsExist, "down migration must retain T2-L01 schema/evidence")
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
}

func TestT2L03RealPostgresAllowsQueuedJobBeforeBackendBind(t *testing.T) {
	if os.Getenv("WEKNORA_T2_M01A_POSTGRES") != "1" {
		return
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	defer func() { require.NoError(t, os.Chdir(previousDir)) }()

	dsn := t2m01aPostgresURL(os.Getenv("DB_NAME"))
	require.NoError(t, RunMigrationsWithOptions(dsn, MigrationOptions{}))
	db, err := gorm.Open(postgres.Open(t2m01aGormDSN(os.Getenv("DB_NAME"))), &gorm.Config{})
	require.NoError(t, err)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	version, dirty := t2m01aMigrationVersion(t, db)
	require.GreaterOrEqual(t, version, 99)
	require.False(t, dirty)

	require.NoError(t, db.Exec("INSERT INTO sessions (id, tenant_id, title, user_id) VALUES ('t2l03-chat', 72, 'T2L03', 'actor-a') ON CONFLICT (id) DO NOTHING").Error)
	require.NoError(t, db.Exec(`INSERT INTO workbench_sessions (id, tenant_id, chat_session_id, purpose, incarnation_id, sandbox_config_id, backend_type, state, state_version, lease_epoch, capability_snapshot, policy_snapshot, created_by) VALUES ('t2l03-workbench', 72, 't2l03-chat', 'workbench', 'incarnation-1', 'sandbox-config-a', 'docker', 'READY', 1, 3, '{}'::jsonb, '{}'::jsonb, 'actor-a')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO workbench_jobs (id, tenant_id, workbench_session_id, chat_session_id, incarnation_id, lease_epoch, start_nonce_hash, backend_type, backend_identity, state, state_version, resource_policy_snapshot, created_by) VALUES ('t2l03-job', 72, 't2l03-workbench', 't2l03-chat', 'incarnation-1', 3, repeat('b', 64), 'docker', '', 'QUEUED', 0, '{}'::jsonb, 'actor-a')`).Error)
	err = db.Exec(`UPDATE workbench_jobs SET state = 'RUNNING' WHERE tenant_id = 72 AND id = 't2l03-job'`).Error
	require.Error(t, err, "RUNNING workbench job must have a backend identity")
	require.NoError(t, db.Exec(`UPDATE workbench_jobs SET backend_identity = 'docker://local/t2l03', state = 'RUNNING', state_version = state_version + 1 WHERE tenant_id = 72 AND id = 't2l03-job'`).Error)
}
func t2m01aMigrationVersion(t *testing.T, db *gorm.DB) (int, bool) {
	t.Helper()
	var version int
	var dirty bool
	require.NoError(t, db.Raw("SELECT version, dirty FROM schema_migrations").Row().Scan(&version, &dirty))
	return version, dirty
}

func t2m01aGormDSN(dbName string) string {
	return "host=" + os.Getenv("DB_HOST") + " port=" + os.Getenv("DB_PORT") + " user=" + os.Getenv("DB_USER") + " password=" + os.Getenv("DB_PASSWORD") + " dbname=" + dbName + " sslmode=disable TimeZone=UTC"
}

func t2m01aPostgresURL(dbName string) string {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(os.Getenv("DB_HOST"), os.Getenv("DB_PORT")), Path: "/" + dbName}
	u.User = url.UserPassword(os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"))
	q := u.Query()
	q.Set("sslmode", "disable")
	q.Set("options", "-c app.skip_embedding=true")
	u.RawQuery = q.Encode()
	return u.String()
}
