//go:build t4pg

package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestCitationProfilePostgresF7ACLRegression(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGSchema(db))
	require.NoError(t, citationProfilePGF7EnsureACLSchema(db))
	require.NoError(t, db.AutoMigrate(&types.Tenant{}))

	ctx := context.Background()

	t.Run("concurrent feature enable coalesces one startup fence", func(t *testing.T) {
		const sourceTenantID = uint64(28190)
		now := time.Now().UTC().Truncate(time.Microsecond)
		scope := citationProfilePGF7WebScope(
			citationProfilePGF7EarlyID(90), sourceTenantID, sourceTenantID,
			"user-f7-startup-coalesce", uuid.NewString(), now,
		)
		require.NoError(t, db.Create(scope).Error)
		t.Cleanup(func() {
			citationProfilePGF7Cleanup(t, db, sourceTenantID)
			_ = db.Exec(`UPDATE citation_profile_acl_runtime_state
				SET enabled = FALSE, transition_generation = 0,
				    changed_at = statement_timestamp(), updated_at = statement_timestamp()
				WHERE id = 1`).Error
		})
		require.NoError(t, db.Exec(`UPDATE citation_profile_acl_runtime_state
			SET enabled = FALSE, transition_generation = 400,
			    changed_at = statement_timestamp(), updated_at = statement_timestamp()
			WHERE id = 1`).Error)

		blocker, err := sqlDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = blocker.Rollback() })
		_, err = blocker.ExecContext(ctx, `SELECT id
			FROM citation_profile_acl_runtime_state
			WHERE id = 1
			FOR UPDATE`)
		require.NoError(t, err)

		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func() {
				errs <- (&citationProfileRepository{db: db}).PrepareCitationProfileACLFeatureState(ctx, true)
			}()
		}
		require.Eventually(t, func() bool {
			var waiting int64
			err := db.Raw(`SELECT COUNT(*)
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND state = 'active'
				  AND wait_event_type = 'Lock'
				  AND query ~* 'citation_profile_acl_runtime_state'
				  AND query ~* 'for[[:space:]]+update'`).Scan(&waiting).Error
			return err == nil && waiting == 2
		}, 5*time.Second, 20*time.Millisecond,
			"both startup replicas must be observed waiting on the marker FOR UPDATE lock")
		require.NoError(t, blocker.Commit())
		for i := 0; i < 2; i++ {
			require.NoError(t, <-errs)
		}

		var state types.CitationProfileACLRuntimeState
		require.NoError(t, db.First(&state, "id = ?", 1).Error)
		require.True(t, state.Enabled)
		require.Equal(t, uint64(401), state.TransitionGeneration,
			"two concurrent replicas must linearize into one enabled transition")
		var stored types.CitationProfileScope
		require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
		require.Equal(t, scope.ACLGeneration+1, stored.ACLGeneration)
		require.Equal(t, scope.ProfileReadVersion+1, stored.ProfileReadVersion)
		require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState)

		require.NoError(t, (&citationProfileRepository{db: db}).
			PrepareCitationProfileACLFeatureState(ctx, false))
		require.NoError(t, db.First(&state, "id = ?", 1).Error)
		require.True(t, state.Enabled,
			"a locally disabled PostgreSQL replica must not clear an enabled fleet marker")
		require.Equal(t, uint64(401), state.TransitionGeneration)
	})

	t.Run("concurrent claims and mutation fences are identity isolated", func(t *testing.T) {
		const (
			sourceTenantID = uint64(28101)
			authTenantID   = uint64(18101)
			otherSourceID  = uint64(28102)
		)
		now := time.Date(2026, 9, 10, 18, 0, 0, 0, time.UTC)
		kbID := uuid.NewString()
		target := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(1), sourceTenantID, authTenantID, "user-f7-target", kbID, now)
		otherTenant := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(2), otherSourceID, authTenantID, target.SubjectID, kbID, now)
		otherSubject := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(3), sourceTenantID, authTenantID, "user-f7-other", kbID, now)
		key991 := citationProfilePGF7APIScope(citationProfilePGF7EarlyID(4), sourceTenantID, authTenantID, 991, kbID, now)
		key992 := citationProfilePGF7APIScope(citationProfilePGF7EarlyID(5), sourceTenantID, authTenantID, 992, kbID, now)
		key991OtherTenant := citationProfilePGF7APIScope(citationProfilePGF7EarlyID(6), otherSourceID, authTenantID, 991, kbID, now)
		scopes := []*types.CitationProfileScope{target, otherTenant, otherSubject, key991, key992, key991OtherTenant}
		for _, scope := range scopes {
			require.NoError(t, db.Create(scope).Error)
		}
		t.Cleanup(func() { citationProfilePGF7Cleanup(t, db, sourceTenantID, otherSourceID) })

		repos := []*citationProfileRepository{{db: db}, {db: db}}
		type claimResult struct {
			claims []types.CitationProfileACLClaim
			err    error
		}
		start := make(chan struct{})
		results := make(chan claimResult, len(repos))
		var workers sync.WaitGroup
		for i, repo := range repos {
			workers.Add(1)
			go func(worker int, candidate *citationProfileRepository) {
				defer workers.Done()
				<-start
				claims, claimErr := candidate.ClaimCitationProfileACLScopes(
					ctx, fmt.Sprintf("f7-pg-worker-%d", worker), 3, now, time.Minute,
				)
				results <- claimResult{claims: claims, err: claimErr}
			}(i, repo)
		}
		close(start)
		workers.Wait()
		close(results)

		claimsByScope := make(map[string]types.CitationProfileACLClaim, len(scopes))
		for result := range results {
			require.NoError(t, result.err)
			require.Len(t, result.claims, 3)
			for _, claim := range result.claims {
				_, duplicate := claimsByScope[claim.ScopeID]
				require.False(t, duplicate, "SKIP LOCKED workers must never receive the same ACL scope")
				claimsByScope[claim.ScopeID] = claim
			}
		}
		require.Len(t, claimsByScope, len(scopes))
		for _, scope := range scopes {
			_, ok := claimsByScope[scope.ID]
			require.True(t, ok, "expected concurrent claim for %s", scope.ID)
		}

		staleTargetClaim := claimsByScope[target.ID]
		require.NoError(t, repos[0].InvalidateCitationProfileACL(ctx, types.CitationProfileACLMutation{
			PrincipalType:         types.PrincipalWebUser,
			PrincipalID:           target.SubjectID,
			AuthenticatedTenantID: authTenantID,
			SourceTenantID:        sourceTenantID,
			KnowledgeBaseID:       kbID,
			AccessPath:            types.CitationProfileACLAccessPathKBShare,
			AccessPathID:          kbID,
		}))
		err = repos[1].ApplyCitationProfileACLResult(
			ctx, &staleTargetClaim, types.CitationProfileACLDecisionAllow,
			now.Add(time.Second), now.Add(time.Hour),
		)
		require.ErrorIs(t, err, types.ErrCitationProfileACLLeaseLost)

		var storedTarget types.CitationProfileScope
		require.NoError(t, db.First(&storedTarget, "id = ?", target.ID).Error)
		require.Equal(t, target.ACLGeneration+1, storedTarget.ACLGeneration)
		require.Equal(t, staleTargetClaim.Scope.ProfileReadVersion+1, storedTarget.ProfileReadVersion)
		require.Equal(t, types.CitationProfileACLStateUnknown, storedTarget.ACLCheckState)
		require.Empty(t, storedTarget.ACLCheckLeaseToken)

		for _, untouched := range []*types.CitationProfileScope{otherTenant, otherSubject, key991, key992, key991OtherTenant} {
			var stored types.CitationProfileScope
			require.NoError(t, db.First(&stored, "id = ?", untouched.ID).Error)
			claim := claimsByScope[untouched.ID]
			require.Equal(t, untouched.ACLGeneration, stored.ACLGeneration, untouched.ID)
			require.Equal(t, claim.Scope.ProfileReadVersion, stored.ProfileReadVersion, untouched.ID)
			require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState, untouched.ID)
			require.Equal(t, claim.LeaseToken, stored.ACLCheckLeaseToken, untouched.ID)
		}

		require.NoError(t, repos[0].InvalidateCitationProfileACL(ctx, types.CitationProfileACLMutation{
			AuthenticatedTenantID: authTenantID,
			APIKeyID:              991,
			SourceTenantID:        sourceTenantID,
			KnowledgeBaseID:       kbID,
		}))
		var storedKey991, storedKey992, storedOtherTenant types.CitationProfileScope
		require.NoError(t, db.First(&storedKey991, "id = ?", key991.ID).Error)
		require.NoError(t, db.First(&storedKey992, "id = ?", key992.ID).Error)
		require.NoError(t, db.First(&storedOtherTenant, "id = ?", key991OtherTenant.ID).Error)
		require.Equal(t, key991.ACLGeneration+1, storedKey991.ACLGeneration)
		require.Equal(t, types.CitationProfileACLStateUnknown, storedKey991.ACLCheckState)
		require.Equal(t, key992.ACLGeneration, storedKey992.ACLGeneration, "a different API key must stay current")
		require.Equal(t, key991OtherTenant.ACLGeneration, storedOtherTenant.ACLGeneration, "the same key identity in a different source tenant must stay current")
	})

	t.Run("tenant API key revoke atomically invalidates authority and fences stale allow", func(t *testing.T) {
		const (
			tenantID = uint64(28141)
			keyID    = uint64(7900141)
		)
		claimAt := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
		kbID := uuid.NewString()
		require.NoError(t, createCitationProfilePGFactATables(db))
		require.NoError(t, db.AutoMigrate(&types.TenantAPIKey{}))
		require.NoError(t, db.Create(&types.Tenant{
			ID: tenantID, Name: "f7 API authority tenant", Status: "active",
			CreatedAt: claimAt.Add(-time.Hour), UpdatedAt: claimAt.Add(-time.Hour),
		}).Error)
		require.NoError(t, db.Exec(
			`INSERT INTO knowledge_bases (id, name, description, tenant_id, embedding_model_id, summary_model_id, rerank_model_id, created_at, updated_at)
			 VALUES (?, ?, '', ?, '', '', '', ?, ?)`,
			kbID, "f7 API owner KB", tenantID, claimAt.Add(-time.Hour), claimAt.Add(-time.Hour),
		).Error)
		key := &types.TenantAPIKey{
			ID:               keyID,
			TenantID:         func() *uint64 { value := tenantID; return &value }(),
			ScopeType:        types.APIKeyScopeTenant,
			Name:             "f7 PostgreSQL authority key",
			KeyHash:          "f7-pg-" + uuid.NewString(),
			FullAccess:       true,
			KnowledgeBaseIDs: types.StringArray{kbID},
			Capabilities:     types.StringArray{string(types.APIKeyCapabilityRetrieve)},
			CreatedAt:        claimAt.Add(-time.Hour),
			UpdatedAt:        claimAt.Add(-time.Hour),
		}
		require.NoError(t, db.Create(key).Error)
		scope := citationProfilePGF7APIScope(citationProfilePGF7EarlyID(41), tenantID, tenantID, key.ID, kbID, claimAt)
		scope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
		scope.ACLAccessPathID = ""
		require.NoError(t, db.Create(scope).Error)
		t.Cleanup(func() {
			citationProfilePGF7Cleanup(t, db, tenantID)
			require.NoError(t, db.Unscoped().Where("id = ?", key.ID).Delete(&types.TenantAPIKey{}).Error)
			require.NoError(t, db.Exec("DELETE FROM knowledge_bases WHERE id = ? AND tenant_id = ?", kbID, tenantID).Error)
			require.NoError(t, db.Unscoped().Where("id = ?", tenantID).Delete(&types.Tenant{}).Error)
		})

		authority := NewCitationProfileACLAuthority(db)
		decision, err := authority.CheckCitationProfileACL(ctx, scope)
		require.NoError(t, err)
		require.Equal(t, types.CitationProfileACLDecisionAllow, decision.Decision)

		aclRepo := &citationProfileRepository{db: db}
		claims, err := aclRepo.ClaimCitationProfileACLScopes(ctx, "f7-pg-api-revoke-old", 100, claimAt, time.Minute)
		require.NoError(t, err)
		var staleAllow types.CitationProfileACLClaim
		foundTarget := false
		for _, claim := range claims {
			if claim.ScopeID == scope.ID {
				staleAllow = claim
				foundTarget = true
				break
			}
		}
		require.True(t, foundTarget, "global ACL queue claim must include the target scope among unrelated due work")

		apiKeys := NewTenantAPIKeyRepository(db, &types.CitationProfileConfig{Enabled: true})
		require.NoError(t, apiKeys.RevokeAPIKey(ctx, tenantID, key.ID))

		var revokedKey types.TenantAPIKey
		var invalidated types.CitationProfileScope
		require.NoError(t, db.First(&revokedKey, "id = ?", key.ID).Error)
		require.NoError(t, db.First(&invalidated, "id = ?", scope.ID).Error)
		require.NotNil(t, revokedKey.RevokedAt)
		require.Equal(t, scope.ACLGeneration+1, invalidated.ACLGeneration)
		require.Equal(t, staleAllow.Scope.ProfileReadVersion+1, invalidated.ProfileReadVersion)
		require.Equal(t, types.CitationProfileACLStateUnknown, invalidated.ACLCheckState)
		require.Empty(t, invalidated.ACLCheckLeaseToken)
		require.Nil(t, invalidated.ACLCheckLeaseUntil)

		err = aclRepo.ApplyCitationProfileACLResult(
			ctx, &staleAllow, types.CitationProfileACLDecisionAllow,
			claimAt.Add(2*time.Minute), claimAt.Add(time.Hour),
		)
		require.ErrorIs(t, err, types.ErrCitationProfileACLLeaseLost)

		decision, err = authority.CheckCitationProfileACL(ctx, &invalidated)
		require.NoError(t, err)
		require.Equal(t, types.CitationProfileACLDecisionDeny, decision.Decision)

		refreshAt := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Microsecond)
		claims, err = aclRepo.ClaimCitationProfileACLScopes(ctx, "f7-pg-api-revoke-new", 1, refreshAt, time.Minute)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, scope.ID, claims[0].ScopeID)
		require.Equal(t, invalidated.ACLGeneration, claims[0].Generation)
		require.NoError(t, aclRepo.ApplyCitationProfileACLResult(ctx, &claims[0], decision.Decision, refreshAt, time.Time{}))

		var fenced types.CitationProfileScope
		require.NoError(t, db.First(&fenced, "id = ?", scope.ID).Error)
		require.False(t, fenced.Enabled)
		require.Equal(t, types.CitationProfileACLStateDenied, fenced.ACLCheckState)
		require.NotNil(t, fenced.FencedAt)
		require.Equal(t, types.CitationProfileFenceReasonACLDenied, fenced.FenceReason)
	})

	t.Run("unknown pauses work without spending attempts and allow resumes budget", func(t *testing.T) {
		const tenantID = uint64(28111)
		now, err := citationProfileDatabaseNow(db)
		require.NoError(t, err)
		scope := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(11), tenantID, tenantID, "user-f7-budget", uuid.NewString(), now)
		outbox := citationProfilePGF7Outbox(scope, citationProfilePGF7EarlyID(101), uuid.NewString(), now.Add(-30*time.Hour), 4)
		outbox.CreatedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		outbox.UpdatedAt = outbox.CreatedAt
		require.NoError(t, db.Create(scope).Error)
		require.NoError(t, db.Create(&outbox).Error)
		t.Cleanup(func() { citationProfilePGF7Cleanup(t, db, tenantID) })

		repo := &citationProfileRepository{db: db}
		claims, err := repo.ClaimCitationProfileACLScopes(ctx, "f7-pg-pause", 1, now, time.Minute)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, scope.ID, claims[0].ScopeID)
		require.NoError(t, repo.ApplyCitationProfileACLResult(
			ctx, &claims[0], types.CitationProfileACLDecisionUnknown, now, now.Add(time.Minute),
		))

		blocked, err := repo.ClaimCitationProfileEventOutbox(ctx, "f7-pg-evidence-blocked", 1, now.Add(48*time.Hour), time.Minute)
		require.NoError(t, err)
		for _, claim := range blocked {
			require.NotEqual(t, outbox.ID, claim.ID, "UNKNOWN authority must make this outbox ineligible")
		}
		var paused types.CitationProfileEventOutbox
		require.NoError(t, db.First(&paused, "id = ?", outbox.ID).Error)
		require.Equal(t, types.CitationProfileOutboxStatusPending, paused.Status)
		require.Equal(t, 4, paused.AttemptCount)
		require.NotNil(t, paused.RetryBudgetPausedAt)
		require.Zero(t, paused.RetryBudgetPausedSeconds)
		require.Nil(t, paused.DeadletterAt)

		resumeAt, err := citationProfileDatabaseNow(db)
		require.NoError(t, err)
		simulatedPausedAt := resumeAt.Add(-48*time.Hour - 2*time.Second)
		require.NoError(t, db.Model(&types.CitationProfileEventOutbox{}).
			Where("id = ?", outbox.ID).
			Update("retry_budget_paused_at", simulatedPausedAt).Error)
		require.NoError(t, db.Model(&types.CitationProfileScope{}).
			Where("id = ?", scope.ID).
			Update("next_acl_check_at", resumeAt.Add(-time.Second)).Error)
		claims, err = repo.ClaimCitationProfileACLScopes(ctx, "f7-pg-resume", 1, resumeAt, time.Minute)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, scope.ID, claims[0].ScopeID)
		require.NoError(t, repo.ApplyCitationProfileACLResult(
			ctx, &claims[0], types.CitationProfileACLDecisionAllow, resumeAt, resumeAt.Add(time.Hour),
		))

		var resumed types.CitationProfileEventOutbox
		require.NoError(t, db.First(&resumed, "id = ?", outbox.ID).Error)
		require.Nil(t, resumed.RetryBudgetPausedAt)
		require.GreaterOrEqual(t, resumed.RetryBudgetPausedSeconds, int64((48*time.Hour)/time.Second))
		require.Equal(t, 4, resumed.AttemptCount)
		ready, err := repo.ClaimCitationProfileEventOutbox(ctx, "f7-pg-evidence-resumed", 1, resumeAt, time.Minute)
		require.NoError(t, err)
		require.Len(t, ready, 1)
		require.Equal(t, outbox.ID, ready[0].ID)
		require.Equal(t, 4, ready[0].AttemptCount, "ACL refresh must not consume an evidence attempt")
	})

	t.Run("deny fences pending work and revokes downloadable exports", func(t *testing.T) {
		const tenantID = uint64(28121)
		now := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
		scope := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(21), tenantID, tenantID, "user-f7-deny", uuid.NewString(), now)
		scope.PendingEventCount = 1
		event := citationProfilePGF7Event(scope, uuid.NewString(), now.Add(-time.Minute))
		outbox := citationProfilePGF7Outbox(scope, uuid.NewString(), event.ID, now.Add(-time.Minute), 2)
		export := citationProfilePGF7Export(scope, uuid.NewString(), now)
		require.NoError(t, db.Create(scope).Error)
		require.NoError(t, db.Create(&event).Error)
		require.NoError(t, db.Create(&outbox).Error)
		require.NoError(t, db.Create(&export).Error)
		t.Cleanup(func() { citationProfilePGF7Cleanup(t, db, tenantID) })

		repo := &citationProfileRepository{db: db}
		claims, err := repo.ClaimCitationProfileACLScopes(ctx, "f7-pg-deny", 1, now, time.Minute)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, scope.ID, claims[0].ScopeID)
		require.NoError(t, repo.ApplyCitationProfileACLResult(
			ctx, &claims[0], types.CitationProfileACLDecisionDeny, now.Add(time.Second), time.Time{},
		))

		var storedScope types.CitationProfileScope
		var storedEvent types.CitationProfileEvent
		var storedOutbox types.CitationProfileEventOutbox
		var storedExport types.CitationProfileOperation
		require.NoError(t, db.First(&storedScope, "id = ?", scope.ID).Error)
		require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
		require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
		require.NoError(t, db.First(&storedExport, "id = ?", export.ID).Error)
		require.False(t, storedScope.Enabled)
		require.Equal(t, types.CitationProfileACLStateDenied, storedScope.ACLCheckState)
		require.NotNil(t, storedScope.FencedAt)
		require.Equal(t, types.CitationProfileFenceReasonACLDenied, storedScope.FenceReason)
		require.NotEqual(t, types.CitationProfileEventStatusPendingResolution, storedEvent.Status)
		require.Equal(t, types.CitationProfileOutboxStatusDeadletter, storedOutbox.Status)
		require.NotNil(t, storedOutbox.DeadletterAt)
		require.Equal(t, 2, storedOutbox.AttemptCount)
		require.Equal(t, types.CitationProfileOperationStatusRevoked, storedExport.Status)
		require.Empty(t, storedExport.ArtifactURI)
	})

	t.Run("DENY then shared blind delete persists deletion for the exact subject", func(t *testing.T) {
		const (
			authTenantID   = uint64(18131)
			sourceTenantID = uint64(28131)
		)
		now := time.Date(2026, 9, 10, 21, 0, 0, 0, time.UTC)
		kbID := uuid.NewString()
		target := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(31), sourceTenantID, authTenantID, "user-f7-shared", kbID, now)
		otherSubject := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(32), sourceTenantID, authTenantID, "user-f7-shared-other", kbID, now)
		require.NoError(t, db.Create(target).Error)
		require.NoError(t, db.Create(otherSubject).Error)
		t.Cleanup(func() { citationProfilePGF7Cleanup(t, db, sourceTenantID) })

		repo := &citationProfileRepository{db: db}
		claims, err := repo.ClaimCitationProfileACLScopes(ctx, "f7-pg-deny-before-blind-delete", 1, now, time.Minute)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, target.ID, claims[0].ScopeID)
		require.NoError(t, repo.ApplyCitationProfileACLResult(
			ctx, &claims[0], types.CitationProfileACLDecisionDeny, now.Add(time.Second), time.Time{},
		))

		var deniedTarget types.CitationProfileScope
		require.NoError(t, db.First(&deniedTarget, "id = ?", target.ID).Error)
		require.False(t, deniedTarget.Enabled)
		require.NotNil(t, deniedTarget.FencedAt)
		require.Nil(t, deniedTarget.DeletedAt)

		operation, err := repo.RequestBlindDelete(ctx, authTenantID, target.SubjectID, kbID)
		require.NoError(t, err)
		require.NotNil(t, operation)
		require.Equal(t, types.CitationProfileOperationStatusAccepted, operation.Status)
		require.Equal(t, target.ID, operation.ScopeID)
		require.Equal(t, sourceTenantID, operation.TenantID)

		var deletedTarget, untouchedSubject types.CitationProfileScope
		require.NoError(t, db.Unscoped().First(&deletedTarget, "id = ?", target.ID).Error)
		require.NoError(t, db.First(&untouchedSubject, "id = ?", otherSubject.ID).Error)
		require.False(t, deletedTarget.Enabled)
		require.NotNil(t, deletedTarget.FencedAt)
		require.NotNil(t, deletedTarget.DeletedAt)
		require.Equal(t, operation.ID, deletedTarget.DeleteRequestID)
		require.Equal(t, types.CitationProfileFenceReasonACLDenied, deletedTarget.FenceReason,
			"blind deletion must preserve the original authority-loss audit reason")
		require.True(t, untouchedSubject.Enabled)
		require.Nil(t, untouchedSubject.FencedAt)
		require.Nil(t, untouchedSubject.DeletedAt)
		var operationCount int64
		require.NoError(t, db.Model(&types.CitationProfileOperation{}).
			Where("id = ? AND scope_id = ? AND operation_type = ?", operation.ID, target.ID, types.CitationOperationDeleteBlind).
			Count(&operationCount).Error)
		require.Equal(t, int64(1), operationCount)
	})

	t.Run("revoked API key before first enrollment stays UNKNOWN until authoritative DENY", func(t *testing.T) {
		const (
			tenantID = uint64(28171)
			keyID    = uint64(7900171)
		)
		now := time.Now().UTC().Truncate(time.Microsecond)
		kbID := uuid.NewString()
		require.NoError(t, createCitationProfilePGFactATables(db))
		require.NoError(t, db.AutoMigrate(&types.TenantAPIKey{}))
		require.NoError(t, db.Create(&types.Tenant{
			ID: tenantID, Name: "f7 revoked authority tenant", Status: "active",
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
		}).Error)
		require.NoError(t, db.Exec(
			`INSERT INTO knowledge_bases (id, name, description, tenant_id, embedding_model_id, summary_model_id, rerank_model_id, created_at, updated_at)
			 VALUES (?, ?, '', ?, '', '', '', ?, ?)`,
			kbID, "f7 revoked-before-enrollment KB", tenantID, now.Add(-time.Hour), now.Add(-time.Hour),
		).Error)
		key := &types.TenantAPIKey{
			ID:               keyID,
			TenantID:         func() *uint64 { value := tenantID; return &value }(),
			ScopeType:        types.APIKeyScopeTenant,
			Name:             "f7 revoked before enrollment key",
			KeyHash:          "f7-pg-pre-revoke-" + uuid.NewString(),
			FullAccess:       true,
			KnowledgeBaseIDs: types.StringArray{kbID},
			Capabilities:     types.StringArray{string(types.APIKeyCapabilityRetrieve)},
			CreatedAt:        now.Add(-time.Hour),
			UpdatedAt:        now.Add(-time.Hour),
		}
		require.NoError(t, db.Create(key).Error)
		t.Cleanup(func() {
			citationProfilePGF7Cleanup(t, db, tenantID)
			require.NoError(t, db.Unscoped().Where("id = ?", key.ID).Delete(&types.TenantAPIKey{}).Error)
			require.NoError(t, db.Exec("DELETE FROM knowledge_bases WHERE id = ? AND tenant_id = ?", kbID, tenantID).Error)
			require.NoError(t, db.Unscoped().Where("id = ?", tenantID).Delete(&types.Tenant{}).Error)
		})

		capturedContext := citationProfilePGF7APIContext(tenantID, key.ID)
		apiKeys := NewTenantAPIKeyRepository(db, &types.CitationProfileConfig{Enabled: true})
		require.NoError(t, apiKeys.RevokeAPIKey(ctx, tenantID, key.ID))

		repo := &citationProfileRepository{db: db}
		zero := uint64(0)
		subjectID := fmt.Sprintf("%s%d:%d", types.SessionOwnerAPITenantKeyPrefix, tenantID, key.ID)
		scope, err := repo.SetEnrollment(capturedContext, tenantID, subjectID, kbID, true, &zero, uuid.NewString())
		require.NoError(t, err)
		require.NotNil(t, scope)
		require.Equal(t, types.CitationProfileACLStateUnknown, scope.ACLCheckState)
		require.NotNil(t, scope.NextACLCheckAt)
		_, err = repo.ListNodes(capturedContext, tenantID, subjectID, kbID, nil, 20)
		require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)

		authority := NewCitationProfileACLAuthority(db)
		decision, err := authority.CheckCitationProfileACL(ctx, scope)
		require.NoError(t, err)
		require.Equal(t, types.CitationProfileACLDecisionDeny, decision.Decision)
		claims, err := repo.ClaimCitationProfileACLScopes(ctx, "f7-pg-pre-revoke", 1, now.Add(time.Minute), time.Minute)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, scope.ID, claims[0].ScopeID)
		require.NoError(t, repo.ApplyCitationProfileACLResult(
			ctx, &claims[0], decision.Decision, now.Add(time.Minute), time.Time{},
		))
		var fenced types.CitationProfileScope
		require.NoError(t, db.First(&fenced, "id = ?", scope.ID).Error)
		require.False(t, fenced.Enabled)
		require.Equal(t, types.CitationProfileACLStateDenied, fenced.ACLCheckState)
		require.NotNil(t, fenced.FencedAt)
	})

	t.Run("explicit enrollment after regrant archives DENY epoch and creates UNKNOWN epoch", func(t *testing.T) {
		const tenantID = uint64(28161)
		now := time.Date(2026, 9, 10, 22, 30, 0, 0, time.UTC)
		kbID := uuid.NewString()
		subjectID := "user-f7-pg-regrant"
		oldScope := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(61), tenantID, tenantID, subjectID, kbID, now)
		oldScope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
		oldScope.ACLAccessPathID = ""
		require.NoError(t, db.Create(oldScope).Error)
		t.Cleanup(func() { citationProfilePGF7Cleanup(t, db, tenantID) })

		repo := &citationProfileRepository{db: db}
		claims, err := repo.ClaimCitationProfileACLScopes(ctx, "f7-pg-regrant-deny", 1, now, time.Minute)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.Equal(t, oldScope.ID, claims[0].ScopeID)
		require.NoError(t, repo.ApplyCitationProfileACLResult(
			ctx, &claims[0], types.CitationProfileACLDecisionDeny, now.Add(time.Second), time.Time{},
		))

		zero := uint64(0)
		newScope, err := repo.SetEnrollment(
			citationProfilePGOwnerContext(ctx, tenantID, subjectID),
			tenantID, subjectID, kbID, true, &zero, uuid.NewString(),
		)
		require.NoError(t, err)
		require.NotNil(t, newScope)
		require.NotEqual(t, oldScope.ID, newScope.ID)
		require.NotEqual(t, oldScope.SubjectEpoch, newScope.SubjectEpoch)
		require.Equal(t, types.CitationProfileACLStateUnknown, newScope.ACLCheckState)
		require.NotNil(t, newScope.NextACLCheckAt)
		var archivedOld types.CitationProfileScope
		require.NoError(t, db.Unscoped().First(&archivedOld, "id = ?", oldScope.ID).Error)
		require.False(t, archivedOld.Enabled)
		require.NotNil(t, archivedOld.FencedAt)
		require.NotNil(t, archivedOld.DeletedAt)
		require.Equal(t, types.CitationProfileFenceReasonACLDenied, archivedOld.FenceReason)
	})

	t.Run("shared ACL mutation locks one ordered union and overlap does not deadlock", func(t *testing.T) {
		const (
			authTenantID = uint64(18151)
			sourceAID    = uint64(28151)
			sourceBID    = uint64(28152)
			rounds       = 8
		)
		now := time.Date(2026, 9, 10, 23, 0, 0, 0, time.UTC)
		agentScope := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(52), sourceBID, authTenantID, "user-f7-pg-lock", uuid.NewString(), now)
		agentScope.ACLAccessPath = types.CitationProfileACLAccessPathAgentShare
		agentScope.ACLAccessPathID = "agent-f7-pg-lock"
		kbScope := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(51), sourceAID, authTenantID, agentScope.SubjectID, uuid.NewString(), now)
		require.NoError(t, db.Create(&[]types.CitationProfileScope{*agentScope, *kbScope}).Error)
		t.Cleanup(func() { citationProfilePGF7Cleanup(t, db, sourceAID, sourceBID) })

		capture := &citationProfilePGSQLCapture{}
		observedDB := db.Session(&gorm.Session{Logger: gormlogger.New(capture, gormlogger.Config{LogLevel: gormlogger.Info})})
		require.NoError(t, observedDB.Transaction(func(tx *gorm.DB) error {
			return invalidateCitationProfileSharedACLForTenantTx(tx, authTenantID, now)
		}))
		var scopeLocks []string
		for _, line := range capture.lines {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "citation_profile_scopes") && strings.Contains(lower, "for update") {
				scopeLocks = append(scopeLocks, line)
			}
		}
		require.Len(t, scopeLocks, 1, "one logical mutation must lock its complete scope union in one SELECT")
		normalizedSQL := strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(scopeLocks[0]), `"`, "")), " ")
		require.Contains(t, normalizedSQL, "order by tenant_id asc,id asc")

		for round := 0; round < rounds; round++ {
			start := make(chan struct{})
			results := make(chan error, 2)
			runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			operations := []func(*gorm.DB) error{
				func(tx *gorm.DB) error {
					return invalidateCitationProfileSharedACLForTenantTx(tx, authTenantID, now.Add(time.Duration(round+1)*time.Second))
				},
				func(tx *gorm.DB) error {
					return invalidateCitationProfileACLForTenantDeletionTx(tx, authTenantID, now.Add(time.Duration(round+1)*time.Second))
				},
			}
			for _, operation := range operations {
				operation := operation
				go func() {
					<-start
					results <- db.WithContext(runCtx).Transaction(operation)
				}()
			}
			close(start)
			for i := 0; i < len(operations); i++ {
				select {
				case concurrentErr := <-results:
					require.NoError(t, concurrentErr)
				case <-runCtx.Done():
					t.Fatalf("round %d did not complete: possible ACL scope lock-order deadlock: %v", round, runCtx.Err())
				}
			}
			cancel()
		}

		var storedScopes []types.CitationProfileScope
		require.NoError(t, db.Where("id IN ?", []string{agentScope.ID, kbScope.ID}).Order("tenant_id ASC").Order("id ASC").Find(&storedScopes).Error)
		require.Len(t, storedScopes, 2)
		for i := range storedScopes {
			require.Equal(t, uint64(9+1+2*rounds), storedScopes[i].ACLGeneration)
			require.Equal(t, types.CitationProfileACLStateUnknown, storedScopes[i].ACLCheckState)
		}
	})

	t.Run("outbox claim does not lock scope or deadlock scope event outbox order", func(t *testing.T) {
		const sourceTenantID = uint64(28181)
		now := time.Now().UTC().Truncate(time.Microsecond)
		checkedAt := now.Add(-time.Minute)
		validUntil := now.Add(time.Hour)
		scope := citationProfilePGF7WebScope(citationProfilePGF7EarlyID(81), sourceTenantID, sourceTenantID, "user-f7-pg-outbox-lock", uuid.NewString(), now)
		scope.ACLAccessPath = types.CitationProfileACLAccessPathOwner
		scope.ACLAccessPathID = ""
		scope.ACLCheckedAt = &checkedAt
		scope.NextACLCheckAt = &validUntil
		event := citationProfilePGF7Event(scope, uuid.NewString(), now)
		outbox := citationProfilePGF7Outbox(scope, uuid.NewString(), event.ID, now, 0)
		require.NoError(t, db.Create(scope).Error)
		require.NoError(t, db.Create(&event).Error)
		require.NoError(t, db.Create(&outbox).Error)
		t.Cleanup(func() { citationProfilePGF7Cleanup(t, db, sourceTenantID) })

		scopeLocked := make(chan struct{})
		claimFinished := make(chan struct{})
		orderedTxDone := make(chan error, 1)
		orderedCtx, cancelOrdered := context.WithTimeout(ctx, 10*time.Second)
		defer cancelOrdered()
		go func() {
			orderedTxDone <- db.WithContext(orderedCtx).Transaction(func(tx *gorm.DB) error {
				var lockedScopeID string
				if err := tx.Raw(`SELECT id FROM citation_profile_scopes WHERE id = ? FOR UPDATE`, scope.ID).Scan(&lockedScopeID).Error; err != nil {
					return err
				}
				if lockedScopeID != scope.ID {
					return fmt.Errorf("scope lock returned %q", lockedScopeID)
				}
				close(scopeLocked)
				select {
				case <-claimFinished:
				case <-orderedCtx.Done():
					return orderedCtx.Err()
				}
				var lockedEventID string
				if err := tx.Raw(`SELECT id FROM citation_profile_events WHERE id = ? FOR UPDATE`, event.ID).Scan(&lockedEventID).Error; err != nil {
					return err
				}
				var lockedOutboxID string
				if err := tx.Raw(`SELECT id FROM citation_profile_event_outbox WHERE id = ? FOR UPDATE`, outbox.ID).Scan(&lockedOutboxID).Error; err != nil {
					return err
				}
				if lockedEventID != event.ID || lockedOutboxID != outbox.ID {
					return fmt.Errorf("ordered locks returned event=%q outbox=%q", lockedEventID, lockedOutboxID)
				}
				return nil
			})
		}()

		select {
		case <-scopeLocked:
		case <-time.After(5 * time.Second):
			t.Fatal("ordered transaction did not acquire the scope lock")
		}
		claimCtx, cancelClaim := context.WithTimeout(ctx, 5*time.Second)
		claims, claimErr := (&citationProfileRepository{db: db}).ClaimCitationProfileEventOutbox(
			claimCtx, "f7-pg-outbox-only-lock", 1, now.Add(24*time.Hour), time.Minute,
		)
		cancelClaim()
		close(claimFinished)
		require.NoError(t, claimErr,
			"claim must use EXISTS for ACL and FOR UPDATE OF outbox only, even while the scope row is locked")
		require.Len(t, claims, 1)
		require.Equal(t, outbox.ID, claims[0].ID)

		select {
		case orderedErr := <-orderedTxDone:
			require.NoError(t, orderedErr)
		case <-time.After(10 * time.Second):
			t.Fatal("scope -> event -> outbox transaction did not finish: possible lock-order deadlock")
		}
	})
}

