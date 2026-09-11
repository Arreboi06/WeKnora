package container

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/database"
	"github.com/stretchr/testify/require"
)

func TestInitDatabaseMigrationFailureStopsStartup(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	testRoot := t.TempDir()
	migrationsRoot := filepath.Join(testRoot, "migrations", "sqlite")
	require.NoError(t, os.MkdirAll(migrationsRoot, 0o755))

	entries, err := os.ReadDir(filepath.Join(repoRoot, "migrations", "sqlite"))
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(repoRoot, "migrations", "sqlite", entry.Name()))
		require.NoError(t, readErr)
		require.NoError(t, os.WriteFile(filepath.Join(migrationsRoot, entry.Name()), data, 0o600))
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(migrationsRoot, "999999_intentionally_broken.up.sql"),
		[]byte("CREATE TABLE intentionally_broken (id INTEGER;\n"),
		0o600,
	))

	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(testRoot))
	t.Cleanup(func() { _ = os.Chdir(previousDir) })

	t.Setenv("DB_DRIVER", "sqlite")
	t.Setenv("DB_PATH", filepath.Join(testRoot, "failed.db"))
	t.Setenv("AUTO_MIGRATE", "true")
	t.Setenv("AUTO_RECOVER_DIRTY", "false")

	db, err := initDatabase(nil)
	require.Error(t, err)
	require.Nil(t, db, "a failed migration must not hand a database to application startup")
	require.NotEmpty(t, database.CachedMigrationError())
	_, dirty, known := database.CachedMigrationVersion()
	require.True(t, known)
	require.True(t, dirty, "failed migration must preserve dirty state for explicit operator repair")
}
