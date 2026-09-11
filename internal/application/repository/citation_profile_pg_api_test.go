//go:build t4pg

package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	profileservice "github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestCitationProfileHTTPPostgresVertical(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("T4_PG_DSN"))
	if dsn == "" {
		t.Skip("T4_PG_DSN is required for the real PostgreSQL citation profile API probe")
	}

	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, ensureCitationProfilePGAPISchema(db))

	repo := repository.NewCitationProfileRepository(db)
	tenantID := uint64(77)
	subjectID := "subject-http"
	kbID := uuid.NewString()
	router := newCitationProfilePGAPIRouter(repo, tenantID, subjectID)

	enrollment := citationProfilePGAPIJSON[types.CitationProfileEnrollmentResponse](t, router, http.MethodPut, "/kb/"+kbID+"/enrollment", types.CitationProfileEnrollmentRequest{
		Enabled:             true,
		ExpectedReadVersion: "0",
		IdempotencyKey:      uuid.NewString(),
	}, http.StatusOK)
	require.True(t, enrollment.Enabled)
	require.True(t, enrollment.Enrolled)
	require.Nil(t, enrollment.Scope)
	require.Nil(t, enrollment.Snapshot)

	otherRouter := newCitationProfilePGAPIRouter(repo, tenantID, "subject-http-other")
	otherStatus := citationProfilePGAPIJSON[types.CitationProfileStatus](t, otherRouter, http.MethodGet, "/kb/"+kbID+"/status", nil, http.StatusOK)
	require.False(t, otherStatus.Enrolled)

	ctx := context.Background()
	scope, err := repo.GetScopeStatus(ctx, tenantID, subjectID, kbID)
	require.NoError(t, err)
	require.NotNil(t, scope)
	initialEpoch := scope.SubjectEpoch
	citationProfilePGAPIAuthorizeScope(t, db, scope)

	now := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	pageA := uuid.NewString()
	pageB := uuid.NewString()
	knowledgeID := uuid.NewString()
	require.NoError(t, insertCitationProfilePGAPIPage(db, tenantID, kbID, pageA, "api/a", "API Page A", types.StringArray{"api/b"}, now))
	require.NoError(t, insertCitationProfilePGAPIPage(db, tenantID, kbID, pageB, "api/b", "API Page B", nil, now.Add(time.Minute)))
	require.NoError(t, db.Create(citationProfilePGAPISourceRef(tenantID, kbID, knowledgeID, pageB, 1, 20, "wm-api", now)).Error)

	event := citationProfilePGAPIEvent(tenantID, subjectID, kbID, scope, knowledgeID, "message-api", 0, now)
	require.NoError(t, db.Create(&event).Error)
	run, err := repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, event.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, run.Status)

	status := citationProfilePGAPIJSON[types.CitationProfileStatus](t, router, http.MethodGet, "/kb/"+kbID+"/status", nil, http.StatusOK)
	require.True(t, status.Enrolled)
	require.NotNil(t, status.Snapshot)
	require.NotEmpty(t, status.Snapshot.ReadVersion)

	nodes := citationProfilePGAPIJSON[types.CitationProfileNodeListResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusOK)
	nodeB := citationProfilePGAPIFindNode(t, nodes.Items, pageB)
	require.Equal(t, 1, nodeB.AuthorizedEvidenceCount)
	require.Equal(t, 1, nodeB.CurrentLinkCount)
	require.Equal(t, types.EvidenceRelationCurrent, nodeB.Overlay)

	graph := citationProfilePGAPIJSON[types.CitationProfileGraphResponse](t, router, http.MethodGet, "/kb/"+kbID+"/graph", nil, http.StatusOK)
	require.Len(t, graph.Edges, 1)
	require.Equal(t, pageA, graph.Edges[0].SourcePageUUID)
	require.Equal(t, pageB, graph.Edges[0].TargetPageUUID)
	require.Equal(t, 1, graph.Edges[0].EvidenceEventCount)

	evidence := citationProfilePGAPIJSON[types.CitationProfileNodeEvidenceResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes/"+pageB+"/evidence?page_size=10", nil, http.StatusOK)
	require.Len(t, evidence.Items, 1)
	require.Equal(t, event.ID, evidence.Items[0].EventID)
	require.Equal(t, types.CitationProfileCorrectionStateNone, evidence.Items[0].CorrectionState)

	citationProfilePGAPIRaw(t, router, http.MethodPost, "/kb/"+kbID+"/corrections", types.CitationProfileCorrectionRequest{
		ExpectedReadVersion: evidence.Snapshot.ReadVersion,
		IdempotencyKey:      uuid.NewString(),
		Action:              types.CitationCorrectionConfirmRelevant,
		EventID:             event.ID,
		PageUUID:            pageA,
		ReasonCode:          "no_link_for_page",
	}, http.StatusNotFound)

	rejectRequest := types.CitationProfileCorrectionRequest{
		ExpectedReadVersion: evidence.Snapshot.ReadVersion,
		IdempotencyKey:      uuid.NewString(),
		Action:              types.CitationCorrectionRejectMapping,
		EventID:             event.ID,
		PageUUID:            pageB,
		ReasonCode:          "wrong_page",
	}
	reject := citationProfilePGAPIJSON[types.CitationProfileCorrectionResponse](t, router, http.MethodPost, "/kb/"+kbID+"/corrections", rejectRequest, http.StatusOK)
	require.Equal(t, types.CitationCorrectionRejectMapping, reject.Action)
	require.Equal(t, "applied", reject.Result)
	require.NotNil(t, reject.Snapshot)
	require.NotEqual(t, evidence.Snapshot.ReadVersion, reject.Snapshot.ReadVersion)
	replayedReject := citationProfilePGAPIJSON[types.CitationProfileCorrectionResponse](t, router, http.MethodPost, "/kb/"+kbID+"/corrections", rejectRequest, http.StatusOK)
	require.Equal(t, reject.CorrectionID, replayedReject.CorrectionID)
	require.Equal(t, reject.Snapshot.ReadVersion, replayedReject.Snapshot.ReadVersion)
	require.Equal(t, reject, replayedReject,
		"an exact idempotent replay must preserve every response field, including snapshot capture time")

	nodesAfterReject := citationProfilePGAPIJSON[types.CitationProfileNodeListResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusOK)
	nodeB = citationProfilePGAPIFindNode(t, nodesAfterReject.Items, pageB)
	require.Equal(t, 1, nodeB.AuthorizedEvidenceCount)
	require.Equal(t, 0, nodeB.CurrentLinkCount)
	require.Equal(t, 1, nodeB.DisputedLinkCount)
	require.Equal(t, types.EvidenceRelationDisputed, nodeB.Overlay)
	evidenceAfterReject := citationProfilePGAPIJSON[types.CitationProfileNodeEvidenceResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes/"+pageB+"/evidence?page_size=10", nil, http.StatusOK)
	require.Len(t, evidenceAfterReject.Items, 1)
	require.Equal(t, types.CitationProfileCorrectionStateRejected, evidenceAfterReject.Items[0].CorrectionState)
	require.Len(t, evidenceAfterReject.Items[0].CorrectionHistory, 1)

	citationProfilePGAPIRaw(t, router, http.MethodPost, "/kb/"+kbID+"/corrections", types.CitationProfileCorrectionRequest{
		ExpectedReadVersion: evidence.Snapshot.ReadVersion,
		IdempotencyKey:      uuid.NewString(),
		Action:              types.CitationCorrectionConfirmRelevant,
		EventID:             event.ID,
		PageUUID:            pageB,
		ReasonCode:          "stale_confirm",
	}, http.StatusConflict)

	confirm := citationProfilePGAPIJSON[types.CitationProfileCorrectionResponse](t, router, http.MethodPost, "/kb/"+kbID+"/corrections", types.CitationProfileCorrectionRequest{
		ExpectedReadVersion: reject.Snapshot.ReadVersion,
		IdempotencyKey:      uuid.NewString(),
		Action:              types.CitationCorrectionConfirmRelevant,
		EventID:             event.ID,
		PageUUID:            pageB,
		ReasonCode:          "reviewed_current",
	}, http.StatusOK)
	require.Equal(t, types.CitationCorrectionConfirmRelevant, confirm.Action)
	require.NotNil(t, confirm.Snapshot)
	evidenceAfterConfirm := citationProfilePGAPIJSON[types.CitationProfileNodeEvidenceResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes/"+pageB+"/evidence?page_size=10", nil, http.StatusOK)
	require.Len(t, evidenceAfterConfirm.Items, 1)
	require.Equal(t, types.EvidenceRelationCurrent, evidenceAfterConfirm.Items[0].Overlay)
	require.Equal(t, types.CitationProfileCorrectionStateConfirmed, evidenceAfterConfirm.Items[0].CorrectionState)
	require.Len(t, evidenceAfterConfirm.Items[0].CorrectionHistory, 2)

	status = citationProfilePGAPIJSON[types.CitationProfileStatus](t, router, http.MethodGet, "/kb/"+kbID+"/status", nil, http.StatusOK)
	require.Equal(t, confirm.Snapshot.ReadVersion, status.Snapshot.ReadVersion)
	exportOp := citationProfilePGAPIJSON[types.CitationProfileExportOperationResponse](t, router, http.MethodPost, "/kb/"+kbID+"/exports", types.CitationProfileExportRequest{
		ExpectedReadVersion: status.Snapshot.ReadVersion,
		IdempotencyKey:      uuid.NewString(),
		Format:              types.CitationProfileExportFormatJSON,
	}, http.StatusOK)
	require.Equal(t, types.CitationProfileOperationStatusReady, exportOp.Status)
	require.NotEmpty(t, exportOp.OperationID)

	download := citationProfilePGAPIRaw(t, router, http.MethodGet, "/kb/"+kbID+"/exports/"+exportOp.OperationID+"/download", nil, http.StatusOK)
	require.NotContains(t, download.Body.String(), "subject_id")
	var payload struct {
		Events []map[string]any `json:"events"`
		Links  []map[string]any `json:"links"`
	}
	require.NoError(t, json.Unmarshal(download.Body.Bytes(), &payload))
	require.Len(t, payload.Events, 1)
	require.Len(t, payload.Links, 1)
	require.Equal(t, event.ID, payload.Events[0]["event_id"])
	citationProfilePGAPIRaw(t, otherRouter, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusNotFound)
	citationProfilePGAPIRaw(t, otherRouter, http.MethodGet, "/kb/"+kbID+"/exports/"+exportOp.OperationID+"/download", nil, http.StatusNotFound)
	otherTenantRouter := newCitationProfilePGAPIRouter(repo, tenantID+1, subjectID)
	citationProfilePGAPIRaw(t, otherTenantRouter, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusNotFound)
	citationProfilePGAPIRaw(t, otherTenantRouter, http.MethodGet, "/kb/"+kbID+"/exports/"+exportOp.OperationID+"/download", nil, http.StatusNotFound)
	citationProfilePGAPIRaw(t, router, http.MethodGet, "/kb/"+uuid.NewString()+"/nodes?page_size=10", nil, http.StatusNotFound)

	kbBID := uuid.NewString()
	enrollmentB := citationProfilePGAPIJSON[types.CitationProfileEnrollmentResponse](t, router, http.MethodPut, "/kb/"+kbBID+"/enrollment", types.CitationProfileEnrollmentRequest{
		Enabled:             true,
		ExpectedReadVersion: "0",
		IdempotencyKey:      uuid.NewString(),
	}, http.StatusOK)
	require.True(t, enrollmentB.Enrolled)
	require.Nil(t, enrollmentB.Scope)
	require.Nil(t, enrollmentB.Snapshot)
	scopeB, err := repo.GetScopeStatus(ctx, tenantID, subjectID, kbBID)
	require.NoError(t, err)
	require.NotNil(t, scopeB)
	require.NotEqual(t, initialEpoch, scopeB.SubjectEpoch)
	citationProfilePGAPIAuthorizeScope(t, db, scopeB)
	pageBSource := uuid.NewString()
	pageBTarget := uuid.NewString()
	knowledgeBID := uuid.NewString()
	require.NoError(t, insertCitationProfilePGAPIPage(db, tenantID, kbBID, pageBSource, "kb-b/a", "KB B Page A", types.StringArray{"kb-b/b"}, now.Add(2*time.Hour)))
	require.NoError(t, insertCitationProfilePGAPIPage(db, tenantID, kbBID, pageBTarget, "kb-b/b", "KB B Page B", nil, now.Add(2*time.Hour+time.Minute)))
	sourceRefB := citationProfilePGAPISourceRef(tenantID, kbBID, knowledgeBID, pageBTarget, 1, 30, "wm-kb-b", now.Add(2*time.Hour))
	sourceRefB.PageSlug = "kb-b/b"
	sourceRefB.PageTitle = "KB B Page B"
	require.NoError(t, db.Create(sourceRefB).Error)
	eventB := citationProfilePGAPIEvent(tenantID, subjectID, kbBID, scopeB, knowledgeBID, "message-kb-b", 0, now.Add(2*time.Hour))
	require.NoError(t, db.Create(&eventB).Error)
	runB, err := repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, eventB.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileEventStatusResolved, runB.Status)
	nodesB := citationProfilePGAPIJSON[types.CitationProfileNodeListResponse](t, router, http.MethodGet, "/kb/"+kbBID+"/nodes?page_size=10", nil, http.StatusOK)
	nodeBTarget := citationProfilePGAPIFindNode(t, nodesB.Items, pageBTarget)
	require.Equal(t, 1, nodeBTarget.AuthorizedEvidenceCount)
	require.Equal(t, 1, nodeBTarget.CurrentLinkCount)

	status = citationProfilePGAPIJSON[types.CitationProfileStatus](t, router, http.MethodGet, "/kb/"+kbID+"/status", nil, http.StatusOK)
	retract := citationProfilePGAPIJSON[types.CitationProfileCorrectionResponse](t, router, http.MethodPost, "/kb/"+kbID+"/corrections", types.CitationProfileCorrectionRequest{
		ExpectedReadVersion: status.Snapshot.ReadVersion,
		IdempotencyKey:      uuid.NewString(),
		Action:              types.CitationCorrectionRetractEvent,
		EventID:             event.ID,
		PageUUID:            pageB,
		ReasonCode:          "source_should_not_count",
	}, http.StatusOK)
	require.Equal(t, types.CitationCorrectionRetractEvent, retract.Action)
	evidenceAfterRetract := citationProfilePGAPIJSON[types.CitationProfileNodeEvidenceResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes/"+pageB+"/evidence?page_size=10", nil, http.StatusOK)
	require.Empty(t, evidenceAfterRetract.Items)
	nodesAfterRetract := citationProfilePGAPIJSON[types.CitationProfileNodeListResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusOK)
	nodeB = citationProfilePGAPIFindNode(t, nodesAfterRetract.Items, pageB)
	require.Equal(t, 0, nodeB.AuthorizedEvidenceCount)
	require.Equal(t, 0, nodeB.CurrentLinkCount)
	require.Equal(t, 0, nodeB.DisputedLinkCount)
	require.Equal(t, types.EvidenceRelationUnknown, nodeB.Overlay)

	status = citationProfilePGAPIJSON[types.CitationProfileStatus](t, router, http.MethodGet, "/kb/"+kbID+"/status", nil, http.StatusOK)
	// A deployment may turn the presentation feature off while a subject's
	// deletion right is exercised. The off-state service must still durably
	// fence the real scope; turning the feature back on must reveal only the
	// redacted tombstone, never the pre-delete evidence snapshot.
	featureOffRouter := newCitationProfilePGAPIRouterWithConfig(
		repo, tenantID, subjectID, &types.CitationProfileConfig{Enabled: false},
	)
	deleteResp := citationProfilePGAPIJSON[types.CitationProfileDeleteResponse](t, featureOffRouter, http.MethodDelete, "/kb/"+kbID, types.CitationProfileDeleteRequest{
		ExpectedReadVersion: status.Snapshot.ReadVersion,
		IdempotencyKey:      uuid.NewString(),
	}, http.StatusAccepted)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, deleteResp.Status)
	require.Equal(t, types.CitationProfileReceiptHiddenAndFenced, deleteResp.ReceiptCode)
	offStatus := citationProfilePGAPIJSON[types.CitationProfileStatus](t, featureOffRouter, http.MethodGet, "/kb/"+kbID+"/status", nil, http.StatusOK)
	require.False(t, offStatus.Enabled)
	require.False(t, offStatus.Enrolled)
	require.False(t, offStatus.Deleted, "feature-off status must not disclose the retained tombstone")
	deletedStatus := citationProfilePGAPIJSON[types.CitationProfileStatus](t, router, http.MethodGet, "/kb/"+kbID+"/status", nil, http.StatusOK)
	require.True(t, deletedStatus.Deleted)
	require.True(t, deletedStatus.Suspended)
	var deletedScope types.CitationProfileScope
	require.NoError(t, db.Where("id = ?", scope.ID).First(&deletedScope).Error)
	require.NotNil(t, deletedScope.FencedAt)
	require.NotNil(t, deletedScope.DeletedAt)
	require.Equal(t, types.CitationOperationDeleteCurrentACL, deletedScope.FenceReason)
	var revokedExport types.CitationProfileOperation
	require.NoError(t, db.Where("id = ?", exportOp.OperationID).First(&revokedExport).Error)
	require.Equal(t, types.CitationProfileOperationStatusRevoked, revokedExport.Status)
	var retainedEvents, retainedLinks, retainedCorrections int64
	require.NoError(t, db.Model(&types.CitationProfileEvent{}).
		Where("scope_id = ?", scope.ID).Count(&retainedEvents).Error)
	require.NoError(t, db.Model(&types.EvidenceNodeLink{}).
		Where("scope_id = ?", scope.ID).Count(&retainedLinks).Error)
	require.NoError(t, db.Model(&types.CitationProfileCorrection{}).
		Where("scope_id = ?", scope.ID).Count(&retainedCorrections).Error)
	require.Positive(t, retainedEvents, "delete must retain the fenced evidence audit trail")
	require.Positive(t, retainedLinks, "delete must retain the fenced resolution audit trail")
	require.Positive(t, retainedCorrections, "delete must retain the fenced correction audit trail")

	citationProfilePGAPIRaw(t, router, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusGone)
	citationProfilePGAPIRaw(t, router, http.MethodGet, "/kb/"+kbID+"/graph", nil, http.StatusGone)
	citationProfilePGAPIRaw(t, router, http.MethodGet, "/kb/"+kbID+"/nodes/"+pageB+"/evidence?page_size=10", nil, http.StatusGone)
	citationProfilePGAPIRaw(t, router, http.MethodGet, "/kb/"+kbID+"/exports/"+exportOp.OperationID+"/download", nil, http.StatusGone)

	reopenedDB, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
	require.NoError(t, err)
	reopenedSQLDB, err := reopenedDB.DB()
	require.NoError(t, err)
	defer reopenedSQLDB.Close()
	reopenedRouter := newCitationProfilePGAPIRouter(repository.NewCitationProfileRepository(reopenedDB), tenantID, subjectID)
	citationProfilePGAPIRaw(t, reopenedRouter, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusGone)
	nodesBAfterRestart := citationProfilePGAPIJSON[types.CitationProfileNodeListResponse](t, reopenedRouter, http.MethodGet, "/kb/"+kbBID+"/nodes?page_size=10", nil, http.StatusOK)
	nodeBTarget = citationProfilePGAPIFindNode(t, nodesBAfterRestart.Items, pageBTarget)
	require.Equal(t, 1, nodeBTarget.AuthorizedEvidenceCount)
	require.Equal(t, 1, nodeBTarget.CurrentLinkCount)

	reenrollment := citationProfilePGAPIJSON[types.CitationProfileEnrollmentResponse](t, router, http.MethodPut, "/kb/"+kbID+"/enrollment", types.CitationProfileEnrollmentRequest{
		Enabled:             true,
		ExpectedReadVersion: "0",
		IdempotencyKey:      uuid.NewString(),
	}, http.StatusOK)
	require.True(t, reenrollment.Enrolled)
	require.Nil(t, reenrollment.Scope)
	require.Nil(t, reenrollment.Snapshot)
	reenrolledScope, err := repo.GetScopeStatus(ctx, tenantID, subjectID, kbID)
	require.NoError(t, err)
	require.NotEqual(t, initialEpoch, reenrolledScope.SubjectEpoch)
	citationProfilePGAPIAuthorizeScope(t, db, reenrolledScope)
	cleanNodes := citationProfilePGAPIJSON[types.CitationProfileNodeListResponse](t, router, http.MethodGet, "/kb/"+kbID+"/nodes?page_size=10", nil, http.StatusOK)
	nodeB = citationProfilePGAPIFindNode(t, cleanNodes.Items, pageB)
	require.Equal(t, 0, nodeB.AuthorizedEvidenceCount)
	require.Equal(t, 0, nodeB.CurrentLinkCount)
	require.Equal(t, types.EvidenceRelationUnknown, nodeB.Overlay)
	citationProfilePGAPIRaw(t, router, http.MethodGet, "/kb/"+kbID+"/exports/"+exportOp.OperationID+"/download", nil, http.StatusNotFound)
}

func newCitationProfilePGAPIRouter(repo interfaces.CitationProfileRepository, tenantID uint64, subjectID string) *gin.Engine {
	return newCitationProfilePGAPIRouterWithConfig(
		repo, tenantID, subjectID, &types.CitationProfileConfig{Enabled: true},
	)
}

func newCitationProfilePGAPIRouterWithConfig(
	repo interfaces.CitationProfileRepository,
	tenantID uint64,
	subjectID string,
	cfg *types.CitationProfileConfig,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := handler.NewCitationProfileHandler(profileservice.NewCitationProfileService(cfg, repo))
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, subjectID)
		ctx = types.WithAuthenticatedTenantID(ctx, tenantID)
		ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalWebUser, ID: subjectID})
		ctx = types.WithCitationProfileACLBinding(ctx, types.CitationProfileACLBinding{
			PrincipalType:         types.PrincipalWebUser,
			PrincipalID:           subjectID,
			AuthenticatedTenantID: tenantID,
			AccessPath:            types.CitationProfileACLAccessPathOwner,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.GET("/kb/:kb_id/status", h.GetStatus)
	r.GET("/kb/:kb_id/nodes", h.ListNodes)
	r.GET("/kb/:kb_id/graph", h.GetGraph)
	r.GET("/kb/:kb_id/nodes/:page_uuid/evidence", h.ListNodeEvidence)
	r.PUT("/kb/:kb_id/enrollment", h.SetEnrollment)
	r.POST("/kb/:kb_id/corrections", h.ApplyCorrection)
	r.POST("/kb/:kb_id/exports", h.CreateExport)
	r.GET("/kb/:kb_id/exports/:operation_id/download", h.DownloadExport)
	r.DELETE("/kb/:kb_id", h.DeleteCurrentScope)
	return r
}

func citationProfilePGAPIAuthorizeScope(t *testing.T, db *gorm.DB, scope *types.CitationProfileScope) {
	t.Helper()
	require.NotNil(t, scope)
	require.Equal(t, types.CitationProfileACLStateUnknown, scope.ACLCheckState)
	authorizedAt := time.Now().UTC().Truncate(time.Microsecond)
	nextACLCheckAt := authorizedAt.Add(time.Hour)
	result := db.Model(&types.CitationProfileScope{}).
		Where("id = ? AND acl_check_state = ?", scope.ID, types.CitationProfileACLStateUnknown).
		Updates(map[string]interface{}{
			"acl_check_state":      types.CitationProfileACLStateCurrent,
			"acl_checked_at":       authorizedAt,
			"next_acl_check_at":    nextACLCheckAt,
			"profile_read_version": scope.ProfileReadVersion + 1,
			"updated_at":           authorizedAt,
		})
	require.NoError(t, result.Error)
	require.Equal(t, int64(1), result.RowsAffected)
	scope.ACLCheckState = types.CitationProfileACLStateCurrent
	scope.ACLCheckedAt = &authorizedAt
	scope.NextACLCheckAt = &nextACLCheckAt
	scope.ProfileReadVersion++
}

func citationProfilePGAPIJSON[T any](t *testing.T, router *gin.Engine, method, path string, body any, wantStatus int) T {
	t.Helper()
	w := citationProfilePGAPIRaw(t, router, method, path, body, wantStatus)
	var envelope struct {
		Success bool `json:"success"`
		Data    T    `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.True(t, envelope.Success, "body=%s", w.Body.String())
	return envelope.Data
}

func citationProfilePGAPIRaw(t *testing.T, router *gin.Engine, method, path string, body any, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	reader := bytes.NewReader(nil)
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equalf(t, wantStatus, w.Code, "body=%s", w.Body.String())
	return w
}

func ensureCitationProfilePGAPISchema(db *gorm.DB) error {
	if err := db.Exec(`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`).Error; err != nil {
		return err
	}
	exists, err := citationProfilePGAPISchemaExists(db)
	if err != nil {
		return err
	}
	if !exists {
		migrationPath := filepath.Join("..", "..", "..", "migrations", "versioned", "000091_citation_profile.up.sql")
		migrationSQL, err := os.ReadFile(migrationPath)
		if err != nil {
			return err
		}
		if err := db.Exec(string(migrationSQL)).Error; err != nil {
			return err
		}
	}
	migrationPath := filepath.Join("..", "..", "..", "migrations", "versioned", "000092_citation_profile_event_identity.up.sql")
	migrationSQL, err := os.ReadFile(migrationPath)
	if err != nil {
		return err
	}
	if err := db.Exec(string(migrationSQL)).Error; err != nil {
		return err
	}
	migrationPath = filepath.Join("..", "..", "..", "migrations", "versioned", "000093_citation_profile_outbox_due_index.up.sql")
	migrationSQL, err = os.ReadFile(migrationPath)
	if err != nil {
		return err
	}
	if err := db.Exec(string(migrationSQL)).Error; err != nil {
		return err
	}
	if err := ensureCitationProfilePGAPIACLSchema(db); err != nil {
		return err
	}
	return createCitationProfilePGAPIWikiPagesTable(db)
}

func ensureCitationProfilePGAPIACLSchema(db *gorm.DB) error {
	var ready bool
	query := "SELECT " +
		"EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'citation_profile_scopes' AND column_name = 'acl_generation') " +
		"AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'citation_profile_event_outbox' AND column_name = 'retry_budget_paused_at') " +
		"AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'citation_profile_event_outbox' AND column_name = 'lease_until')"
	if err := db.Raw(query).Scan(&ready).Error; err != nil {
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

func citationProfilePGAPISchemaExists(db *gorm.DB) (bool, error) {
	var exists bool
	err := db.Raw(`SELECT EXISTS (
		SELECT 1
		FROM information_schema.tables
		WHERE table_schema = current_schema()
			AND table_name = 'citation_profile_scopes'
	)`).Scan(&exists).Error
	return exists, err
}

func createCitationProfilePGAPIWikiPagesTable(db *gorm.DB) error {
	return db.Exec(`
CREATE TABLE IF NOT EXISTS wiki_pages (
	id VARCHAR(36) PRIMARY KEY,
	tenant_id BIGINT NOT NULL,
	knowledge_base_id VARCHAR(36) NOT NULL,
	slug VARCHAR(255) NOT NULL,
	title VARCHAR(512) NOT NULL,
	page_type VARCHAR(32) NOT NULL,
	status VARCHAR(32) NOT NULL,
	content TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	aliases JSON NOT NULL DEFAULT '[]',
	parent_slug VARCHAR(255) NOT NULL DEFAULT '',
	folder_id VARCHAR(36) NOT NULL DEFAULT '',
	category_path JSON NOT NULL DEFAULT '[]',
	wiki_path VARCHAR(1024) NOT NULL DEFAULT '',
	depth INTEGER NOT NULL DEFAULT 0,
	sort_order INTEGER NOT NULL DEFAULT 0,
	source_refs JSON NOT NULL DEFAULT '[]',
	chunk_refs JSON NOT NULL DEFAULT '[]',
	in_links JSON NOT NULL DEFAULT '[]',
	out_links JSON NOT NULL DEFAULT '[]',
	page_metadata JSON NOT NULL DEFAULT '{}',
	version INTEGER NOT NULL DEFAULT 1,
	last_edit_source VARCHAR(16) NOT NULL DEFAULT '',
	last_editor_id VARCHAR(64) NOT NULL DEFAULT '',
	created_at TIMESTAMP WITH TIME ZONE NOT NULL,
	updated_at TIMESTAMP WITH TIME ZONE NOT NULL,
	deleted_at TIMESTAMP WITH TIME ZONE
)`).Error
}

func insertCitationProfilePGAPIPage(db *gorm.DB, tenantID uint64, kbID, pageID, slug, title string, outLinks types.StringArray, now time.Time) error {
	outLinksJSON, err := json.Marshal(outLinks)
	if err != nil {
		return err
	}
	return db.Exec(
		`INSERT INTO wiki_pages (id, tenant_id, knowledge_base_id, slug, title, page_type, status, out_links, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?::json, ?, ?)`,
		pageID, tenantID, kbID, slug, title, types.WikiPageTypeConcept, types.WikiPageStatusPublished, string(outLinksJSON), now, now,
	).Error
}

func citationProfilePGAPISourceRef(tenantID uint64, kbID, knowledgeID, pageID string, pageVersion int, mappingRevision uint64, watermark string, now time.Time) types.WikiSourceRefIndex {
	return types.WikiSourceRefIndex{
		ID:                uuid.NewString(),
		TenantID:          tenantID,
		KnowledgeBaseID:   kbID,
		SourceKnowledgeID: knowledgeID,
		PageUUID:          pageID,
		PageVersion:       pageVersion,
		PageSlug:          "api/b",
		PageTitle:         "API Page B",
		NormalizedRef:     knowledgeID,
		MappingRevision:   mappingRevision,
		LifecycleState:    "current",
		IndexWatermark:    watermark,
		IndexedAt:         now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func citationProfilePGAPIEvent(tenantID uint64, subjectID, kbID string, scope *types.CitationProfileScope, knowledgeID, messageID string, referenceIndex int, now time.Time) types.CitationProfileEvent {
	return types.CitationProfileEvent{
		ID:                   uuid.NewString(),
		TenantID:             tenantID,
		SubjectID:            subjectID,
		KnowledgeBaseID:      kbID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		SessionID:            uuid.NewString(),
		MessageID:            messageID,
		MessageVersion:       "message-v1",
		MessageCompletedAt:   &now,
		OriginReferenceIndex: referenceIndex,
		SourceKnowledgeID:    knowledgeID,
		SourceResultID:       "source-result",
		SourceRefNormalized:  knowledgeID,
		SourceRefsSnapshot:   json.RawMessage(`{}`),
		KnowledgeSnapshot:    json.RawMessage(`{}`),
		KnowledgeBaseProof:   json.RawMessage(`{"knowledge_base_id":"` + kbID + `"}`),
		ProducerEventKey:     messageID + ":" + knowledgeID,
		Status:               types.CitationProfileEventStatusPendingResolution,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func citationProfilePGAPIFindNode(t *testing.T, nodes []types.CitationProfileNodeDTO, pageUUID string) types.CitationProfileNodeDTO {
	t.Helper()
	for _, node := range nodes {
		if node.PageUUID == pageUUID {
			return node
		}
	}
	t.Fatalf("missing node %s", pageUUID)
	return types.CitationProfileNodeDTO{}
}