func citationProfilePGF7EnsureACLSchema(db *gorm.DB) error {
	var ready bool
	if err := db.Raw(`SELECT
		EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'citation_profile_scopes' AND column_name = 'acl_generation')
		AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'citation_profile_event_outbox' AND column_name = 'retry_budget_paused_at')
		AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'citation_profile_event_outbox' AND column_name = 'lease_until')
		AND EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'citation_profile_acl_runtime_state')`).Scan(&ready).Error; err != nil {
		return err
	}
	if ready {
		return nil
	}
	migrationSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000094_citation_profile_acl_sync.up.sql"))
	if err != nil {
		return err
	}
	return db.Exec(string(migrationSQL)).Error
}

func citationProfilePGF7EarlyID(sequence int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", sequence)
}

func citationProfilePGF7WebScope(id string, sourceTenantID, authenticatedTenantID uint64, subjectID, kbID string, now time.Time) *types.CitationProfileScope {
	dueAt := now.Add(-time.Minute)
	checkedAt := now.Add(-time.Hour)
	return &types.CitationProfileScope{
		ID:                       id,
		TenantID:                 sourceTenantID,
		SubjectID:                subjectID,
		KnowledgeBaseID:          kbID,
		SubjectEpoch:             uuid.NewString(),
		ProfileReadVersion:       17,
		ProfilePolicyVersion:     types.CitationProfilePolicyVersion,
		RetentionPolicyVersion:   types.CitationProfileRetentionPolicyVersion,
		Enabled:                  true,
		ACLCheckState:            types.CitationProfileACLStateCurrent,
		ACLCheckedAt:             &checkedAt,
		NextACLCheckAt:           &dueAt,
		ACLPrincipalType:         types.PrincipalWebUser,
		ACLPrincipalID:           subjectID,
		ACLAuthenticatedTenantID: authenticatedTenantID,
		ACLAccessPath:            types.CitationProfileACLAccessPathKBShare,
		ACLAccessPathID:          kbID,
		ACLGeneration:            9,
		CreatedAt:                now.Add(-time.Hour),
		UpdatedAt:                now.Add(-time.Hour),
	}
}

