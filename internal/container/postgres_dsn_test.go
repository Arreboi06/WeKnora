package container

import (
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestBuildPostgresDSNsKeepsGORMAndMigratorOnSameDatabase(t *testing.T) {
	tests := []struct {
		name     string
		password string
	}{
		{name: "trust auth empty password", password: ""},
		{name: "reserved password characters", password: "p@ss word:/?#%+"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gormDSN, migrationDSN := buildPostgresDSNs(
				"2001:db8::1", "5432", "postgres", tc.password,
				"weknora_isolated", true,
			)

			gormConfig, err := pgx.ParseConfig(gormDSN)
			require.NoError(t, err)
			require.Equal(t, "2001:db8::1", gormConfig.Host)
			require.Equal(t, uint16(5432), gormConfig.Port)
			require.Equal(t, "postgres", gormConfig.User)
			require.Equal(t, tc.password, gormConfig.Password)
			require.Equal(t, "weknora_isolated", gormConfig.Database)
			// pgx consumes sslmode into TLSConfig rather than preserving it as a
			// runtime parameter; nil is the parsed representation of disable.
			require.Nil(t, gormConfig.TLSConfig)
			require.Equal(t, "UTC", gormConfig.RuntimeParams["TimeZone"])

			migrationURL, err := url.Parse(migrationDSN)
			require.NoError(t, err)
			require.Equal(t, "2001:db8::1", migrationURL.Hostname())
			require.Equal(t, "5432", migrationURL.Port())
			require.Equal(t, "/weknora_isolated", migrationURL.Path)
			require.Equal(t, "postgres", migrationURL.User.Username())
			actualPassword, passwordSet := migrationURL.User.Password()
			require.Equal(t, tc.password != "", passwordSet)
			require.Equal(t, tc.password, actualPassword)
			require.Equal(t, "disable", migrationURL.Query().Get("sslmode"))
			require.Equal(t, "-c app.skip_embedding=true", migrationURL.Query().Get("options"))
		})
	}
}

func TestBuildPostgresDSNsCanRequireEmbeddingMigrations(t *testing.T) {
	_, migrationDSN := buildPostgresDSNs(
		"127.0.0.1", "5432", "postgres", "secret", "weknora", false,
	)
	migrationURL, err := url.Parse(migrationDSN)
	require.NoError(t, err)
	require.Equal(t, "-c app.skip_embedding=false", migrationURL.Query().Get("options"))
}
