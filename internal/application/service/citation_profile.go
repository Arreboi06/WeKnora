package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

type citationProfileService struct {
	config types.CitationProfileConfig
	repo   interfaces.CitationProfileRepository
}

func NewCitationProfileService(
	configInfo *types.CitationProfileConfig,
	repo interfaces.CitationProfileRepository,
) interfaces.CitationProfileService {
	cfg := types.CitationProfileConfig{}
	if configInfo != nil {
		cfg = *configInfo
	}
	return &citationProfileService{config: cfg, repo: repo}
}

func (s *citationProfileService) CompleteAssistantMessage(ctx context.Context, message *types.Message) (bool, error) {
	if !s.config.Enabled || message == nil || len(message.KnowledgeReferences) == 0 {
		return false, nil
	}
	if s.repo == nil {
		return true, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, err := citationProfileSubject(ctx)
	if err != nil {
		return true, err
	}
	_, err = s.repo.CompleteAssistantMessageWithEvents(ctx, tenantID, subjectID, message)
	return true, err
}

func (s *citationProfileService) ResolveEvidenceEvent(ctx context.Context, eventID string) (*types.EvidenceResolutionRun, error) {
	if !s.config.Enabled {
		return nil, types.ErrCitationProfileDisabled
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, err := citationProfileSubject(ctx)
	if err != nil {
		return nil, err
	}
	return s.repo.ResolveEvidenceEvent(ctx, tenantID, subjectID, strings.TrimSpace(eventID))
}

func (s *citationProfileService) GetStatus(ctx context.Context, kbID string) (*types.CitationProfileStatus, error) {
	status := baseCitationProfileStatus(s.config.Enabled)
	kbID = strings.TrimSpace(kbID)

	if !s.config.Enabled {
		status.EmptyState = types.CitationProfileEmptyState{
			Kind:        types.CitationProfileEmptyFeatureDisabled,
			MessageCode: types.CitationProfileMessageDisabled,
		}
		return status, nil
	}

	tenantID, subjectID, err := citationProfileSubject(ctx)
	if err != nil {
		return nil, err
	}
	if kbID == "" {
		return nil, fmt.Errorf("%w: knowledge base id is required", types.ErrCitationProfileInvalidRequest)
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}

	scope, err := s.repo.GetScopeStatus(ctx, tenantID, subjectID, kbID)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		status.EmptyState = types.CitationProfileEmptyState{
			Kind:        types.CitationProfileEmptyNotEnrolled,
			MessageCode: types.CitationProfileMessageNotEnrolled,
		}
		return status, nil
	}

	status.Enrolled = true
	status.Deleted = scope.Deleted()
	status.Suspended = scope.Suspended()
	status.Scope = scope.DTO()
	status.Snapshot = citationProfileSnapshotFromScope(scope)
	if status.Suspended && strings.TrimSpace(scope.ACLCheckState) != "" && scope.ACLCheckState != "current" {
		status.EmptyState = types.CitationProfileEmptyState{
			Kind:        types.CitationProfileEmptyACLUnknown,
			MessageCode: types.CitationProfileMessageACLUnknown,
		}
	}
	return status, nil
}

func (s *citationProfileService) SetEnrollment(
	ctx context.Context,
	kbID string,
	req types.CitationProfileEnrollmentRequest,
) (*types.CitationProfileEnrollmentResponse, error) {
	if !s.config.Enabled {
		return nil, types.ErrCitationProfileDisabled
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, kbID, err := citationProfileScopeInput(ctx, kbID)
	if err != nil {
		return nil, err
	}
	expected, err := citationProfileReadVersion(req.ExpectedReadVersion, false)
	if err != nil {
		return nil, err
	}
	idempotencyKey, err := citationProfileIdempotencyKey(req.IdempotencyKey, true)
	if err != nil {
		return nil, err
	}

	scope, err := s.repo.SetEnrollment(ctx, tenantID, subjectID, kbID, req.Enabled, expected, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if scope == nil {
		return &types.CitationProfileEnrollmentResponse{
			Enabled:  false,
			Enrolled: false,
			Snapshot: citationProfileZeroSnapshot(),
		}, nil
	}
	return &types.CitationProfileEnrollmentResponse{
		Enabled:  scope.Enabled,
		Enrolled: !scope.Deleted(),
		Scope:    scope.DTO(),
		Snapshot: citationProfileSnapshotFromScope(scope),
	}, nil
}

func (s *citationProfileService) CreateExport(
	ctx context.Context,
	kbID string,
	req types.CitationProfileExportRequest,
) (*types.CitationProfileExportOperationResponse, error) {
	if !s.config.Enabled {
		return nil, types.ErrCitationProfileDisabled
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, kbID, err := citationProfileScopeInput(ctx, kbID)
	if err != nil {
		return nil, err
	}
	expected, err := citationProfileReadVersion(req.ExpectedReadVersion, true)
	if err != nil {
		return nil, err
	}
	idempotencyKey, err := citationProfileIdempotencyKey(req.IdempotencyKey, true)
	if err != nil {
		return nil, err
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = types.CitationProfileExportFormatJSON
	}
	if format != types.CitationProfileExportFormatJSON {
		return nil, fmt.Errorf("%w: unsupported export format", types.ErrCitationProfileInvalidRequest)
	}

	op, err := s.repo.CreateExportOperation(ctx, tenantID, subjectID, kbID, *expected, idempotencyKey, format)
	if err != nil {
		return nil, err
	}
	return citationProfileExportResponse(kbID, op), nil
}

func (s *citationProfileService) GetExport(
	ctx context.Context,
	kbID string,
	operationID string,
) (*types.CitationProfileExportOperationResponse, error) {
	if !s.config.Enabled {
		return nil, types.ErrCitationProfileDisabled
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, kbID, err := citationProfileScopeInput(ctx, kbID)
	if err != nil {
		return nil, err
	}
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, fmt.Errorf("%w: operation id is required", types.ErrCitationProfileInvalidRequest)
	}
	op, err := s.repo.GetExportOperation(ctx, tenantID, subjectID, kbID, operationID)
	if err != nil {
		return nil, err
	}
	return citationProfileExportResponse(kbID, op), nil
}

func (s *citationProfileService) DownloadExport(ctx context.Context, kbID string, operationID string) ([]byte, error) {
	if !s.config.Enabled {
		return nil, types.ErrCitationProfileDisabled
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, kbID, err := citationProfileScopeInput(ctx, kbID)
	if err != nil {
		return nil, err
	}
	op, err := s.repo.GetExportOperation(ctx, tenantID, subjectID, kbID, strings.TrimSpace(operationID))
	if err != nil {
		return nil, err
	}
	if citationProfileOperationExpired(op) || op.Status != types.CitationProfileOperationStatusReady || len(op.ResultSummary) == 0 {
		return nil, types.ErrCitationProfileNotFound
	}
	return append([]byte(nil), op.ResultSummary...), nil
}

func (s *citationProfileService) RequestCurrentACLDelete(
	ctx context.Context,
	kbID string,
	req types.CitationProfileDeleteRequest,
) (*types.CitationProfileDeleteResponse, error) {
	if !s.config.Enabled {
		if _, _, _, err := citationProfileScopeInput(ctx, kbID); err != nil {
			return nil, err
		}
		return citationProfileSyntheticDelete(types.CitationOperationDeleteCurrentACL, types.CitationProfileReceiptHiddenPurgeScheduled), nil
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, kbID, err := citationProfileScopeInput(ctx, kbID)
	if err != nil {
		return nil, err
	}
	expected, err := citationProfileReadVersion(req.ExpectedReadVersion, false)
	if err != nil {
		return nil, err
	}
	idempotencyKey, err := citationProfileIdempotencyKey(req.IdempotencyKey, false)
	if err != nil {
		return nil, err
	}

	op, err := s.repo.RequestCurrentACLDelete(ctx, tenantID, subjectID, kbID, expected, idempotencyKey)
	if err != nil {
		return nil, err
	}
	return citationProfileDeleteResponse(op, types.CitationProfileReceiptHiddenPurgeScheduled), nil
}

func (s *citationProfileService) RequestBlindDelete(
	ctx context.Context,
	kbID string,
) (*types.CitationProfileDeleteResponse, error) {
	if !s.config.Enabled {
		if _, _, _, err := citationProfileScopeInput(ctx, kbID); err != nil {
			return nil, err
		}
		return citationProfileSyntheticDelete(types.CitationOperationDeleteBlind, types.CitationProfileReceiptAccepted), nil
	}
	if s.repo == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	tenantID, subjectID, kbID, err := citationProfileScopeInput(ctx, kbID)
	if err != nil {
		return nil, err
	}
	op, err := s.repo.RequestBlindDelete(ctx, tenantID, subjectID, kbID)
	if err != nil {
		return nil, err
	}
	return citationProfileDeleteResponse(op, types.CitationProfileReceiptAccepted), nil
}

func citationProfileSubject(ctx context.Context) (uint64, string, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return 0, "", types.ErrCitationProfileAuthRequired
	}
	subjectID := strings.TrimSpace(types.SessionOwnerIDFromContext(ctx))
	if subjectID == "" {
		return 0, "", types.ErrCitationProfileAuthRequired
	}
	return tenantID, subjectID, nil
}

func citationProfileScopeInput(ctx context.Context, kbID string) (uint64, string, string, error) {
	tenantID, subjectID, err := citationProfileSubject(ctx)
	if err != nil {
		return 0, "", "", err
	}
	kbID = strings.TrimSpace(kbID)
	if kbID == "" {
		return 0, "", "", fmt.Errorf("%w: knowledge base id is required", types.ErrCitationProfileInvalidRequest)
	}
	return tenantID, subjectID, kbID, nil
}

func citationProfileReadVersion(value string, required bool) (*uint64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return nil, fmt.Errorf("%w: expected_read_version is required", types.ErrCitationProfileInvalidRequest)
		}
		return nil, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: expected_read_version must be a decimal string", types.ErrCitationProfileInvalidRequest)
	}
	return &parsed, nil
}

func citationProfileIdempotencyKey(value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("%w: idempotency_key is required", types.ErrCitationProfileInvalidRequest)
		}
		return uuid.NewString(), nil
	}
	if _, err := uuid.Parse(value); err != nil {
		return "", fmt.Errorf("%w: idempotency_key must be a UUID", types.ErrCitationProfileInvalidRequest)
	}
	return value, nil
}

func citationProfileSnapshotFromScope(scope *types.CitationProfileScope) *types.CitationProfileSnapshot {
	if scope == nil {
		return nil
	}
	watermark := strings.TrimSpace(scope.SourceUniverseWatermark)
	return &types.CitationProfileSnapshot{
		SubjectEpoch:                 scope.SubjectEpoch,
		ReadVersion:                  strconv.FormatUint(scope.ProfileReadVersion, 10),
		MappingRevision:              strconv.FormatUint(scope.MappingRevision, 10),
		SourceUniverseWatermark:      watermark,
		CurrentIndexWatermark:        watermark,
		CurrentWikiUniverseWatermark: watermark,
		PendingEventCount:            scope.PendingEventCount,
		PendingMappingCount:          scope.PendingMappingCount,
		DirtyEventCount:              scope.DirtyMappingCount,
		DirtyMappingCount:            scope.DirtyMappingCount,
		CapturedAt:                   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func citationProfileZeroSnapshot() *types.CitationProfileSnapshot {
	return &types.CitationProfileSnapshot{
		ReadVersion: "0",
		CapturedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func citationProfileExportResponse(kbID string, op *types.CitationProfileOperation) *types.CitationProfileExportOperationResponse {
	if op == nil {
		return nil
	}
	status := op.Status
	if citationProfileOperationExpired(op) && (status == types.CitationProfileOperationStatusReady || status == types.CitationProfileOperationStatusPreparing) {
		status = types.CitationProfileOperationStatusExpired
	}
	var expiresAt string
	if op.ExpiresAt != nil && !op.ExpiresAt.IsZero() {
		expiresAt = op.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	var downloadURL *string
	if status == types.CitationProfileOperationStatusReady && len(op.ResultSummary) > 0 {
		url := fmt.Sprintf("/api/v1/knowledgebase/%s/citation-profile/exports/%s/download", kbID, op.ID)
		downloadURL = &url
	}
	return &types.CitationProfileExportOperationResponse{
		OperationID:   op.ID,
		Status:        status,
		Snapshot:      citationProfileOperationSnapshot(op),
		ExpiresAt:     expiresAt,
		DownloadURL:   downloadURL,
		SchemaVersion: types.CitationProfileExportSchemaVersion,
	}
}

func citationProfileOperationSnapshot(op *types.CitationProfileOperation) *types.CitationProfileSnapshot {
	if op == nil {
		return nil
	}
	var raw struct {
		Snapshot types.CitationProfileSnapshot `json:"snapshot"`
	}
	if len(op.ResultSummary) > 0 && json.Unmarshal(op.ResultSummary, &raw) == nil && raw.Snapshot.ReadVersion != "" {
		return &raw.Snapshot
	}
	return &types.CitationProfileSnapshot{
		SubjectEpoch: op.SubjectEpoch,
		ReadVersion:  "0",
		CapturedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func citationProfileOperationExpired(op *types.CitationProfileOperation) bool {
	return op != nil && op.ExpiresAt != nil && !op.ExpiresAt.IsZero() && time.Now().UTC().After(op.ExpiresAt.UTC())
}

func citationProfileDeleteResponse(op *types.CitationProfileOperation, receipt string) *types.CitationProfileDeleteResponse {
	if op == nil || strings.TrimSpace(op.ID) == "" {
		return citationProfileSyntheticDelete(types.CitationOperationDeleteBlind, receipt)
	}
	return &types.CitationProfileDeleteResponse{
		OperationID: op.ID,
		Status:      types.CitationProfileOperationStatusAccepted,
		ReceiptCode: receipt,
	}
}

func citationProfileSyntheticDelete(operationType string, receipt string) *types.CitationProfileDeleteResponse {
	_ = operationType
	return &types.CitationProfileDeleteResponse{
		OperationID: uuid.NewString(),
		Status:      types.CitationProfileOperationStatusAccepted,
		ReceiptCode: receipt,
	}
}

func baseCitationProfileStatus(enabled bool) *types.CitationProfileStatus {
	return &types.CitationProfileStatus{
		Enabled:  enabled,
		Guidance: types.CitationProfileDefaultGuidance(),
		Limits:   types.CitationProfileDefaultLimits(),
	}
}
