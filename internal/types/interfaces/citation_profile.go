package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

type CitationProfileRepository interface {
	GetScopeStatus(ctx context.Context, tenantID uint64, subjectID string, kbID string) (*types.CitationProfileScope, error)
	ListNodes(ctx context.Context, tenantID uint64, subjectID string, kbID string, cursor *types.CitationProfileCursor, pageSize int) (*types.CitationProfileNodeListResponse, error)
	GetGraph(ctx context.Context, tenantID uint64, subjectID string, kbID string) (*types.CitationProfileGraphResponse, error)
	ListNodeEvidence(ctx context.Context, tenantID uint64, subjectID string, kbID string, pageUUID string, cursor *types.CitationProfileCursor, pageSize int) (*types.CitationProfileNodeEvidenceResponse, error)
	ApplyCorrection(ctx context.Context, tenantID uint64, subjectID string, kbID string, expectedReadVersion uint64, idempotencyKey string, action string, eventID string, pageUUID string, reasonCode string) (*types.CitationProfileCorrection, error)
	SetEnrollment(ctx context.Context, tenantID uint64, subjectID string, kbID string, enabled bool, expectedReadVersion *uint64, idempotencyKey string) (*types.CitationProfileScope, error)
	CompleteAssistantMessageWithEvents(ctx context.Context, tenantID uint64, subjectID string, message *types.Message) (int, error)
	ResolveEvidenceEvent(ctx context.Context, tenantID uint64, subjectID string, eventID string) (*types.EvidenceResolutionRun, error)
	CreateExportOperation(ctx context.Context, tenantID uint64, subjectID string, kbID string, expectedReadVersion uint64, idempotencyKey string, format string) (*types.CitationProfileOperation, error)
	GetExportOperation(ctx context.Context, tenantID uint64, subjectID string, kbID string, operationID string) (*types.CitationProfileOperation, error)
	RequestCurrentACLDelete(ctx context.Context, tenantID uint64, subjectID string, kbID string, expectedReadVersion *uint64, idempotencyKey string) (*types.CitationProfileOperation, error)
	RequestBlindDelete(ctx context.Context, tenantID uint64, subjectID string, kbID string) (*types.CitationProfileOperation, error)
}

type CitationProfileService interface {
	GetStatus(ctx context.Context, kbID string) (*types.CitationProfileStatus, error)
	ListNodes(ctx context.Context, kbID string, cursor string, pageSize int) (*types.CitationProfileNodeListResponse, error)
	GetGraph(ctx context.Context, kbID string) (*types.CitationProfileGraphResponse, error)
	ListNodeEvidence(ctx context.Context, kbID string, pageUUID string, cursor string, pageSize int) (*types.CitationProfileNodeEvidenceResponse, error)
	ApplyCorrection(ctx context.Context, kbID string, req types.CitationProfileCorrectionRequest) (*types.CitationProfileCorrectionResponse, error)
	SetEnrollment(ctx context.Context, kbID string, req types.CitationProfileEnrollmentRequest) (*types.CitationProfileEnrollmentResponse, error)
	CompleteAssistantMessage(ctx context.Context, message *types.Message) (bool, error)
	ResolveEvidenceEvent(ctx context.Context, eventID string) (*types.EvidenceResolutionRun, error)
	CreateExport(ctx context.Context, kbID string, req types.CitationProfileExportRequest) (*types.CitationProfileExportOperationResponse, error)
	GetExport(ctx context.Context, kbID string, operationID string) (*types.CitationProfileExportOperationResponse, error)
	DownloadExport(ctx context.Context, kbID string, operationID string) ([]byte, error)
	RequestCurrentACLDelete(ctx context.Context, kbID string, req types.CitationProfileDeleteRequest) (*types.CitationProfileDeleteResponse, error)
	RequestBlindDelete(ctx context.Context, kbID string) (*types.CitationProfileDeleteResponse, error)
}
