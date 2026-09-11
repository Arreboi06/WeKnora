//go:build t4pg

package repository

import (
	"context"
	"fmt"
	"os"
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

type citationProfilePGSourceEventSelectBlocker struct {
	selected chan struct{}
	release  <-chan struct{}
	once     sync.Once
}

type citationProfilePGSQLSignal struct {
	match   string
	started chan struct{}
	once    sync.Once
}

type citationProfilePGScopeLockBlocker struct {
	locked  chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *citationProfilePGSQLSignal) Printf(format string, args ...interface{}) {
	line := strings.ToLower(fmt.Sprintf(format, args...))
	if strings.Contains(line, strings.ToLower(s.match)) {
		s.once.Do(func() { close(s.started) })
	}
}

func (b *citationProfilePGSourceEventSelectBlocker) Printf(format string, args ...interface{}) {
	line := strings.ToLower(fmt.Sprintf(format, args...))
	if !strings.Contains(line, "citation_profile_events") ||
		!strings.Contains(line, "source_knowledge_id in") ||
		!strings.Contains(line, "retracted_at is null") {
		return
	}
	b.once.Do(func() {
		close(b.selected)
		<-b.release
	})
}

func (b *citationProfilePGScopeLockBlocker) Printf(format string, args ...interface{}) {
	line := strings.ToLower(fmt.Sprintf(format, args...))
	if !strings.Contains(line, "citation_profile_scopes") ||
		!strings.Contains(line, "order by id asc") ||
		!strings.Contains(line, "for update") {
		return
	}
	b.once.Do(func() {
		close(b.locked)
		<-b.release
	})
}

func TestCitationProfilePostgresWikiSourceUniverseRaceCannotPublishStaleRunPhantomEvent(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(8)
	require.NoError(t, ensureCitationProfilePGSchema(db))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC()
	tenantID := uint64(now.UnixNano())
	subjectID := "subject-f1-phantom-" + uuid.NewString()
	kbID := uuid.NewString()
	sourceID := uuid.NewString()
	pageID := uuid.NewString()
	scope := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              subjectID,
		KnowledgeBaseID:        kbID,
		SubjectEpoch:           uuid.NewString(),
		ProfileReadVersion:     1,
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          types.CitationProfileACLStateCurrent,
		PendingEventCount:      1,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releasePage := func() { releaseOnce.Do(func() { close(release) }) }
	defer releasePage()
	blocker := &citationProfilePGSourceEventSelectBlocker{selected: make(chan struct{}), release: release}
	pageDB := db.Session(&gorm.Session{Logger: gormlogger.New(blocker, gormlogger.Config{LogLevel: gormlogger.Info})})
	page := &types.WikiPage{
		ID:              pageID,
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		Slug:            "doc/f1-phantom-" + pageID,
		Title:           "F1 phantom",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		Aliases:         types.StringArray{},
		CategoryPath:    types.StringArray{},
		SourceRefs:      types.StringArray{sourceID + "|Phantom"},
		ChunkRefs:       types.StringArray{},
		InLinks:         types.StringArray{},
		OutLinks:        types.StringArray{},
		PageMetadata:    types.JSON(`{}`),
		UpdatedAt:       now,
	}
	pageDone := make(chan error, 1)
	go func() { pageDone <- newCitationProfileWikiPageRepository(pageDB).Create(ctx, page) }()
	select {
	case <-blocker.selected:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "page transaction did not reach the source-event candidate read")
	}

	event := citationProfilePGEvent(tenantID, subjectID, kbID, scope, sourceID, uuid.NewString(), 0, now.Add(time.Second))
	require.NoError(t, db.Create(&event).Error)
	resolverSignal := &citationProfilePGSQLSignal{match: event.ID, started: make(chan struct{})}
	resolverDB := db.Session(&gorm.Session{Logger: gormlogger.New(resolverSignal, gormlogger.Config{LogLevel: gormlogger.Info})})
	repo := NewCitationProfileRepository(resolverDB)
	type resolveResult struct {
		run *types.EvidenceResolutionRun
		err error
	}
	resolveDone := make(chan resolveResult, 1)
	go func() {
		run, resolveErr := repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, event.ID)
		resolveDone <- resolveResult{run: run, err: resolveErr}
	}()
	select {
	case <-resolverSignal.started:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "resolver did not reach the event-seed read")
	}

	var resolved resolveResult
	resolvedEarly := false
	select {
	case resolved = <-resolveDone:
		resolvedEarly = true
	case <-time.After(300 * time.Millisecond):
	}
	releasePage()
	require.NoError(t, <-pageDone)
	if !resolvedEarly {
		select {
		case resolved = <-resolveDone:
		case <-ctx.Done():
			require.NoError(t, ctx.Err(), "resolver did not finish after the page transaction committed")
		}
	}
	if resolvedEarly {
		t.Errorf("resolver published before the in-flight source-universe transaction committed")
	}
	require.NoError(t, resolved.err)
	require.NotNil(t, resolved.run)
	require.Equal(t, types.CitationProfileEventStatusResolved, resolved.run.Status)
	require.Equal(t, 1, resolved.run.OutputCount)
	var links []types.EvidenceNodeLink
	require.NoError(t, db.Where("resolution_run_id = ? AND relation_state = ?", resolved.run.ID, types.EvidenceRelationCurrent).Find(&links).Error)
	require.Len(t, links, 1)
	require.Equal(t, pageID, links[0].PageUUID)
}