func citationProfilePGF7APIScope(id string, sourceTenantID, authenticatedTenantID, apiKeyID uint64, kbID string, now time.Time) *types.CitationProfileScope {
	subjectID := fmt.Sprintf("%s%d:%d", types.SessionOwnerAPITenantKeyPrefix, authenticatedTenantID, apiKeyID)
	scope := citationProfilePGF7WebScope(id, sourceTenantID, authenticatedTenantID, subjectID, kbID, now)
	scope.ACLPrincipalType = types.PrincipalAPITenant
	scope.ACLPrincipalID = fmt.Sprint(authenticatedTenantID)
	scope.ACLAPIKeyID = apiKeyID
	return scope
}

func citationProfilePGF7APIContext(tenantID, apiKeyID uint64) context.Context {
	principal := types.Principal{Type: types.PrincipalAPITenant, ID: fmt.Sprint(tenantID)}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenantID)
	ctx = types.WithPrincipal(ctx, principal)
	ctx = types.WithAuthenticatedTenantID(ctx, tenantID)
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{
		KeyID:      apiKeyID,
		ScopeType:  types.APIKeyScopeTenant,
		FullAccess: true,
	})
	return types.WithCitationProfileACLBinding(ctx, types.CitationProfileACLBinding{
		PrincipalType:         principal.Type,
		PrincipalID:           principal.ID,
		AuthenticatedTenantID: tenantID,
		APIKeyID:              apiKeyID,
		AccessPath:            types.CitationProfileACLAccessPathOwner,
	})
}

