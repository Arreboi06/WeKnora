//go:build cgo

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCitationProfileACLStartupFenceQuarantinesCurrentScopesOnly(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC()

	current := citationProfileACLTestScope("scope-f7-startup-current", now)
	current.ACLCheckState = types.CitationProfileACLStateCurrent
	checkedAt := now
	nextCheckAt := now.Add(5 * time.Minute)
	leaseUntil := now.Add(time.Minute)
	current.ACLCheckedAt = &checkedAt
	current.NextACLCheckAt = &nextCheckAt
	current.ACLCheckLeaseToken = "stale-before-reenable"
	current.ACLCheckLeaseUntil = &leaseUntil
	unknown := citationProfileACLTestScope("scope-f7-startup-unknown", now)
	unknown.ACLCheckState = types.CitationProfileACLStateUnknown
	unknown.ACLCheckedAt = nil
	unknown.NextACLCheckAt = &now
	require.NoError(t, db.Create(current).Error)
	require.NoError(t, db.Create(unknown).Error)

	require.NoError(t, repo.FenceCitationProfileACLForStartup(context.Background()))

	var storedCurrent types.CitationProfileScope
	require.NoError(t, db.First(&storedCurrent, "id = ?", current.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, storedCurrent.ACLCheckState)
	require.Nil(t, storedCurrent.ACLCheckedAt)
	require.Empty(t, storedCurrent.ACLCheckLeaseToken)
	require.Nil(t, storedCurrent.ACLCheckLeaseUntil)
	require.Equal(t, current.ACLGeneration+1, storedCurrent.ACLGeneration)
	require.Equal(t, current.ProfileReadVersion+1, storedCurrent.ProfileReadVersion)
	require.NotNil(t, storedCurrent.NextACLCheckAt)
	require.False(t, storedCurrent.NextACLCheckAt.After(time.Now().UTC().Add(time.Second)))

	var storedUnknown types.CitationProfileScope
	require.NoError(t, db.First(&storedUnknown, "id = ?", unknown.ID).Error)
	require.Equal(t, unknown.ACLGeneration, storedUnknown.ACLGeneration)
	require.Equal(t, unknown.ProfileReadVersion, storedUnknown.ProfileReadVersion)
}

func TestCitationProfileACLStartupFenceCoalescesOneEnabledRollout(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.CitationProfileACLRuntimeState{}))
	require.NoError(t, db.Create(&types.CitationProfileACLRuntimeState{
		ID: 1, Enabled: false, TransitionGeneration: 0, ChangedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error)
	repo := &citationProfileRepository{db: db}
	now := time.Now().UTC()

	scope := citationProfileACLTestScope("scope-f7-startup-rollout", now)
	scope.ACLCheckState = types.CitationProfileACLStateCurrent
	checkedAt := now
	nextCheckAt := now.Add(5 * time.Minute)
	scope.ACLCheckedAt = &checkedAt
	scope.NextACLCheckAt = &nextCheckAt
	require.NoError(t, db.Create(scope).Error)
	lockedAt := now
	leaseUntil := now.Add(10 * time.Minute)
	outbox := types.CitationProfileEventOutbox{
		ID: "outbox-f7-startup-rollout", TenantID: scope.TenantID, SubjectID: scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID, SubjectEpoch: scope.SubjectEpoch, ScopeID: scope.ID,
		EventID: "event-f7-startup-rollout", Status: types.CitationProfileOutboxStatusDelivering,
		NextAttemptAt: now, LockedAt: &lockedAt, LeaseUntil: &leaseUntil, LockedBy: "old-rollout-worker",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&outbox).Error)

	// The first enabled instance in a rollout must fence any grant that could
	// have survived a feature-disabled interval.
	require.NoError(t, repo.PrepareCitationProfileACLFeatureState(context.Background(), true))
	var firstFence types.CitationProfileScope
	require.NoError(t, db.First(&firstFence, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, firstFence.ACLCheckState)
	require.Equal(t, scope.ACLGeneration+1, firstFence.ACLGeneration)
	require.Equal(t, scope.ProfileReadVersion+1, firstFence.ProfileReadVersion)
	var firstFencedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&firstFencedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, firstFencedOutbox.Status)
	require.Nil(t, firstFencedOutbox.LockedAt)
	require.Nil(t, firstFencedOutbox.LeaseUntil)
	require.Empty(t, firstFencedOutbox.LockedBy)
	require.NotNil(t, firstFencedOutbox.RetryBudgetPausedAt)
	_, err := repo.StartCitationProfileEventOutboxAttempt(context.Background(), &outbox, now)
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost,
		"the startup fence must revoke an in-flight pre-enable outbox lease")

	// Model a completed authority refresh before the next replica starts.
	refreshedAt := now.Add(time.Minute)
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("id = ?", scope.ID).
		Updates(map[string]interface{}{
			"acl_check_state":   types.CitationProfileACLStateCurrent,
			"acl_checked_at":    refreshedAt,
			"next_acl_check_at": refreshedAt.Add(5 * time.Minute),
		}).Error)

	// A serial second instance in the same enabled rollout observes the durable
	// state marker and must not trigger another global generation/read fence.
	require.NoError(t, repo.PrepareCitationProfileACLFeatureState(context.Background(), true))
	var sameRollout types.CitationProfileScope
	require.NoError(t, db.First(&sameRollout, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateCurrent, sameRollout.ACLCheckState)
	require.Equal(t, firstFence.ACLGeneration, sameRollout.ACLGeneration)
	require.Equal(t, firstFence.ProfileReadVersion, sameRollout.ProfileReadVersion)
	var sameRolloutOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&sameRolloutOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, firstFencedOutbox.Status, sameRolloutOutbox.Status)
	require.Equal(t, firstFencedOutbox.UpdatedAt, sameRolloutOutbox.UpdatedAt)
	require.Equal(t, firstFencedOutbox.RetryBudgetPausedAt, sameRolloutOutbox.RetryBudgetPausedAt)

	var runtimeState types.CitationProfileACLRuntimeState
	require.NoError(t, db.First(&runtimeState, "id = ?", 1).Error)
	require.True(t, runtimeState.Enabled)
	require.Equal(t, uint64(1), runtimeState.TransitionGeneration)
}

