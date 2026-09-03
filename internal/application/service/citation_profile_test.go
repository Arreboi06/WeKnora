package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type spyCitationProfileScopeStore struct {
	statusReads int
	lastTenant  uint64
	lastSubject string
	lastKB      string
	scope       *types.CitationProfileScope

	setEnrollmentCalls int
	lastEnabled        bool
	lastExpected       *uint64
	lastIdem           string
	enrollmentScope    *types.CitationProfileScope

	exportCalls int
	exportOp    *types.CitationProfileOperation

	blindDeleteCalls int
	currentDeletes   int
	deleteOp         *types.CitationProfileOperation

	listNodeCalls int

	lastCursor *types.CitationProfileCursor

	lastPageSize int

	listNodeResp *types.CitationProfileNodeListResponse

	graphCalls int

	graphResp *types.CitationProfileGraphResponse

	evidenceCalls int

	lastPageUUID string

	evidenceResp *types.CitationProfileNodeEvidenceResponse

	correctionCalls int

	lastAction string

	lastEventID string

	lastReasonCode string

	correctionResp *types.CitationProfileCorrection
}

func (s *spyCitationProfileScopeStore) GetScopeStatus(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileScope, error) {
	s.statusReads++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	return s.scope, nil
}

func (s *spyCitationProfileScopeStore) ListNodes(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	cursor *types.CitationProfileCursor,
	pageSize int,
) (*types.CitationProfileNodeListResponse, error) {
	s.listNodeCalls++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	s.lastCursor = cursor
	s.lastPageSize = pageSize
	if s.listNodeResp != nil {
		return s.listNodeResp, nil
	}
	return &types.CitationProfileNodeListResponse{PageSize: pageSize, Guidance: types.CitationProfileDefaultGuidance()}, nil
}

func (s *spyCitationProfileScopeStore) GetGraph(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileGraphResponse, error) {
	s.graphCalls++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	if s.graphResp != nil {
		return s.graphResp, nil
	}
	return &types.CitationProfileGraphResponse{Guidance: types.CitationProfileDefaultGuidance()}, nil
}

func (s *spyCitationProfileScopeStore) ListNodeEvidence(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	pageUUID string,
	cursor *types.CitationProfileCursor,
	pageSize int,
) (*types.CitationProfileNodeEvidenceResponse, error) {
	s.evidenceCalls++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	s.lastPageUUID = pageUUID
	s.lastCursor = cursor
	s.lastPageSize = pageSize
	if s.evidenceResp != nil {
		return s.evidenceResp, nil
	}
	return &types.CitationProfileNodeEvidenceResponse{Guidance: types.CitationProfileDefaultGuidance()}, nil
}

func (s *spyCitationProfileScopeStore) ApplyCorrection(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	expectedReadVersion uint64,
	idempotencyKey string,
	action string,
	eventID string,
	pageUUID string,
	reasonCode string,
) (*types.CitationProfileCorrection, error) {
	s.correctionCalls++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	s.lastExpected = &expectedReadVersion
	s.lastIdem = idempotencyKey
	s.lastAction = action
	s.lastEventID = eventID
	s.lastPageUUID = pageUUID
	s.lastReasonCode = reasonCode
	if s.correctionResp != nil {
		return s.correctionResp, nil
	}
	return &types.CitationProfileCorrection{ID: "correction-1", SubjectEpoch: "epoch-1", CorrectionType: action, ResultingReadVersion: expectedReadVersion + 1}, nil
}
func (s *spyCitationProfileScopeStore) SetEnrollment(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	enabled bool,
	expectedReadVersion *uint64,
	idempotencyKey string,
) (*types.CitationProfileScope, error) {
	s.setEnrollmentCalls++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	s.lastEnabled = enabled
	s.lastExpected = expectedReadVersion
	s.lastIdem = idempotencyKey
	return s.enrollmentScope, nil
}

