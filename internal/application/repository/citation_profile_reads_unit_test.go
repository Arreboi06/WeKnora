package repository

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileEvidenceItemDowngradesStaleCurrentSourceRef(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	scope := &types.CitationProfileScope{MappingRevision: 10, SourceUniverseWatermark: "wm-current"}
	link := types.EvidenceNodeLink{
		ID:                "link-1",
		EventID:           "event-1",
		ResolutionRunID:   "run-1",
		SourceKnowledgeID: "knowledge-1",
		PageUUID:          "page-1",
		PageVersion:       3,
		NormalizedRef:     "knowledge-1",
		RelationState:     types.EvidenceRelationCurrent,
		MappingRevision:   10,
		UniverseWatermark: "wm-current",
	}
	event := types.CitationProfileEvent{ID: "event-1", MessageID: "message-1", CreatedAt: now}
	run := types.EvidenceResolutionRun{ID: "run-1", ResolvedAt: &now, RunMappingRevision: 10, RunUniverseWatermark: "wm-current"}

	item := citationProfileEvidenceItem(scope, link, event, run, nil, map[citationProfileSourceRefKey]struct{}{})

	require.True(t, item.StaleMapping)
	require.Equal(t, types.EvidenceRelationHistorical, item.Overlay)
}

func TestCitationProfileEvidenceItemKeepsCurrentWhenSourceRefKeyMatches(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	scope := &types.CitationProfileScope{MappingRevision: 10, SourceUniverseWatermark: "wm-current"}
	link := types.EvidenceNodeLink{
		ID:                "link-1",
		EventID:           "event-1",
		ResolutionRunID:   "run-1",
		SourceKnowledgeID: "knowledge-1",
		PageUUID:          "page-1",
		PageVersion:       3,
		NormalizedRef:     "knowledge-1",
		RelationState:     types.EvidenceRelationCurrent,
		MappingRevision:   10,
		UniverseWatermark: "wm-current",
	}
	event := types.CitationProfileEvent{ID: "event-1", MessageID: "message-1", CreatedAt: now}
	run := types.EvidenceResolutionRun{ID: "run-1", ResolvedAt: &now, RunMappingRevision: 10, RunUniverseWatermark: "wm-current"}
	currentRefs := map[citationProfileSourceRefKey]struct{}{
		citationProfileSourceRefKeyFromLink(link): {},
	}

	item := citationProfileEvidenceItem(scope, link, event, run, nil, currentRefs)

	require.False(t, item.StaleMapping)
	require.Equal(t, types.EvidenceRelationCurrent, item.Overlay)
}

func TestCitationProfileNodeStaleIncludesSourceRefDrift(t *testing.T) {
	scope := &types.CitationProfileScope{MappingRevision: 10, SourceUniverseWatermark: "wm-current"}
	count := citationProfileNodeCount{AuthorizedEvidenceCount: 1, CurrentLinkCount: 0, HistoricalLinkCount: 1, StaleMapping: true}

	require.True(t, citationProfileNodeStale(scope, count))
}
func TestCitationProfileNodeCountsSkipLinksWithoutLiveEvent(t *testing.T) {
	scope := &types.CitationProfileScope{MappingRevision: 10, SourceUniverseWatermark: "wm-current"}
	links := []types.EvidenceNodeLink{
		{
			ID:                "link-live",
			EventID:           "event-live",
			SourceKnowledgeID: "knowledge-live",
			PageUUID:          "page-1",
			PageVersion:       1,
			NormalizedRef:     "knowledge-live",
			RelationState:     types.EvidenceRelationCurrent,
			MappingRevision:   10,
			UniverseWatermark: "wm-current",
		},
		{
			ID:                "link-retracted",
			EventID:           "event-retracted",
			SourceKnowledgeID: "knowledge-retracted",
			PageUUID:          "page-1",
			PageVersion:       1,
			NormalizedRef:     "knowledge-retracted",
			RelationState:     types.EvidenceRelationDisputed,
			MappingRevision:   10,
			UniverseWatermark: "wm-current",
		},
	}
	currentRefs := map[citationProfileSourceRefKey]struct{}{
		citationProfileSourceRefKeyFromLink(links[0]): {},
	}
	liveEvents := map[string]types.CitationProfileEvent{
		"event-live": {ID: "event-live"},
	}

	counts := citationProfileNodeCountsFromLinks(scope, links, currentRefs, liveEvents)

	require.Equal(t, 1, counts["page-1"].AuthorizedEvidenceCount)
	require.Equal(t, 1, counts["page-1"].CurrentLinkCount)
	require.Equal(t, 0, counts["page-1"].DisputedLinkCount)
	require.Equal(t, types.EvidenceRelationCurrent, citationProfileNodeDTO(scope, types.WikiPage{ID: "page-1"}, counts["page-1"]).Overlay)
}
