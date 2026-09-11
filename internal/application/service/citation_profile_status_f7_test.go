package service

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileStatusRedactsEveryNonCurrentScope(t *testing.T) {
	for _, state := range []string{
		types.CitationProfileACLStateUnknown,
		types.CitationProfileACLStateDenied,
		types.CitationProfileACLStateError,
		types.CitationProfileACLStateTimeout,
		types.CitationProfileACLStateCurrent,
	} {
		t.Run(state, func(t *testing.T) {
			store := &spyCitationProfileScopeStore{scope: &types.CitationProfileScope{
				ID:                      "sensitive-scope-id",
				TenantID:                7,
				SubjectID:               "user-7",
				KnowledgeBaseID:         "kb-a",
				SubjectEpoch:            "sensitive-epoch",
				ProfileReadVersion:      912,
				MappingRevision:         44,
				SourceUniverseWatermark: "sensitive-watermark",
				PendingEventCount:       3,
				PendingMappingCount:     4,
				DirtyMappingCount:       5,
				Enabled:                 true,
				ACLCheckState:           state,
				ACLCurrent:              false,
			}}
			svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: true}, store)

			got, err := svc.GetStatus(citationProfileTestContext(), "kb-a")
			require.NoError(t, err)
			require.True(t, got.Enrolled)
			require.True(t, got.Suspended)
			require.Nil(t, got.Scope)
			require.Nil(t, got.Snapshot)
			require.Equal(t, types.CitationProfileEmptyACLUnknown, got.EmptyState.Kind)
			require.Equal(t, types.CitationProfileMessageACLUnknown, got.EmptyState.MessageCode)
		})
	}
}

func TestCitationProfileExportServiceTrustsRepositoryDBDerivedStatus(t *testing.T) {
	pastApplicationDeadline := time.Now().UTC().Add(-time.Hour)
	payload := []byte(`{"snapshot":{"read_version":"7"}}`)
	store := &spyCitationProfileScopeStore{exportOp: &types.CitationProfileOperation{
		ID:              "export-db-authoritative",
		KnowledgeBaseID: "kb-a",
		SubjectEpoch:    "epoch-a",
		Status:          types.CitationProfileOperationStatusReady,
		ResultSummary:   payload,
		ExpiresAt:       &pastApplicationDeadline,
	}}
	svc := NewCitationProfileService(&types.CitationProfileConfig{Enabled: true}, store)

	response, err := svc.GetExport(citationProfileTestContext(), "kb-a", store.exportOp.ID)
	require.NoError(t, err)
	require.Equal(t, types.CitationProfileOperationStatusReady, response.Status)
	require.NotNil(t, response.DownloadURL)

	download, err := svc.DownloadExport(citationProfileTestContext(), "kb-a", store.exportOp.ID)
	require.NoError(t, err)
	require.Equal(t, payload, download)
}
