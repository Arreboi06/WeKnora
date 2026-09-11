package repository

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedCitationProfileACLRuntimeStateForRepositoryTest equips partial-schema
// repository fixtures with the same default-off singleton that versioned
// migrations install. Permission mutations intentionally fail closed when this
// marker is absent, so tests must model the production prerequisite rather than
// weakening that behavior.
func seedCitationProfileACLRuntimeStateForRepositoryTest(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&types.CitationProfileACLRuntimeState{}))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&types.CitationProfileACLRuntimeState{
		ID:                   1,
		Enabled:              false,
		TransitionGeneration: 0,
		ChangedAt:            now,
		UpdatedAt:            now,
	}).Error)
}
