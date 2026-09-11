//go:build cgo

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCitationProfileGetScopeStatusProjectsACLCurrentFromDatabaseClock(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	dbNow := time.Now().UTC()

	current := citationProfileACLTestScope("scope-f7-status-db-current", dbNow)
	current.KnowledgeBaseID = "kb-f7-status-db-current"
	checkedAt := dbNow.Add(-time.Minute)
	validUntil := dbNow.Add(time.Hour)
	current.ACLCheckedAt = &checkedAt
	current.NextACLCheckAt = &validUntil

	expired := citationProfileACLTestScope("scope-f7-status-db-expired", dbNow)
	expired.KnowledgeBaseID = "kb-f7-status-db-expired"
	expiredCheckedAt := dbNow.Add(-2 * time.Hour)
	expiredAt := dbNow.Add(-time.Minute)
	expired.ACLCheckedAt = &expiredCheckedAt
	expired.NextACLCheckAt = &expiredAt
	require.NoError(t, db.Create(current).Error)
	require.NoError(t, db.Create(expired).Error)

	gotCurrent, err := repo.GetScopeStatus(
		context.Background(), current.TenantID, current.SubjectID, current.KnowledgeBaseID,
	)
	require.NoError(t, err)
	require.NotNil(t, gotCurrent)
	require.True(t, gotCurrent.ACLCurrent)

	gotExpired, err := repo.GetScopeStatus(
		context.Background(), expired.TenantID, expired.SubjectID, expired.KnowledgeBaseID,
	)
	require.NoError(t, err)
	require.NotNil(t, gotExpired)
	require.False(t, gotExpired.ACLCurrent)
}