func TestCitationProfilePostgresWikiDriftDuringACLUnknownSerializesBeforeAllow(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile probe")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(8)
	require.NoError(t, ensureCitationProfilePGSchema(db))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	tenantID := uint64(now.UnixNano())
	scope := &types.CitationProfileScope{
		ID:                     uuid.NewString(),
		TenantID:               tenantID,
		SubjectID:              "subject-f1-acl-unknown-" + uuid.NewString(),
		KnowledgeBaseID:        uuid.NewString(),
		SubjectEpoch:           uuid.NewString(),
		ProfileReadVersion:     1,
		ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
		RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
		Enabled:                true,
		ACLCheckState:          types.CitationProfileACLStateCurrent,
		PendingEventCount:      1,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	citationProfilePGBindCurrentOwnerScope(scope, now)
	require.NoError(t, db.Create(scope).Error)
	repo := &citationProfileRepository{db: db}
	sourceID := uuid.NewString()
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
	resolvedEmptyRun, err := repo.ResolveEvidenceEvent(ctx, tenantID, scope.SubjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolvedEmpty, resolvedEmptyRun.Status)
	var outbox types.CitationProfileEventOutbox
	require.NoError(t, db.Where("event_id = ?", event.ID).First(&outbox).Error)
	require.Equal(t, types.CitationProfileOutboxStatusDelivered, outbox.Status)
	require.NoError(t, repo.InvalidateCitationProfileACL(ctx, types.CitationProfileACLMutation{
		SourceTenantID:  tenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
	}))
	claimAt := time.Now().UTC().Add(time.Minute)
	var unknownScope types.CitationProfileScope
	require.NoError(t, db.First(&unknownScope, "id = ?", scope.ID).Error)
	leaseToken := "acl-f1-pg-unknown:" + uuid.NewString()
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("id = ? AND acl_generation = ?", scope.ID, unknownScope.ACLGeneration).
		Updates(map[string]interface{}{
			"acl_check_lease_token": leaseToken,
			"acl_check_lease_until": claimAt.Add(time.Minute),
		}).Error)
	aclClaim := types.CitationProfileACLClaim{
		ScopeID:    scope.ID,
		LeaseToken: leaseToken,
		Generation: unknownScope.ACLGeneration,
		Scope:      unknownScope,
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	releasePage := func() { releaseOnce.Do(func() { close(release) }) }
	defer releasePage()
	blocker := &citationProfilePGScopeLockBlocker{locked: make(chan struct{}), release: release}
	pageDB := db.Session(&gorm.Session{Logger: gormlogger.New(blocker, gormlogger.Config{LogLevel: gormlogger.Info})})
	page := &types.WikiPage{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		Slug:            "doc/f1-acl-unknown-" + uuid.NewString(),
		Title:           "F1 ACL unknown drift",
		PageType:        types.WikiPageTypeConcept,
		Status:          types.WikiPageStatusPublished,
		Version:         1,
		Aliases:         types.StringArray{},
		CategoryPath:    types.StringArray{},
		SourceRefs:      types.StringArray{sourceID + "|ACL unknown"},
		ChunkRefs:       types.StringArray{},
		InLinks:         types.StringArray{},
		OutLinks:        types.StringArray{},
		PageMetadata:    types.JSON(`{}`),
		UpdatedAt:       now.Add(2 * time.Minute),
	}
	pageDone := make(chan error, 1)
	go func() { pageDone <- newCitationProfileWikiPageRepository(pageDB).Create(ctx, page) }()
	select {
	case <-blocker.locked:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "Wiki transaction did not lock the UNKNOWN scope")
	}

	allowAt := now.Add(48 * time.Hour)
	allowDone := make(chan error, 1)
	go func() {
		allowDone <- repo.ApplyCitationProfileACLResult(
			ctx,
			&aclClaim,
			types.CitationProfileACLDecisionAllow,
			allowAt,
			allowAt.Add(5*time.Minute),
		)
	}()
	select {
	case allowErr := <-allowDone:
		require.Failf(t, "ALLOW bypassed Wiki scope lock", "unexpected early result: %v", allowErr)
	case <-time.After(300 * time.Millisecond):
	}
	releasePage()
	require.NoError(t, <-pageDone)
	select {
	case allowErr := <-allowDone:
		require.NoError(t, allowErr)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "ALLOW did not resume after Wiki commit")
	}

	var storedEvent types.CitationProfileEvent
	require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
	require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedEvent.Status)
	require.Empty(t, storedEvent.ActiveRunID)
	var storedOutbox types.CitationProfileEventOutbox
	require.NoError(t, db.First(&storedOutbox, "id = ?", outbox.ID).Error)
	require.Equal(t, types.CitationProfileOutboxStatusPending, storedOutbox.Status)
	require.Nil(t, storedOutbox.DeliveredAt)
	require.Nil(t, storedOutbox.RetryBudgetPausedAt)
	var eventCount int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("scope_id = ?", scope.ID).
		Count(&eventCount).Error)
	require.Equal(t, int64(1), eventCount, "Wiki drift must not backfill citation events")

	claims, err := repo.ClaimCitationProfileEventOutbox(ctx, "outbox-f1-pg-unknown", 1, allowAt.Add(time.Second), time.Minute)
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, event.ID, claims[0].EventID)
	_, err = repo.StartCitationProfileEventOutboxAttempt(ctx, &claims[0], allowAt.Add(2*time.Second))
	require.NoError(t, err)
	run, err := repo.ResolveClaimedEvidenceEvent(ctx, &claims[0])
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, run.Status)
	var currentLink types.EvidenceNodeLink
	require.NoError(t, db.Where(
		"event_id = ? AND page_uuid = ? AND relation_state = ?",
		event.ID,
		page.ID,
		types.EvidenceRelationCurrent,
	).First(&currentLink).Error)
}
