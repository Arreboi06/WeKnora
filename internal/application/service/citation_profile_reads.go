package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

func (s *citationProfileService) ListNodes(
	ctx context.Context,
	kbID string,
	cursorToken string,
	pageSize int,
) (*types.CitationProfileNodeListResponse, error) {
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
	pageSize = citationProfilePageSize(pageSize)
	cursor, err := citationProfileDecodeCursor(cursorToken, types.CitationProfileCursorEndpointNodes, kbID, pageSize)
	if err != nil {
		return nil, err
	}
	return s.repo.ListNodes(ctx, tenantID, subjectID, kbID, cursor, pageSize)
}

func (s *citationProfileService) GetGraph(ctx context.Context, kbID string) (*types.CitationProfileGraphResponse, error) {
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
	return s.repo.GetGraph(ctx, tenantID, subjectID, kbID)
}

func (s *citationProfileService) ListNodeEvidence(
	ctx context.Context,
	kbID string,
	pageUUID string,
	cursorToken string,
	pageSize int,
) (*types.CitationProfileNodeEvidenceResponse, error) {
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
	pageUUID = strings.TrimSpace(pageUUID)
	if pageUUID == "" {
		return nil, fmt.Errorf("%w: page_uuid is required", types.ErrCitationProfileInvalidRequest)
	}
	pageSize = citationProfilePageSize(pageSize)
	cursor, err := citationProfileDecodeCursor(cursorToken, types.CitationProfileCursorEndpointEvidence, kbID, pageSize)
	if err != nil {
		return nil, err
	}
	return s.repo.ListNodeEvidence(ctx, tenantID, subjectID, kbID, pageUUID, cursor, pageSize)
}

func citationProfilePageSize(requested int) int {
	limits := types.CitationProfileDefaultLimits()
	if requested <= 0 {
		return limits.ListPageSize
	}
	if requested > limits.ListPageSize {
		return limits.ListPageSize
	}
	return requested
}

func citationProfileDecodeCursor(token string, endpoint string, kbID string, pageSize int) (*types.CitationProfileCursor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(token)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: cursor is malformed", types.ErrCitationProfileInvalidRequest)
	}
	var cursor types.CitationProfileCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("%w: cursor is malformed", types.ErrCitationProfileInvalidRequest)
	}
	if cursor.SchemaVersion != types.CitationProfileCursorSchemaV1 ||
		cursor.Endpoint != endpoint ||
		cursor.KnowledgeBaseID != kbID ||
		cursor.PageSize != pageSize {
		return nil, types.ErrCitationProfileChanged
	}
	return &cursor, nil
}
