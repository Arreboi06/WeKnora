package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

func (s *citationProfileService) ApplyCorrection(
	ctx context.Context,
	kbID string,
	req types.CitationProfileCorrectionRequest,
) (*types.CitationProfileCorrectionResponse, error) {
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
	action, err := citationProfileCorrectionAction(req.Action)
	if err != nil {
		return nil, err
	}
	eventID, err := citationProfileCanonicalUUID(req.EventID, "event_id")
	if err != nil {
		return nil, err
	}
	pageUUID, err := citationProfileCanonicalUUID(req.PageUUID, "page_uuid")
	if err != nil {
		return nil, err
	}
	reasonCode := strings.TrimSpace(req.ReasonCode)
	if len(reasonCode) > 512 {
		return nil, fmt.Errorf("%w: reason_code is too long", types.ErrCitationProfileInvalidRequest)
	}

	correction, err := s.repo.ApplyCorrection(ctx, tenantID, subjectID, kbID, *expected, idempotencyKey, action, eventID, pageUUID, reasonCode)
	if err != nil {
		return nil, err
	}
	return citationProfileCorrectionResponse(correction), nil
}

func citationProfileCorrectionAction(raw string) (string, error) {
	action := strings.TrimSpace(raw)
	switch action {
	case types.CitationCorrectionConfirmRelevant, types.CitationCorrectionRejectMapping, types.CitationCorrectionRetractEvent:
		return action, nil
	default:
		return "", fmt.Errorf("%w: unsupported correction action", types.ErrCitationProfileInvalidRequest)
	}
}

func citationProfileCanonicalUUID(raw string, field string) (string, error) {
	value := strings.TrimSpace(raw)
	parsed, err := uuid.Parse(value)
	if value == "" || err != nil || parsed.String() != value {
		return "", fmt.Errorf("%w: %s must be a canonical UUID", types.ErrCitationProfileInvalidRequest, field)
	}
	return value, nil
}

func citationProfileCorrectionResponse(correction *types.CitationProfileCorrection) *types.CitationProfileCorrectionResponse {
	if correction == nil {
		return nil
	}
	eventCutoff, correctionCutoff, activeRunPointerCutoff := types.CitationProfileSnapshotCutoffs(correction.ResultingReadVersion, correction.CreatedAt)
	return &types.CitationProfileCorrectionResponse{
		CorrectionID: correction.ID,
		Action:       correction.CorrectionType,
		Snapshot: &types.CitationProfileSnapshot{
			SubjectEpoch:           correction.SubjectEpoch,
			ReadVersion:            strconv.FormatUint(correction.ResultingReadVersion, 10),
			EventCutoff:            eventCutoff,
			CorrectionCutoff:       correctionCutoff,
			ActiveRunPointerCutoff: activeRunPointerCutoff,
			CapturedAt:             correction.CreatedAt.UTC().Format(time.RFC3339Nano),
		},
		Result: "applied",
	}
}
