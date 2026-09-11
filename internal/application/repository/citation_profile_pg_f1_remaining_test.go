//go:build t4pg

package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCitationProfilePostgresF1RemainingRegression(t *testing.T) {
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

	repo := NewCitationProfileRepository(db)
	wikiRepo := newCitationProfileWikiPageRepository(db)
	ctx := context.Background()
	const tenantID uint64 = 77011

	t.Run("independent source runs stay current", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		scope := citationProfilePGF1Scope(tenantID, 2, now)
		require.NoError(t, db.Create(scope).Error)
		sourceA, sourceB := uuid.NewString(), uuid.NewString()
		pageA := citationProfilePGF1Page(scope, sourceA, now)
		pageB := citationProfilePGF1Page(scope, sourceB, now.Add(time.Second))
		require.NoError(t, wikiRepo.Create(ctx, pageA))
		require.NoError(t, wikiRepo.Create(ctx, pageB))
		events := []types.CitationProfileEvent{
			citationProfilePGEvent(tenantID, scope.SubjectID, scope.KnowledgeBaseID, scope, sourceA, uuid.NewString(), 0, now),
			citationProfilePGEvent(tenantID, scope.SubjectID, scope.KnowledgeBaseID, scope, sourceB, uuid.NewString(), 1, now.Add(time.Second)),
		}
		require.NoError(t, db.Create(&events).Error)

		runA, err := repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, events[0].ID)
		require.NoError(t, err)
		runB, err := repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, events[1].ID)
		require.NoError(t, err)
		require.NotEqual(t, runA.RunUniverseWatermark, runB.RunUniverseWatermark)

		var refreshedScope types.CitationProfileScope
		require.NoError(t, db.First(&refreshedScope, "id = ?", scope.ID).Error)
		var refs []types.WikiSourceRefIndex
		require.NoError(t, db.Where(
			"tenant_id = ? AND knowledge_base_id = ? AND lifecycle_state = ?",
			tenantID, scope.KnowledgeBaseID, "current",
		).Find(&refs).Error)
		require.Len(t, refs, 2)
		currentRefs := make(map[citationProfileSourceRefKey]struct{}, len(refs))
		for _, ref := range refs {
			currentRefs[citationProfileSourceRefKeyFromIndex(ref)] = struct{}{}
		}
		var links []types.EvidenceNodeLink
		require.NoError(t, db.Where("scope_id = ? AND relation_state = ?", scope.ID, types.EvidenceRelationCurrent).
			Order("page_uuid ASC").Find(&links).Error)
		require.Len(t, links, 2)
		var resolvedEvents []types.CitationProfileEvent
		require.NoError(t, db.Where("id IN ?", []string{events[0].ID, events[1].ID}).Find(&resolvedEvents).Error)
		liveEvents := make(map[string]types.CitationProfileEvent, len(resolvedEvents))
		for _, event := range resolvedEvents {
			liveEvents[event.ID] = event
		}
		counts := citationProfileNodeCountsFromLinks(&refreshedScope, links, currentRefs, liveEvents)
		for _, link := range links {
			require.False(t, citationProfileLinkStale(&refreshedScope, link, currentRefs))
			require.False(t, citationProfileNodeStale(&refreshedScope, counts[link.PageUUID]))
		}
	})

	t.Run("reruns preserve correction overlay and clear dirtiness", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		scope := citationProfilePGF1Scope(tenantID, 1, now)
		require.NoError(t, db.Create(scope).Error)
		sourceID := uuid.NewString()
		pageA := citationProfilePGF1Page(scope, sourceID, now)
		require.NoError(t, wikiRepo.Create(ctx, pageA))
		event := citationProfilePGEvent(tenantID, scope.SubjectID, scope.KnowledgeBaseID, scope, sourceID, uuid.NewString(), 0, now)
		require.NoError(t, db.Create(&event).Error)
		firstRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, event.ID)
		require.NoError(t, err)

		pageB := citationProfilePGF1Page(scope, sourceID, now.Add(time.Minute))
		require.NoError(t, wikiRepo.Create(ctx, pageB))
		var dirty types.CitationProfileScope
		require.NoError(t, db.First(&dirty, "id = ?", scope.ID).Error)
		require.Greater(t, dirty.DirtyMappingCount, 0)
		require.Equal(t, 1, dirty.PendingEventCount)
		secondRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, event.ID)
		require.NoError(t, err)
		require.NotEqual(t, firstRun.ID, secondRun.ID)
		require.NoError(t, db.First(&dirty, "id = ?", scope.ID).Error)
		require.Zero(t, dirty.DirtyMappingCount)
		require.Zero(t, dirty.PendingEventCount)

		var historical, active types.EvidenceNodeLink
		require.NoError(t, db.Where("event_id = ? AND resolution_run_id = ? AND page_uuid = ?", event.ID, firstRun.ID, pageA.ID).First(&historical).Error)
		require.NoError(t, db.Where("event_id = ? AND resolution_run_id = ? AND page_uuid = ?", event.ID, secondRun.ID, pageA.ID).First(&active).Error)
		const historicalID = "00000000-0000-0000-0000-00000000f101"
		const activeID = "ffffffff-ffff-ffff-ffff-fffffffff101"
		require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("id = ?", historical.ID).UpdateColumn("id", historicalID).Error)
		require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("id = ?", active.ID).UpdateColumn("id", activeID).Error)

		var currentScope types.CitationProfileScope
		require.NoError(t, db.First(&currentScope, "id = ?", scope.ID).Error)
		_, err = repo.ApplyCorrection(ctx, tenantID, scope.SubjectID, scope.KnowledgeBaseID,
			currentScope.ProfileReadVersion, uuid.NewString(), types.CitationCorrectionRejectMapping,
			event.ID, pageA.ID, "incorrect_mapping")
		require.NoError(t, err)
		historical = types.EvidenceNodeLink{}
		active = types.EvidenceNodeLink{}
		require.NoError(t, db.First(&historical, "id = ?", historicalID).Error)
		require.NoError(t, db.First(&active, "id = ?", activeID).Error)
		require.Equal(t, types.EvidenceRelationHistorical, historical.RelationState)
		require.Equal(t, types.EvidenceRelationDisputed, active.RelationState)

		pageC := citationProfilePGF1Page(scope, sourceID, now.Add(2*time.Minute))
		require.NoError(t, wikiRepo.Create(ctx, pageC))
		thirdRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, event.ID)
		require.NoError(t, err)
		var rerunLink types.EvidenceNodeLink
		require.NoError(t, db.Where("event_id = ? AND resolution_run_id = ? AND page_uuid = ?", event.ID, thirdRun.ID, pageA.ID).First(&rerunLink).Error)
		require.Equal(t, types.EvidenceRelationDisputed, rerunLink.RelationState)
		var rejectedCurrent int64
		require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
			Where("event_id = ? AND page_uuid = ? AND relation_state = ?", event.ID, pageA.ID, types.EvidenceRelationCurrent).
			Count(&rejectedCurrent).Error)
		require.Zero(t, rejectedCurrent)
		require.NoError(t, db.First(&currentScope, "id = ?", scope.ID).Error)
		require.Zero(t, currentScope.DirtyMappingCount)
		require.Zero(t, currentScope.PendingEventCount)
	})

	t.Run("new page without matching event advances every readable scope without backfill", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		kbID := uuid.NewString()
		scopeA := citationProfilePGF1Scope(tenantID, 0, now)
		scopeA.KnowledgeBaseID = kbID
		scopeB := citationProfilePGF1Scope(tenantID, 0, now)
		scopeB.KnowledgeBaseID = kbID
		aclUnknown := citationProfilePGF1Scope(tenantID, 0, now)
		aclUnknown.KnowledgeBaseID = kbID
		aclUnknown.ACLCheckState = types.CitationProfileACLStateUnknown
		disabled := citationProfilePGF1Scope(tenantID, 0, now)
		disabled.KnowledgeBaseID = kbID
		disabled.Enabled = false
		records := []types.CitationProfileScope{*scopeB, *aclUnknown, *disabled, *scopeA}
		require.NoError(t, db.Create(&records).Error)
		// GORM omits a false zero value when the model declares default:true,
		// so persist the disabled control explicitly.
		require.NoError(t, db.Model(&types.CitationProfileScope{}).
			Where("id = ?", disabled.ID).
			UpdateColumn("enabled", false).Error)

		for i := 0; i < 2; i++ {
			page := citationProfilePGF1Page(scopeA, uuid.NewString(), now.Add(time.Duration(i)*time.Second))
			require.NoError(t, wikiRepo.Create(ctx, page))
		}
		readCursor := func(scope *types.CitationProfileScope) (types.CitationProfileCursor, types.CitationProfileScope) {
			t.Helper()
			page, err := repo.ListNodes(ctx, tenantID, scope.SubjectID, kbID, nil, 1)
			require.NoError(t, err)
			require.NotNil(t, page.NextCursor)
			raw, err := base64.RawURLEncoding.DecodeString(*page.NextCursor)
			require.NoError(t, err)
			var cursor types.CitationProfileCursor
			require.NoError(t, json.Unmarshal(raw, &cursor))
			var before types.CitationProfileScope
			require.NoError(t, db.First(&before, "id = ?", scope.ID).Error)
			return cursor, before
		}
		cursorA, beforeA := readCursor(scopeA)
		cursorB, beforeB := readCursor(scopeB)
		var beforeACLUnknown types.CitationProfileScope
		require.NoError(t, db.First(&beforeACLUnknown, "id = ?", aclUnknown.ID).Error)
		var beforeDisabled types.CitationProfileScope
		require.NoError(t, db.First(&beforeDisabled, "id = ?", disabled.ID).Error)

		page := citationProfilePGF1Page(scopeA, uuid.NewString(), now.Add(time.Minute))
		require.NoError(t, wikiRepo.Create(ctx, page))

		var eventCount int64
		require.NoError(t, db.Model(&types.CitationProfileEvent{}).
			Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
			Count(&eventCount).Error)
		require.Zero(t, eventCount, "a page-universe change must not backfill citation events")

		for _, item := range []struct {
			scope  *types.CitationProfileScope
			before types.CitationProfileScope
			cursor types.CitationProfileCursor
		}{
			{scope: scopeA, before: beforeA, cursor: cursorA},
			{scope: scopeB, before: beforeB, cursor: cursorB},
		} {
			var after types.CitationProfileScope
			require.NoError(t, db.First(&after, "id = ?", item.scope.ID).Error)
			require.Greater(t, after.ProfileReadVersion, item.before.ProfileReadVersion)
			require.Greater(t, after.MappingRevision, item.before.MappingRevision)
			require.Zero(t, after.PendingEventCount)
			require.Zero(t, after.DirtyMappingCount)
			_, err := repo.ListNodes(ctx, tenantID, item.scope.SubjectID, kbID, &item.cursor, 1)
			require.ErrorIs(t, err, types.ErrCitationProfileChanged)
		}
		var afterACLUnknown types.CitationProfileScope
		require.NoError(t, db.First(&afterACLUnknown, "id = ?", aclUnknown.ID).Error)
		require.Equal(t, types.CitationProfileACLStateUnknown, afterACLUnknown.ACLCheckState)
		require.Greater(t, afterACLUnknown.ProfileReadVersion, beforeACLUnknown.ProfileReadVersion)
		require.Greater(t, afterACLUnknown.MappingRevision, beforeACLUnknown.MappingRevision)
		require.NotEqual(t, beforeACLUnknown.SourceUniverseWatermark, afterACLUnknown.SourceUniverseWatermark)
		require.Zero(t, afterACLUnknown.PendingEventCount)
		require.Zero(t, afterACLUnknown.DirtyMappingCount)

		var afterDisabled types.CitationProfileScope
		require.NoError(t, db.First(&afterDisabled, "id = ?", disabled.ID).Error)
		require.Equal(t, beforeDisabled.ProfileReadVersion, afterDisabled.ProfileReadVersion)
		require.Equal(t, beforeDisabled.MappingRevision, afterDisabled.MappingRevision)
		require.Equal(t, beforeDisabled.SourceUniverseWatermark, afterDisabled.SourceUniverseWatermark)
	})

	t.Run("automatic links advance graph snapshot without evidence backfill", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		scope := citationProfilePGF1Scope(tenantID, 0, now)
		scope.KnowledgeBaseID = uuid.NewString()
		require.NoError(t, db.Create(scope).Error)
		pageA := citationProfilePGF1Page(scope, uuid.NewString(), now)
		pageB := citationProfilePGF1Page(scope, uuid.NewString(), now.Add(time.Second))
		require.NoError(t, wikiRepo.Create(ctx, pageA))
		require.NoError(t, wikiRepo.Create(ctx, pageB))

		beforeGraph, err := repo.GetGraph(ctx, tenantID, scope.SubjectID, scope.KnowledgeBaseID)
		require.NoError(t, err)
		require.Empty(t, beforeGraph.Edges)
		var beforeScope types.CitationProfileScope
		require.NoError(t, db.First(&beforeScope, "id = ?", scope.ID).Error)

		pageVersion := pageA.Version
		pageA.Content = "linked [[" + pageB.Slug + "]]"
		pageA.OutLinks = types.StringArray{pageB.Slug}
		require.NoError(t, wikiRepo.UpdateAutoLinkedContent(ctx, pageA))

		var afterScope types.CitationProfileScope
		require.NoError(t, db.First(&afterScope, "id = ?", scope.ID).Error)
		require.Greater(t, afterScope.ProfileReadVersion, beforeScope.ProfileReadVersion)
		require.Greater(t, afterScope.MappingRevision, beforeScope.MappingRevision)
		require.Zero(t, afterScope.PendingEventCount)
		require.Zero(t, afterScope.DirtyMappingCount)
		var eventCount int64
		require.NoError(t, db.Model(&types.CitationProfileEvent{}).
			Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, scope.KnowledgeBaseID).
			Count(&eventCount).Error)
		require.Zero(t, eventCount)

		var storedPage types.WikiPage
		require.NoError(t, db.First(&storedPage, "id = ?", pageA.ID).Error)
		require.Equal(t, pageVersion, storedPage.Version)
		afterGraph, err := repo.GetGraph(ctx, tenantID, scope.SubjectID, scope.KnowledgeBaseID)
		require.NoError(t, err)
		require.NotEqual(t, beforeGraph.Snapshot.ReadVersion, afterGraph.Snapshot.ReadVersion)
		require.Equal(t, []types.CitationProfileGraphEdgeDTO{{
			SourcePageUUID: pageA.ID,
			TargetPageUUID: pageB.ID,
			EdgeType:       "wiki_link",
		}}, afterGraph.Edges)
	})

	t.Run("drift deadletter releases dirtiness and a later requeue resolves cleanly", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		scope := citationProfilePGF1Scope(tenantID, 1, now)
		require.NoError(t, db.Create(scope).Error)
		sourceID := uuid.NewString()
		pageA := citationProfilePGF1Page(scope, sourceID, now)
		require.NoError(t, wikiRepo.Create(ctx, pageA))
		event := citationProfilePGEvent(
			tenantID,
			scope.SubjectID,
			scope.KnowledgeBaseID,
			scope,
			sourceID,
			uuid.NewString(),
			0,
			now,
		)
		require.NoError(t, db.Create(&event).Error)
		_, err := repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, event.ID)
		require.NoError(t, err)

		pageB := citationProfilePGF1Page(scope, sourceID, now.Add(time.Minute))
		require.NoError(t, wikiRepo.Create(ctx, pageB))
		var dirtyAfterDrift types.CitationProfileScope
		require.NoError(t, db.First(&dirtyAfterDrift, "id = ?", scope.ID).Error)
		require.Equal(t, 1, dirtyAfterDrift.DirtyMappingCount)
		require.Equal(t, 1, dirtyAfterDrift.PendingEventCount)

		leaseAt := now.Add(2 * time.Minute)
		workerID := "f1-pg-deadletter-" + uuid.NewString()
		leaseResult := db.Model(&types.CitationProfileEventOutbox{}).
			Where(
				"tenant_id = ? AND knowledge_base_id = ? AND scope_id = ? AND event_id = ? AND status = ? AND delivered_at IS NULL AND deadletter_at IS NULL",
				tenantID,
				scope.KnowledgeBaseID,
				scope.ID,
				event.ID,
				types.CitationProfileOutboxStatusPending,
			).
			Updates(map[string]interface{}{
				"status":      types.CitationProfileOutboxStatusDelivering,
				"locked_at":   leaseAt,
				"lease_until": leaseAt.Add(time.Minute),
				"locked_by":   workerID,
				"updated_at":  leaseAt,
			})
		require.NoError(t, leaseResult.Error)
		require.Equal(t, int64(1), leaseResult.RowsAffected)
		var claim types.CitationProfileEventOutbox
		require.NoError(t, db.Where(
			"tenant_id = ? AND knowledge_base_id = ? AND scope_id = ? AND event_id = ?",
			tenantID,
			scope.KnowledgeBaseID,
			scope.ID,
			event.ID,
		).First(&claim).Error)
		outboxRepo := &citationProfileRepository{db: db}
		require.NoError(t, outboxRepo.DeadletterCitationProfileEventOutbox(
			ctx,
			&claim,
			leaseAt,
			errors.New("forced PostgreSQL drift terminal failure"),
		))

		var afterDeadletter types.CitationProfileScope
		require.NoError(t, db.First(&afterDeadletter, "id = ?", scope.ID).Error)
		require.Zero(t, afterDeadletter.DirtyMappingCount)
		require.Zero(t, afterDeadletter.PendingEventCount)

		pageC := citationProfilePGF1Page(scope, sourceID, now.Add(3*time.Minute))
		require.NoError(t, wikiRepo.Create(ctx, pageC))
		var requeuedScope types.CitationProfileScope
		require.NoError(t, db.First(&requeuedScope, "id = ?", scope.ID).Error)
		require.Equal(t, 1, requeuedScope.DirtyMappingCount)
		require.Equal(t, 1, requeuedScope.PendingEventCount)
		var requeuedOutbox types.CitationProfileEventOutbox
		require.NoError(t, db.Where(
			"tenant_id = ? AND knowledge_base_id = ? AND scope_id = ? AND event_id = ?",
			tenantID,
			scope.KnowledgeBaseID,
			scope.ID,
			event.ID,
		).First(&requeuedOutbox).Error)
		require.Equal(t, types.CitationProfileOutboxStatusPending, requeuedOutbox.Status)
		require.Zero(t, requeuedOutbox.AttemptCount)
		require.Nil(t, requeuedOutbox.DeadletterAt)
		require.Nil(t, requeuedOutbox.LockedAt)
		require.Empty(t, requeuedOutbox.LockedBy)

		_, err = repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, event.ID)
		require.NoError(t, err)
		var resolvedScope types.CitationProfileScope
		require.NoError(t, db.First(&resolvedScope, "id = ?", scope.ID).Error)
		require.Zero(t, resolvedScope.DirtyMappingCount)
		require.Zero(t, resolvedScope.PendingEventCount)
		var resolvedEvent types.CitationProfileEvent
		require.NoError(t, db.First(&resolvedEvent, "id = ?", event.ID).Error)
		require.Equal(t, types.CitationProfileEventStatusResolved, resolvedEvent.Status)
	})
}