func TestCitationProfileACLFeatureStateFailsClosedWhenCoordinationStateIsMissing(t *testing.T) {
	t.Run("table missing", func(t *testing.T) {
		db := newCitationProfileRepositoryTestDB(t)
		repo := &citationProfileRepository{db: db}
		require.Error(t, repo.PrepareCitationProfileACLFeatureState(context.Background(), true))
		require.Error(t, repo.PrepareCitationProfileACLFeatureState(context.Background(), false))
	})

	t.Run("singleton row missing", func(t *testing.T) {
		db := newCitationProfileRepositoryTestDB(t)
		require.NoError(t, db.AutoMigrate(&types.CitationProfileACLRuntimeState{}))
		repo := &citationProfileRepository{db: db}
		require.Error(t, repo.PrepareCitationProfileACLFeatureState(context.Background(), true))
		require.Error(t, repo.PrepareCitationProfileACLFeatureState(context.Background(), false))
	})
}

func TestCitationProfileACLFeatureOffReplicaStillInvalidatesAfterClusterEnable(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	require.NoError(t, db.AutoMigrate(&types.CitationProfileACLRuntimeState{}))
	now := time.Now().UTC()
	require.NoError(t, db.Create(&types.CitationProfileACLRuntimeState{
		ID: 1, Enabled: true, TransitionGeneration: 1, ChangedAt: now, UpdatedAt: now,
	}).Error)
	scope := citationProfileACLTestScope("scope-f7-old-off-replica", now)
	require.NoError(t, db.Create(scope).Error)
	lockedAt := now
	leaseUntil := now.Add(10 * time.Minute)
	outbox := types.CitationProfileEventOutbox{
		ID: "outbox-f7-old-off-replica", TenantID: scope.TenantID, SubjectID: scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID, SubjectEpoch: scope.SubjectEpoch, ScopeID: scope.ID,
		EventID: "event-f7-old-off-replica", Status: types.CitationProfileOutboxStatusDelivering,
		NextAttemptAt: now, LockedAt: &lockedAt, LeaseUntil: &leaseUntil, LockedBy: "old-off-worker",
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(&outbox).Error)

	repo := &citationProfileRepository{db: db}
	require.NoError(t, repo.PrepareCitationProfileACLFeatureState(context.Background(), false))
	var runtimeState types.CitationProfileACLRuntimeState
	require.NoError(t, db.First(&runtimeState, "id = ?", 1).Error)
	require.True(t, runtimeState.Enabled,
		"a locally disabled replica must not clear a marker while enabled peers may still serve")
	require.Equal(t, uint64(1), runtimeState.TransitionGeneration)

	oldOffReplicaGate := newCitationProfileACLInvalidationGate(&types.CitationProfileConfig{Enabled: false})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return oldOffReplicaGate.invalidate(tx, types.CitationProfileACLMutation{
			PrincipalType: types.PrincipalWebUser, PrincipalID: scope.SubjectID,
			AuthenticatedTenantID: scope.TenantID, SourceTenantID: scope.TenantID,
			KnowledgeBaseID: scope.KnowledgeBaseID,
		}, now)
	}))

	var stored types.CitationProfileScope
	require.NoError(t, db.First(&stored, "id = ?", scope.ID).Error)
	require.Equal(t, types.CitationProfileACLStateUnknown, stored.ACLCheckState,
		"an old feature-off replica must honor the cluster enabled marker")
	require.Equal(t, scope.ACLGeneration+1, stored.ACLGeneration)
	require.Equal(t, scope.ProfileReadVersion+1, stored.ProfileReadVersion)
	var storedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, storedOutbox.Status)
	require.Nil(t, storedOutbox.LockedAt)
	require.Nil(t, storedOutbox.LeaseUntil)
	require.Empty(t, storedOutbox.LockedBy)
	require.NotNil(t, storedOutbox.RetryBudgetPausedAt)
	_, err := repo.StartCitationProfileEventOutboxAttempt(context.Background(), &outbox, now)
	require.ErrorIs(t, err, types.ErrCitationProfileOutboxLeaseLost,
		"permission mutation on a locally disabled replica must revoke the stale lease")
}
