//go:build cgo

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileACLAuthorityF7ReturnsExactAPIKeyValidityHorizon(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keyID     uint64
		expiresAt *time.Time
	}{
		{
			name:      "expiring key returns durable expiration",
			keyID:     141,
			expiresAt: citationProfileACLAuthorityFutureTime(),
		},
		{
			name:  "non-expiring key has no external horizon",
			keyID: 142,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newCitationProfileACLAuthorityF7TestDB(t)
			require.NoError(t, db.Create(&types.KnowledgeBase{
				ID:       "kb-api-validity-f7",
				Name:     "API validity authority fixture",
				TenantID: 7,
			}).Error)
			tenantID := uint64(7)
			require.NoError(t, db.Create(&types.TenantAPIKey{
				ID:         tc.keyID,
				TenantID:   &tenantID,
				ScopeType:  types.APIKeyScopeTenant,
				Name:       "API validity key",
				KeyHash:    "hash-api-validity",
				APIKey:     "sk-api-validity",
				FullAccess: true,
				ExpiresAt:  tc.expiresAt,
			}).Error)

			result, err := NewCitationProfileACLAuthority(db).CheckCitationProfileACL(
				context.Background(),
				citationProfileACLAuthorityF7APIScope(7, 7, tc.keyID, "kb-api-validity-f7"),
			)
			require.NoError(t, err)
			require.Equal(t, types.CitationProfileACLDecisionAllow, result.Decision)
			if tc.expiresAt == nil {
				require.Nil(t, result.ValidUntil)
			} else {
				require.NotNil(t, result.ValidUntil)
				require.True(t, tc.expiresAt.Equal(*result.ValidUntil))
			}
		})
	}
}

func TestCitationProfileACLAuthorityF7InactiveWebUserDenies(t *testing.T) {
	db := newCitationProfileACLAuthorityF7TestDB(t)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&types.KnowledgeBase{
		ID:       "kb-inactive-web-f7",
		Name:     "Inactive web-user authority fixture",
		TenantID: 7,
	}).Error)
	require.NoError(t, db.Model(&types.User{}).
		Where("id = ?", "user-owner-f7").
		Update("is_active", false).Error)
	require.NoError(t, db.Create(&types.TenantMember{
		UserID:   "user-owner-f7",
		TenantID: 7,
		Role:     types.TenantRoleViewer,
		Status:   types.TenantMemberStatusActive,
		JoinedAt: now,
	}).Error)

	result, err := NewCitationProfileACLAuthority(db).CheckCitationProfileACL(
		context.Background(),
		citationProfileACLAuthorityF7WebScope(
			7, 7, "user-owner-f7", "kb-inactive-web-f7", types.CitationProfileACLAccessPathOwner,
		),
	)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileACLDecisionDeny, result.Decision)
}

func citationProfileACLAuthorityFutureTime() *time.Time {
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	return &future
}
