//go:build cgo

package repository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCitationProfileACLF7SharedMutationLocksAllScopesInOneGlobalOrder(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	now := time.Date(2026, 9, 10, 23, 0, 0, 0, time.UTC)

	agentScope := citationProfileACLMutationF7WebScope("z-agent-scope", 200, 100, "user-lock-order", "kb-agent", now)
	agentScope.ACLAccessPath = types.CitationProfileACLAccessPathAgentShare
	agentScope.ACLAccessPathID = "agent-lock-order"
	kbScope := citationProfileACLMutationF7WebScope("a-kb-scope", 200, 100, "user-lock-order", "kb-share", now)
	require.NoError(t, db.Create(agentScope).Error)
	require.NoError(t, db.Create(kbScope).Error)

	var mu sync.Mutex
	var scopeSelects []string
	callbackName := "p36:f7:capture-scope-lock-order"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		sql := strings.ToLower(strings.TrimSpace(tx.Statement.SQL.String()))
		if strings.HasPrefix(sql, "select") && strings.Contains(sql, "citation_profile_scopes") {
			mu.Lock()
			scopeSelects = append(scopeSelects, sql)
			mu.Unlock()
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	require.NoError(t, db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		return invalidateCitationProfileSharedACLForTenantTx(tx, 100, now)
	}))

	mu.Lock()
	captured := append([]string(nil), scopeSelects...)
	mu.Unlock()
	require.Len(t, captured, 1, "one permission mutation must collect and lock its full target set in one query")
	normalized := strings.Join(strings.Fields(captured[0]), " ")
	require.Contains(t, normalized, "order by tenant_id asc,id asc")

	for _, id := range []string{agentScope.ID, kbScope.ID} {
		var stored types.CitationProfileScope
		require.NoError(t, db.First(&stored, "id = ?", id).Error)
		require.Equal(t, uint64(10), stored.ACLGeneration)
		require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)
	}
}
