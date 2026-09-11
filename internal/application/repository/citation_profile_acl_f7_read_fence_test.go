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

func TestCitationProfileACLNonCurrentFencesReadsExportsAndResolverPublish(t *testing.T) {
	for _, aclState := range []string{
		types.CitationProfileACLStateUnknown,
		types.CitationProfileACLStateError,
		types.CitationProfileACLStateTimeout,
	} {
		t.Run(aclState, func(t *testing.T) {
			db := newCitationProfileRepositoryTestDB(t)
			repo := NewCitationProfileRepository(db)
			ctx := context.Background()
			now := time.Date(2026, 9, 10, 20, 0, 0, 0, time.UTC)
			scope := citationProfileTestScope(
				"scope-f7-read-fence-"+aclState,
				"user-f7-read-fence-"+aclState,
				"kb-f7-read-fence-"+aclState,
				"epoch-f7-read-fence-"+aclState,
				7,
				1,
			)
			scope.ACLCheckState = aclState
			event := types.CitationProfileEvent{
				ID:                   "event-f7-read-fence-" + aclState,
				TenantID:             scope.TenantID,
				SubjectID:            scope.SubjectID,
				KnowledgeBaseID:      scope.KnowledgeBaseID,
				SubjectEpoch:         scope.SubjectEpoch,
				ScopeID:              scope.ID,
				MessageID:            "message-f7-read-fence-" + aclState,
				OriginReferenceIndex: 0,
				SourceKnowledgeID:    "source-f7-read-fence-" + aclState,
				SourceRefsSnapshot:   json.RawMessage(`{}`),
				KnowledgeSnapshot:    json.RawMessage(`{}`),
				KnowledgeBaseProof:   json.RawMessage(`{}`),
				ProducerEventKey:     "producer-f7-read-fence-" + aclState,
				Status:               types.CitationProfileEventStatusPendingResolution,
				CreatedAt:            now,
				UpdatedAt:            now,
			}
			expiresAt := now.Add(time.Hour)
			export := types.CitationProfileOperation{
				ID:              "export-f7-read-fence-" + aclState,
				TenantID:        scope.TenantID,
				SubjectID:       scope.SubjectID,
				KnowledgeBaseID: scope.KnowledgeBaseID,
				SubjectEpoch:    scope.SubjectEpoch,
				ScopeID:         scope.ID,
				OperationType:   types.CitationOperationExport,
				IdempotencyKey:  "idem-existing-f7-read-fence-" + aclState,
				Status:          types.CitationProfileOperationStatusReady,
				RequestSnapshot: json.RawMessage(`{}`),
				ResultSummary:   json.RawMessage(`{"must_not_serve":true}`),
				ArtifactURI:     "db:result_summary",
				ExpiresAt:       &expiresAt,
				CompletedAt:     &now,
				CreatedAt:       now,
				UpdatedAt:       now,
			}
			require.NoError(t, db.Create(scope).Error)
			require.NoError(t, db.Create(&event).Error)
			require.NoError(t, db.Create(&export).Error)

			_, err := repo.ListNodes(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, nil, 20)
			require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
			_, err = repo.GetGraph(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID)
			require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
			_, err = repo.ListNodeEvidence(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, "page-secret", nil, 20)
			require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
			_, err = repo.CreateExportOperation(
				ctx,
				scope.TenantID,
				scope.SubjectID,
				scope.KnowledgeBaseID,
				scope.ProfileReadVersion,
				"idem-new-f7-read-fence-"+aclState,
				types.CitationProfileExportFormatJSON,
			)
			require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
			_, err = repo.GetExportOperation(ctx, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, export.ID)
			require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)
			_, err = repo.ResolveEvidenceEvent(ctx, scope.TenantID, scope.SubjectID, event.ID)
			require.ErrorIs(t, err, types.ErrCitationProfileUnavailable)

			var runCount, linkCount, exportCount int64
			require.NoError(t, db.Model(&types.EvidenceResolutionRun{}).Where("event_id = ?", event.ID).Count(&runCount).Error)
			require.NoError(t, db.Model(&types.EvidenceNodeLink{}).Where("event_id = ?", event.ID).Count(&linkCount).Error)
			require.NoError(t, db.Model(&types.CitationProfileOperation{}).
				Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID).
				Count(&exportCount).Error)
			require.Zero(t, runCount, "non-current ACL must prevent resolver publication")
			require.Zero(t, linkCount, "non-current ACL must prevent evidence publication")
			require.Equal(t, int64(1), exportCount, "non-current ACL must not create another export")

			var storedEvent types.CitationProfileEvent
			require.NoError(t, db.First(&storedEvent, "id = ?", event.ID).Error)
			require.Equal(t, types.CitationProfileEventStatusPendingResolution, storedEvent.Status)
			require.Empty(t, storedEvent.ActiveRunID)
		})
	}
}
