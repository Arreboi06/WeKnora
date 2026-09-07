package repository

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestT2M01AWorkbenchRepositoryRejectsSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:t2m01a-workbench?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	repo := NewWorkbenchSessionRepository(db)

	err = repo.ProbeSchema(context.Background())
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)

	_, err = repo.CreateOrGet(context.Background(), t2m01aWorkbenchRecord(1, "chat-a", "incarnation-a"))
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)
}

func TestT2M01ARealPostgresWorkbenchRepositoryBehavior(t *testing.T) {
	if os.Getenv("WEKNORA_T2_M01A_POSTGRES") != "1" {
		return
	}
	ctx := context.Background()
	emptyDB := t2m01aPostgresRepositoryDB(t, false)
	err := NewWorkbenchSessionRepository(emptyDB).ProbeSchema(ctx)
	require.ErrorIs(t, err, ErrWorkbenchMigrationUnavailable)

	db := t2m01aPostgresRepositoryDB(t, true)
	repo := NewWorkbenchSessionRepository(db)
	require.NoError(t, repo.ProbeSchema(ctx))

	t2m01aInsertChatSession(t, db, 1, "chat-a")
	created, err := repo.CreateOrGet(ctx, t2m01aWorkbenchRecord(1, "chat-a", "incarnation-a"))
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, types.WorkbenchStateProvisioning, created.State)
	require.Equal(t, int64(0), created.StateVersion)
	require.Equal(t, int64(0), created.LeaseEpoch)
	require.Nil(t, created.ClosedAt)

	duplicate, err := repo.CreateOrGet(ctx, t2m01aWorkbenchRecord(1, "chat-a", "incarnation-a"))
	require.NoError(t, err)
	require.Equal(t, created.ID, duplicate.ID)

	_, err = repo.CreateOrGet(ctx, t2m01aWorkbenchRecord(1, "chat-a", "incarnation-b"))
	require.ErrorIs(t, err, ErrWorkbenchSessionConflict)

	_, err = repo.GetByID(ctx, 2, created.ID)
	require.ErrorIs(t, err, ErrWorkbenchSessionNotFound)

	active, err := repo.GetActiveByChatSession(ctx, 1, "chat-a")
	require.NoError(t, err)
	require.Equal(t, created.ID, active.ID)

	ready, err := repo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID:             1,
		ID:                   created.ID,
		ExpectedStateVersion: 0,
		ExpectedState:        types.WorkbenchStateProvisioning,
		NextState:            types.WorkbenchStateReady,
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchStateReady, ready.State)
	require.Equal(t, int64(1), ready.StateVersion)

	_, err = repo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID:             1,
		ID:                   created.ID,
		ExpectedStateVersion: 0,
		NextState:            types.WorkbenchStateClosing,
	})
	require.ErrorIs(t, err, ErrWorkbenchSessionStaleVersion)

	closed, err := repo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID:             1,
		ID:                   created.ID,
		ExpectedStateVersion: 1,
		ExpectedState:        types.WorkbenchStateReady,
		NextState:            types.WorkbenchStateClosed,
		TerminalReason:       "owner_closed",
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchStateClosed, closed.State)
	require.Equal(t, int64(2), closed.StateVersion)
	require.NotNil(t, closed.ClosedAt)
	require.Equal(t, "owner_closed", closed.TerminalReason)

	_, err = repo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID:             1,
		ID:                   created.ID,
		ExpectedStateVersion: 2,
		NextState:            types.WorkbenchStateReady,
	})
	require.ErrorIs(t, err, ErrWorkbenchSessionStaleVersion)

	replacement, err := repo.CreateOrGet(ctx, t2m01aWorkbenchRecord(1, "chat-a", "incarnation-b"))
	require.NoError(t, err)
	require.NotEqual(t, created.ID, replacement.ID)

	lost, err := repo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID:             1,
		ID:                   replacement.ID,
		ExpectedStateVersion: 0,
		ExpectedState:        types.WorkbenchStateProvisioning,
		NextState:            types.WorkbenchStateLost,
		TerminalReason:       "lease_lost",
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchStateLost, lost.State)
	require.Equal(t, int64(1), lost.StateVersion)
	require.NotNil(t, lost.ClosedAt)
	require.Equal(t, "lease_lost", lost.TerminalReason)

	t2m01aInsertChatSession(t, db, 1, "chat-b")
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row, err := repo.CreateOrGet(ctx, t2m01aWorkbenchRecord(1, "chat-b", "incarnation-concurrent"))
			if err != nil {
				errs <- err
				return
			}
			ids <- row.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		require.Equal(t, first, id)
	}
}

func t2m01aWorkbenchRecord(tenantID uint64, chatSessionID, incarnationID string) *types.WorkbenchSession {
	return &types.WorkbenchSession{
		TenantID:           tenantID,
		ChatSessionID:      chatSessionID,
		Purpose:            types.WorkbenchPurpose,
		IncarnationID:      incarnationID,
		SandboxConfigID:    "sandbox-config-a",
		BackendType:        "docker-r1",
		State:              types.WorkbenchStateProvisioning,
		StateVersion:       0,
		LeaseEpoch:         0,
		CapabilitySnapshot: types.JSONMap{"sandbox.workbench": true},
		PolicySnapshot:     types.JSONMap{"max_seconds": float64(60)},
		CreatedBy:          "actor-a",
	}
}

func t2m01aPostgresRepositoryDB(t *testing.T, migrate bool) *gorm.DB {
	t.Helper()
	name := "weknora_m01a_repo_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := gorm.Open(postgres.Open(t2m01aGormDSN("postgres")), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		admin.Exec("DROP DATABASE IF EXISTS " + t2m01aQuoteIdentifier(name) + " WITH (FORCE)")
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, admin.Exec("CREATE DATABASE "+t2m01aQuoteIdentifier(name)).Error)

	if migrate {
		previousDir, err := os.Getwd()
		require.NoError(t, err)
		repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
		require.NoError(t, err)
		require.NoError(t, os.Chdir(repoRoot))
		t.Cleanup(func() { _ = os.Chdir(previousDir) })
		require.NoError(t, database.RunMigrationsWithOptions(t2m01aPostgresURL(name), database.MigrationOptions{}))
		require.NoError(t, os.Chdir(previousDir))
	}

	db, err := gorm.Open(postgres.Open(t2m01aGormDSN(name)), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func t2m01aInsertChatSession(t *testing.T, db *gorm.DB, tenantID uint64, id string) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO sessions (id, tenant_id, title, user_id) VALUES (?, ?, ?, ?) ON CONFLICT (id) DO NOTHING",
		id, tenantID, "Workbench host chat", "actor-a",
	).Error)
}

func t2m01aGormDSN(dbName string) string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=UTC",
		os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), dbName,
	)
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

func t2m01aQuoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
