//go:build cgo

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileCorrectionRejectsNewActionAfterRetraction(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	now := time.Now().UTC()
	scope := citationProfileTestScope("scope-correction-terminal", "subject-correction-terminal", "kb-correction-terminal", "epoch-correction-terminal", 10, 0)
	require.NoError(t, db.Create(scope).Error)
	event := &types.CitationProfileEvent{
		ID:                   "event-correction-terminal",
		TenantID:             7,
		SubjectID:            scope.SubjectID,
		KnowledgeBaseID:      scope.KnowledgeBaseID,
		SubjectEpoch:         scope.SubjectEpoch,
		ScopeID:              scope.ID,
		MessageID:            "message-correction-terminal",
		OriginReferenceIndex: 0,
		SourceKnowledgeID:    "knowledge-correction-terminal",
		SourceRefsSnapshot:   json.RawMessage(`{}`),
		KnowledgeSnapshot:    json.RawMessage(`{}`),
		KnowledgeBaseProof:   json.RawMessage(`{}`),
		ProducerEventKey:     "event-correction-terminal-key",
		Status:               types.CitationProfileEventStatusResolved,
		ActiveRunID:          "run-correction-terminal",
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	require.NoError(t, db.Create(event).Error)
	link := &types.EvidenceNodeLink{
		ID:                "link-correction-terminal",
		TenantID:          scope.TenantID,
		SubjectID:         scope.SubjectID,
		KnowledgeBaseID:   scope.KnowledgeBaseID,
		SubjectEpoch:      scope.SubjectEpoch,
		ScopeID:           scope.ID,
		EventID:           event.ID,
		ResolutionRunID:   event.ActiveRunID,
		SourceKnowledgeID: event.SourceKnowledgeID,
		PageUUID:          "page-correction-terminal",
		PageVersion:       1,
		NormalizedRef:     event.SourceKnowledgeID,
		RelationState:     types.EvidenceRelationCurrent,
		RelationSource:    "source_ref_index",
		MappingRevision:   1,
		CreatedAt:         now,
	}
	require.NoError(t, db.Create(link).Error)

	first, err := repo.ApplyCorrection(context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.ProfileReadVersion, "retract-1", types.CitationCorrectionRetractEvent, event.ID, link.PageUUID, "user_requested")
	require.NoError(t, err)
	require.Equal(t, types.CitationCorrectionRetractEvent, first.CorrectionType)

	var afterFirst types.CitationProfileScope
	require.NoError(t, db.First(&afterFirst, "id = ?", scope.ID).Error)
	var eventAfterFirst types.CitationProfileEvent
	require.NoError(t, db.First(&eventAfterFirst, "id = ?", event.ID).Error)
	var linkAfterFirst types.EvidenceNodeLink
	require.NoError(t, db.First(&linkAfterFirst, "id = ?", link.ID).Error)
	correctionCountBefore := int64(0)
	require.NoError(t, db.Model(&types.CitationProfileCorrection{}).Where("scope_id = ?", scope.ID).Count(&correctionCountBefore).Error)

	_, err = repo.ApplyCorrection(context.Background(), scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, afterFirst.ProfileReadVersion, "retract-2", types.CitationCorrectionRetractEvent, event.ID, link.PageUUID, "duplicate_terminal_action")
	require.ErrorIs(t, err, types.ErrCitationProfileDeleted)

	var afterSecond types.CitationProfileScope
	require.NoError(t, db.First(&afterSecond, "id = ?", scope.ID).Error)
	var eventAfterSecond types.CitationProfileEvent
	require.NoError(t, db.First(&eventAfterSecond, "id = ?", event.ID).Error)
	var linkAfterSecond types.EvidenceNodeLink
	require.NoError(t, db.First(&linkAfterSecond, "id = ?", link.ID).Error)
	var correctionCountAfter int64
	require.NoError(t, db.Model(&types.CitationProfileCorrection{}).Where("scope_id = ?", scope.ID).Count(&correctionCountAfter).Error)
	require.Equal(t, afterFirst.ProfileReadVersion, afterSecond.ProfileReadVersion)
	require.Equal(t, eventAfterFirst.RetractedAt, eventAfterSecond.RetractedAt)
	require.Equal(t, linkAfterFirst.RelationState, linkAfterSecond.RelationState)
	require.Equal(t, correctionCountBefore, correctionCountAfter)
}