func (s *spyCitationProfileScopeStore) CompleteAssistantMessageWithEvents(
	context.Context, uint64, string, *types.Message,
) (int, error) {
	return 0, nil
}

func (s *spyCitationProfileScopeStore) ResolveEvidenceEvent(
	context.Context, uint64, string, string,
) (*types.EvidenceResolutionRun, error) {
	return nil, nil
}

func (s *spyCitationProfileScopeStore) CreateExportOperation(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	expectedReadVersion uint64,
	idempotencyKey string,
	format string,
) (*types.CitationProfileOperation, error) {
	s.exportCalls++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	s.lastExpected = &expectedReadVersion
	s.lastIdem = idempotencyKey
	if s.exportOp != nil {
		return s.exportOp, nil
	}
	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)
	snapshot := types.CitationProfileSnapshot{SubjectEpoch: "epoch-1", ReadVersion: "7", CapturedAt: now.Format(time.RFC3339Nano)}
	payload, _ := json.Marshal(map[string]interface{}{"snapshot": snapshot})
	return &types.CitationProfileOperation{
		ID:              "op-export",
		KnowledgeBaseID: kbID,
		SubjectEpoch:    "epoch-1",
		Status:          types.CitationProfileOperationStatusReady,
		ResultSummary:   payload,
		ExpiresAt:       &expiresAt,
	}, nil
}

func (s *spyCitationProfileScopeStore) GetExportOperation(
	context.Context, uint64, string, string, string,
) (*types.CitationProfileOperation, error) {
	return s.exportOp, nil
}

func (s *spyCitationProfileScopeStore) RequestCurrentACLDelete(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	expectedReadVersion *uint64,
	idempotencyKey string,
) (*types.CitationProfileOperation, error) {
	s.currentDeletes++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	s.lastExpected = expectedReadVersion
	s.lastIdem = idempotencyKey
	if s.deleteOp != nil {
		return s.deleteOp, nil
	}
	return &types.CitationProfileOperation{ID: "op-delete", Status: types.CitationProfileOperationStatusAccepted}, nil
}

func (s *spyCitationProfileScopeStore) RequestBlindDelete(
	_ context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileOperation, error) {
	s.blindDeleteCalls++
	s.lastTenant = tenantID
	s.lastSubject = subjectID
	s.lastKB = kbID
	return &types.CitationProfileOperation{ID: "op-blind", Status: types.CitationProfileOperationStatusAccepted}, nil
}

func TestCitationProfileStatusFeatureOffDoesNotReadProfileRows(t *testing.T) {
	store := &spyCitationProfileScopeStore{}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: false}, store)

	ctx := citationProfileTestContext()

	got, err := svc.GetStatus(ctx, "kb-a")
	require.NoError(t, err)
	require.False(t, got.Enabled)
	require.False(t, got.Enrolled)
	require.False(t, got.Deleted)
	require.False(t, got.Suspended)
	require.Nil(t, got.Scope)
	require.Nil(t, got.Snapshot)
	require.Equal(t, types.CitationProfileGuidance{
		Kind:       "none",
		Reason:     "evidence_insufficient",
		Candidates: []string{},
	}, got.Guidance)
	require.Equal(t, types.CitationProfileEmptyState{
		Kind:        "feature_disabled",
		MessageCode: "citation_profile_disabled",
	}, got.EmptyState)
	require.Equal(t, 0, store.statusReads, "default-off must not touch Topic 4 profile storage")
}

