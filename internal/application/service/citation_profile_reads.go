package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	citationProfileCursorMaxPayloadLength = 16 * 1024
	citationProfileCursorDomain           = "weknora:citation-profile:cursor:v1"
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
	response, err := s.repo.ListNodes(ctx, tenantID, subjectID, kbID, cursor, pageSize)
	if err != nil {
		return nil, err
	}
	return citationProfileSignNodeListCursor(response)
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
	response, err := s.repo.ListNodeEvidence(ctx, tenantID, subjectID, kbID, pageUUID, cursor, pageSize)
	if err != nil {
		return nil, err
	}
	return citationProfileSignEvidenceCursor(response)
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
	if len(token) > citationProfileCursorMaxPayloadLength+1+base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return nil, fmt.Errorf("%w: cursor is malformed", types.ErrCitationProfileInvalidRequest)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("%w: cursor is malformed", types.ErrCitationProfileInvalidRequest)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size ||
		!hmac.Equal(signature, citationProfileCursorSignature(parts[0])) {
		return nil, fmt.Errorf("%w: cursor signature is invalid", types.ErrCitationProfileInvalidRequest)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(raw) > citationProfileCursorMaxPayloadLength ||
		base64.RawURLEncoding.EncodeToString(raw) != parts[0] {
		return nil, fmt.Errorf("%w: cursor is malformed", types.ErrCitationProfileInvalidRequest)
	}
	var cursor types.CitationProfileCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("%w: cursor is malformed", types.ErrCitationProfileInvalidRequest)
	}
	if cursor.SchemaVersion != types.CitationProfileCursorSchemaV1 ||
		cursor.Endpoint != endpoint ||
		cursor.KnowledgeBaseID != kbID ||
		cursor.Sort != types.CitationProfileCursorSortIDAsc ||
		cursor.PageSize != pageSize {
		return nil, types.ErrCitationProfileChanged
	}
	return &cursor, nil
}

func citationProfileSignCursorToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) > citationProfileCursorMaxPayloadLength ||
		base64.RawURLEncoding.EncodeToString(raw) != token {
		return "", fmt.Errorf("%w: generated cursor is malformed", types.ErrCitationProfileUnavailable)
	}
	signature := base64.RawURLEncoding.EncodeToString(citationProfileCursorSignature(token))
	return token + "." + signature, nil
}

func citationProfileCursorSignature(payload string) []byte {
	mac := hmac.New(sha256.New, []byte(getJwtSecret()))
	_, _ = mac.Write([]byte(citationProfileCursorDomain))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func citationProfileSignNodeListCursor(
	response *types.CitationProfileNodeListResponse,
) (*types.CitationProfileNodeListResponse, error) {
	if response == nil || response.NextCursor == nil {
		return response, nil
	}
	signed, err := citationProfileSignCursorToken(*response.NextCursor)
	if err != nil {
		return nil, err
	}
	copyResponse := *response
	copyResponse.NextCursor = &signed
	return &copyResponse, nil
}

func citationProfileSignEvidenceCursor(
	response *types.CitationProfileNodeEvidenceResponse,
) (*types.CitationProfileNodeEvidenceResponse, error) {
	if response == nil || response.NextCursor == nil {
		return response, nil
	}
	signed, err := citationProfileSignCursorToken(*response.NextCursor)
	if err != nil {
		return nil, err
	}
	copyResponse := *response
	copyResponse.NextCursor = &signed
	return &copyResponse, nil
}