func citationProfilePGF1Scope(tenantID uint64, pending int, now time.Time) *types.CitationProfileScope {
	checkedAt := now.Add(-time.Minute)
	nextCheckAt := now.Add(time.Hour)
	subjectID := "f1-final-pg-" + uuid.NewString()
	return &types.CitationProfileScope{
		ID:                       uuid.NewString(),
		TenantID:                 tenantID,
		SubjectID:                subjectID,
		KnowledgeBaseID:          uuid.NewString(),
		SubjectEpoch:             uuid.NewString(),
		ProfileReadVersion:       1,
		ProfilePolicyVersion:     types.CitationProfilePolicyVersion,
		RetentionPolicyVersion:   types.CitationProfileRetentionPolicyVersion,
		Enabled:                  true,
		PendingEventCount:        pending,
		ACLCheckState:            types.CitationProfileACLStateCurrent,
		ACLCheckedAt:             &checkedAt,
		NextACLCheckAt:           &nextCheckAt,
		ACLPrincipalType:         types.PrincipalWebUser,
		ACLPrincipalID:           subjectID,
		ACLAuthenticatedTenantID: tenantID,
		ACLAccessPath:            types.CitationProfileACLAccessPathOwner,
		ACLGeneration:            1,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
}

func citationProfilePGF1Page(scope *types.CitationProfileScope, sourceID string, now time.Time) *types.WikiPage {
	pageID := uuid.NewString()
	return &types.WikiPage{
		ID:              pageID,
		TenantID:        scope.TenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "f1/final-" + pageID,
		Title:           "F1 final " + pageID,
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		Aliases:         types.StringArray{},
		CategoryPath:    types.StringArray{},
		SourceRefs:      types.StringArray{sourceID + "|Source"},
		ChunkRefs:       types.StringArray{},
		InLinks:         types.StringArray{},
		OutLinks:        types.StringArray{},
		PageMetadata:    types.JSON(json.RawMessage(`{}`)),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}