func citationProfilePGF7Outbox(scope *types.CitationProfileScope, id, eventID string, createdAt time.Time, attempts int) types.CitationProfileEventOutbox {
	return types.CitationProfileEventOutbox{
		ID:              id,
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		SubjectEpoch:    scope.SubjectEpoch,
		ScopeID:         scope.ID,
		EventID:         eventID,
		Status:          types.CitationProfileOutboxStatusPending,
		AttemptCount:    attempts,
		NextAttemptAt:   createdAt,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
}

func citationProfilePGF7Event(scope *types.CitationProfileScope, id string, now time.Time) types.CitationProfileEvent {
	return types.CitationProfileEvent{
		ID:                   id,
		TenantID:             scope.TenantID,
		SubjectID:            scope.SubjectID,
		KnowledgeBaseID:      scope.KnowledgeBaseID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		MessageID:            uuid.NewString(),
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    uuid.NewString(),
		ProducerEventKey:     uuid.NewString(),
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func citationProfilePGF7Export(scope *types.CitationProfileScope, id string, now time.Time) types.CitationProfileOperation {
	return types.CitationProfileOperation{
		ID:              id,
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		SubjectEpoch:    scope.SubjectEpoch,
		ScopeID:         scope.ID,
		OperationType:   types.CitationOperationExport,
		IdempotencyKey:  uuid.NewString(),
		Status:          types.CitationProfileOperationStatusReady,
		ArtifactURI:     "db:result_summary",
		NextAttemptAt:   now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func citationProfilePGF7Cleanup(t *testing.T, db *gorm.DB, tenantIDs ...uint64) {
	t.Helper()
	for _, model := range []interface{}{
		&types.EvidenceNodeLink{},
		&types.EvidenceResolutionRun{},
		&types.CitationProfileCorrection{},
		&types.CitationProfileOperation{},
		&types.CitationProfileEventOutbox{},
		&types.CitationProfileEvent{},
		&types.CitationProfileScope{},
	} {
		require.NoError(t, db.Unscoped().Where("tenant_id IN ?", tenantIDs).Delete(model).Error)
	}
}