func TestCitationProfileStatusUsesServerSubjectAndSnapshot(t *testing.T) {
	store := &spyCitationProfileScopeStore{
		scope: &types.CitationProfileScope{
			ID:                      "scope-1",
			TenantID:                7,
			SubjectID:               "user-7",
			KnowledgeBaseID:         "kb-a",
			SubjectEpoch:            "22f90680-c3c4-4b13-85fb-36ec61b7b95c",
			ProfileReadVersion:      12,
			MappingRevision:         5,
			SourceUniverseWatermark: "wiki-watermark-5",
			PendingEventCount:       2,
			PendingMappingCount:     3,
			DirtyMappingCount:       4,
			ProfilePolicyVersion:    types.CitationProfilePolicyVersion,
			RetentionPolicyVersion:  types.CitationProfileRetentionPolicyVersion,
			Enabled:                 true,
			UpdatedAt:               time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		},
	}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: true}, store)

	got, err := svc.GetStatus(citationProfileTestContext(), " kb-a ")
	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.True(t, got.Enrolled)
	require.False(t, got.Deleted)
	require.False(t, got.Suspended)
	require.Equal(t, uint64(7), store.lastTenant)
	require.Equal(t, "user-7", store.lastSubject)
	require.Equal(t, "kb-a", store.lastKB)
	require.Equal(t, "kb-a", got.Scope.KnowledgeBaseID)
	require.Equal(t, "22f90680-c3c4-4b13-85fb-36ec61b7b95c", got.Snapshot.SubjectEpoch)
	require.Equal(t, "12", got.Snapshot.ReadVersion)
	require.Equal(t, "5", got.Snapshot.MappingRevision)
	require.Equal(t, "wiki-watermark-5", got.Snapshot.SourceUniverseWatermark)
	require.Equal(t, "wiki-watermark-5", got.Snapshot.CurrentWikiUniverseWatermark)
	require.Equal(t, 2, got.Snapshot.PendingEventCount)
	require.Equal(t, 3, got.Snapshot.PendingMappingCount)
	require.Equal(t, 4, got.Snapshot.DirtyMappingCount)
	require.Equal(t, types.CitationProfileGuidance{
		Kind:       "none",
		Reason:     "evidence_insufficient",
		Candidates: []string{},
	}, got.Guidance)
}

func TestCitationProfileListNodesUsesServerSubjectAndCursor(t *testing.T) {
	cursorPayload, err := json.Marshal(types.CitationProfileCursor{
		SchemaVersion:   types.CitationProfileCursorSchemaV1,
		Endpoint:        types.CitationProfileCursorEndpointNodes,
		KnowledgeBaseID: "kb-a",
		PageSize:        50,
		LastPageUUID:    "page-a",
	})
	require.NoError(t, err)
	store := &spyCitationProfileScopeStore{}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: true}, store)

	got, err := svc.ListNodes(citationProfileTestContext(), " kb-a ", base64.RawURLEncoding.EncodeToString(cursorPayload), 50)

	require.NoError(t, err)
	require.Equal(t, 1, store.listNodeCalls)
	require.Equal(t, uint64(7), store.lastTenant)
	require.Equal(t, "user-7", store.lastSubject)
	require.Equal(t, "kb-a", store.lastKB)
	require.NotNil(t, store.lastCursor)
	require.Equal(t, "page-a", store.lastCursor.LastPageUUID)
	require.Equal(t, 50, store.lastPageSize)
	require.Equal(t, 50, got.PageSize)
}
func TestCitationProfileEnrollmentUsesServerSubjectAndGuard(t *testing.T) {
	expected := uint64(16)
	idem := "11111111-1111-4111-8111-111111111111"
	store := &spyCitationProfileScopeStore{
		enrollmentScope: &types.CitationProfileScope{
			ID:                     "scope-2",
			TenantID:               7,
			SubjectID:              "user-7",
			KnowledgeBaseID:        "kb-a",
			SubjectEpoch:           "33333333-3333-4333-8333-333333333333",
			ProfileReadVersion:     17,
			ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
			RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
			Enabled:                true,
		},
	}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: true}, store)

	got, err := svc.SetEnrollment(citationProfileTestContext(), " kb-a ", types.CitationProfileEnrollmentRequest{
		Enabled:             true,
		ExpectedReadVersion: "16",
		IdempotencyKey:      idem,
	})

	require.NoError(t, err)
	require.True(t, got.Enabled)
	require.Equal(t, 1, store.setEnrollmentCalls)
	require.Equal(t, uint64(7), store.lastTenant)
	require.Equal(t, "user-7", store.lastSubject)
	require.Equal(t, "kb-a", store.lastKB)
	require.Equal(t, &expected, store.lastExpected)
	require.Equal(t, idem, store.lastIdem)
	require.Equal(t, "17", got.Snapshot.ReadVersion)
}

func TestCitationProfileCorrectionUsesServerSubjectAndCanonicalIDs(t *testing.T) {
	expected := uint64(17)
	store := &spyCitationProfileScopeStore{}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: true}, store)
	idem := "33333333-3333-4333-8333-333333333333"
	eventID := "44444444-4444-4444-8444-444444444444"
	pageUUID := "55555555-5555-4555-8555-555555555555"

	got, err := svc.ApplyCorrection(citationProfileTestContext(), " kb-a ", types.CitationProfileCorrectionRequest{
		ExpectedReadVersion: "17",
		IdempotencyKey:      idem,
		Action:              types.CitationCorrectionRejectMapping,
		EventID:             eventID,
		PageUUID:            pageUUID,
		ReasonCode:          "wrong_page",
	})

	require.NoError(t, err)
	require.Equal(t, 1, store.correctionCalls)
	require.Equal(t, uint64(7), store.lastTenant)
	require.Equal(t, "user-7", store.lastSubject)
	require.Equal(t, "kb-a", store.lastKB)
	require.Equal(t, &expected, store.lastExpected)
	require.Equal(t, idem, store.lastIdem)
	require.Equal(t, types.CitationCorrectionRejectMapping, store.lastAction)
	require.Equal(t, eventID, store.lastEventID)
	require.Equal(t, pageUUID, store.lastPageUUID)
	require.Equal(t, "18", got.Snapshot.ReadVersion)

	_, err = svc.ApplyCorrection(citationProfileTestContext(), "kb-a", types.CitationProfileCorrectionRequest{
		ExpectedReadVersion: "17",
		IdempotencyKey:      idem,
		Action:              types.CitationCorrectionRejectMapping,
		EventID:             "not-a-uuid",
		PageUUID:            pageUUID,
	})
	require.ErrorIs(t, err, types.ErrCitationProfileInvalidRequest)
}
func TestCitationProfileExportRequiresGuardAndBuildsDownloadURL(t *testing.T) {
	store := &spyCitationProfileScopeStore{}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: true}, store)

	got, err := svc.CreateExport(citationProfileTestContext(), "kb-a", types.CitationProfileExportRequest{
		ExpectedReadVersion: "7",
		IdempotencyKey:      "22222222-2222-4222-8222-222222222222",
		Format:              "json",
	})

	require.NoError(t, err)
	require.Equal(t, 1, store.exportCalls)
	require.Equal(t, "user-7", store.lastSubject)
	require.Equal(t, uint64(7), *store.lastExpected)
	require.Equal(t, types.CitationProfileOperationStatusReady, got.Status)
	require.NotNil(t, got.DownloadURL)
	require.Contains(t, *got.DownloadURL, "/api/v1/knowledgebase/kb-a/citation-profile/exports/op-export/download")
	require.Equal(t, "7", got.Snapshot.ReadVersion)
}

func TestCitationProfileBlindDeleteFeatureOffStillRequiresSubjectButDoesNotReadStore(t *testing.T) {
	store := &spyCitationProfileScopeStore{}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: false}, store)

	_, err := svc.RequestBlindDelete(context.Background(), "kb-a")
	require.ErrorIs(t, err, types.ErrCitationProfileAuthRequired)

	got, err := svc.RequestBlindDelete(citationProfileTestContext(), "kb-a")
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusAccepted, got.Status)
	require.Equal(t, types.CitationProfileReceiptAccepted, got.ReceiptCode)
	require.Equal(t, 0, store.blindDeleteCalls)
}

func citationProfileTestContext() context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(7))
	return context.WithValue(ctx, types.UserIDContextKey, "user-7")
}
