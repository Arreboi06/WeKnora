package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

type citationProfileNodeCount struct {
	AuthorizedEvidenceCount int
	CurrentLinkCount        int
	HistoricalLinkCount     int
	DisputedLinkCount       int
	LatestMappingRevision   uint64
	LatestUniverseWatermark string
	StaleMapping            bool
}

func (r *citationProfileRepository) ListNodes(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	cursor *types.CitationProfileCursor,
	pageSize int,
) (*types.CitationProfileNodeListResponse, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	if tenantID == 0 || subjectID == "" || kbID == "" {
		return nil, fmt.Errorf("%w: node list requires scope", types.ErrCitationProfileInvalidRequest)
	}

	var response *types.CitationProfileNodeListResponse
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadReadableCitationScope(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		if err := citationProfileCursorMatchesScope(scope, cursor, types.CitationProfileCursorEndpointNodes, kbID, pageSize); err != nil {
			return err
		}

		snapshot := citationProfileScopeSnapshotDTO(scope, time.Now().UTC())
		lastPageUUID := ""
		if cursor != nil {
			lastPageUUID = cursor.LastPageUUID
		}
		pages, hasMore, err := r.loadCitationProfilePages(tx, scope, lastPageUUID, pageSize)
		if err != nil {
			return err
		}
		counts, err := r.loadCitationProfileNodeCounts(tx, scope, citationProfilePageIDs(pages))
		if err != nil {
			return err
		}
		items := make([]types.CitationProfileNodeDTO, 0, len(pages))
		for _, page := range pages {
			items = append(items, citationProfileNodeDTO(scope, page, counts[page.ID]))
		}

		var nextCursor *string
		if hasMore && len(pages) > 0 {
			nextCursor, err = citationProfileEncodeCursor(citationProfileCursorFromSnapshot(
				types.CitationProfileCursorEndpointNodes,
				kbID,
				pageSize,
				snapshot,
				pages[len(pages)-1].ID,
				"",
			))
			if err != nil {
				return err
			}
		}
		response = &types.CitationProfileNodeListResponse{
			Snapshot:     snapshot,
			Items:        items,
			PageSize:     pageSize,
			NextCursor:   nextCursor,
			CompleteList: !hasMore,
			Guidance:     types.CitationProfileDefaultGuidance(),
		}
		return nil
	})
	return response, err
}

func (r *citationProfileRepository) GetGraph(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileGraphResponse, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	if tenantID == 0 || subjectID == "" || kbID == "" {
		return nil, fmt.Errorf("%w: graph requires scope", types.ErrCitationProfileInvalidRequest)
	}

	var response *types.CitationProfileGraphResponse
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadReadableCitationScope(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		limits := types.CitationProfileDefaultLimits()
		snapshot := citationProfileScopeSnapshotDTO(scope, time.Now().UTC())
		pages, pageTruncated, err := r.loadCitationProfilePages(tx, scope, "", limits.GraphNodes)
		if err != nil {
			return err
		}
		counts, err := r.loadCitationProfileNodeCounts(tx, scope, citationProfilePageIDs(pages))
		if err != nil {
			return err
		}

		nodes := make([]types.CitationProfileNodeDTO, 0, len(pages))
		slugToUUID := make(map[string]string, len(pages))
		for _, page := range pages {
			nodes = append(nodes, citationProfileNodeDTO(scope, page, counts[page.ID]))
			if slug := strings.TrimSpace(page.Slug); slug != "" {
				slugToUUID[slug] = page.ID
			}
		}

		edges := make([]types.CitationProfileGraphEdgeDTO, 0)
		seenEdges := make(map[string]struct{})
		edgeTruncated := false
		for _, source := range pages {
			for _, rawTarget := range source.OutLinks {
				targetSlug := strings.TrimSpace(rawTarget)
				if targetSlug == "" {
					continue
				}
				targetUUID, ok := slugToUUID[targetSlug]
				if !ok || targetUUID == source.ID {
					continue
				}
				key := source.ID + "\x00" + targetUUID
				if _, ok := seenEdges[key]; ok {
					continue
				}
				seenEdges[key] = struct{}{}
				if len(edges) >= limits.GraphEdges {
					edgeTruncated = true
					break
				}
				targetCount := counts[targetUUID]
				edges = append(edges, types.CitationProfileGraphEdgeDTO{
					SourcePageUUID:     source.ID,
					TargetPageUUID:     targetUUID,
					EdgeType:           "wiki_link",
					EvidenceEventCount: targetCount.AuthorizedEvidenceCount,
					StaleMapping:       citationProfileNodeStale(scope, targetCount),
				})
			}
			if edgeTruncated {
				break
			}
		}

		response = &types.CitationProfileGraphResponse{
			Snapshot: snapshot,
			Nodes:    nodes,
			Edges:    edges,
			Caps: types.CitationProfileGraphCaps{
				MaxNodes: limits.GraphNodes,
				MaxEdges: limits.GraphEdges,
			},
			GraphTruncated:  pageTruncated || edgeTruncated,
			CompleteListURL: fmt.Sprintf("/api/v1/knowledgebase/%s/citation-profile/nodes", kbID),
			Guidance:        types.CitationProfileDefaultGuidance(),
		}
		return nil
	})
	return response, err
}

func (r *citationProfileRepository) ListNodeEvidence(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	pageUUID string,
	cursor *types.CitationProfileCursor,
	pageSize int,
) (*types.CitationProfileNodeEvidenceResponse, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	pageUUID = strings.TrimSpace(pageUUID)
	if tenantID == 0 || subjectID == "" || kbID == "" || pageUUID == "" {
		return nil, fmt.Errorf("%w: evidence list requires scope and page", types.ErrCitationProfileInvalidRequest)
	}

	var response *types.CitationProfileNodeEvidenceResponse
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadReadableCitationScope(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		if err := citationProfileCursorMatchesScope(scope, cursor, types.CitationProfileCursorEndpointEvidence, kbID, pageSize); err != nil {
			return err
		}
		if cursor != nil && strings.TrimSpace(cursor.LastPageUUID) != "" && strings.TrimSpace(cursor.LastPageUUID) != pageUUID {
			return types.ErrCitationProfileChanged
		}
		page, err := r.loadCitationProfilePage(tx, tenantID, kbID, pageUUID)
		if err != nil {
			return err
		}
		lastRelationID := ""
		if cursor != nil {
			lastRelationID = cursor.LastRelationID
		}
		links, hasMore, err := r.loadCitationProfileEvidenceLinks(tx, scope, pageUUID, lastRelationID, pageSize)
		if err != nil {
			return err
		}
		currentSourceRefs, err := r.loadCurrentWikiSourceRefKeys(tx, scope, []string{pageUUID})
		if err != nil {
			return err
		}
		events, err := r.loadCitationProfileEventsByID(tx, scope, citationProfileLinkEventIDs(links))
		if err != nil {
			return err
		}
		runs, err := r.loadCitationProfileRunsByID(tx, scope, citationProfileLinkRunIDs(links))
		if err != nil {
			return err
		}
		corrections, err := r.loadCitationProfileCorrectionsForPage(tx, scope, pageUUID, citationProfileLinkEventIDs(links))
		if err != nil {
			return err
		}

		snapshot := citationProfileScopeSnapshotDTO(scope, time.Now().UTC())
		items := make([]types.CitationProfileEvidenceItemDTO, 0, len(links))
		for _, link := range links {
			event, ok := events[link.EventID]
			if !ok {
				continue
			}
			run := runs[link.ResolutionRunID]
			key := citationProfileCorrectionKey(link.EventID, link.PageUUID)
			items = append(items, citationProfileEvidenceItem(scope, link, event, run, corrections[key], currentSourceRefs))
		}
		var nextCursor *string
		if hasMore && len(links) > 0 {
			nextCursor, err = citationProfileEncodeCursor(citationProfileCursorFromSnapshot(
				types.CitationProfileCursorEndpointEvidence,
				kbID,
				pageSize,
				snapshot,
				pageUUID,
				links[len(links)-1].ID,
			))
			if err != nil {
				return err
			}
		}
		response = &types.CitationProfileNodeEvidenceResponse{
			Snapshot: snapshot,
			Page: types.CitationProfileEvidencePageDTO{
				PageUUID:    page.ID,
				PageVersion: strconv.Itoa(page.Version),
				Title:       page.Title,
				Slug:        page.Slug,
			},
			Items:      items,
			NextCursor: nextCursor,
			Guidance:   types.CitationProfileDefaultGuidance(),
		}
		return nil
	})
	return response, err
}

func (r *citationProfileRepository) loadReadableCitationScope(tx *gorm.DB, tenantID uint64, subjectID string, kbID string) (*types.CitationProfileScope, error) {
	var scope types.CitationProfileScope
	err := tx.Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", tenantID, subjectID, kbID).
		Order("updated_at DESC").
		First(&scope).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
		}
		return nil, err
	}
	if scope.FencedAt != nil {
		return nil, types.ErrCitationProfileDeleted
	}
	if scope.DeletedAt != nil {
		return nil, types.ErrCitationProfileDeleted
	}
	if !scope.Enabled {
		return nil, types.ErrCitationProfileNotFound
	}
	if !citationProfileScopeACLCurrent(&scope) {
		return nil, types.ErrCitationProfileUnavailable
	}
	return &scope, nil
}

func (r *citationProfileRepository) loadCitationProfilePages(tx *gorm.DB, scope *types.CitationProfileScope, afterPageUUID string, limit int) ([]types.WikiPage, bool, error) {
	if limit <= 0 {
		limit = types.CitationProfileDefaultLimits().ListPageSize
	}
	query := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND status <> ?", scope.TenantID, scope.KnowledgeBaseID, types.WikiPageStatusArchived)
	if afterPageUUID = strings.TrimSpace(afterPageUUID); afterPageUUID != "" {
		query = query.Where("id > ?", afterPageUUID)
	}
	var pages []types.WikiPage
	if err := query.Order("id ASC").Limit(limit + 1).Find(&pages).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(pages) > limit
	if hasMore {
		pages = pages[:limit]
	}
	return pages, hasMore, nil
}

func (r *citationProfileRepository) loadCitationProfilePage(tx *gorm.DB, tenantID uint64, kbID string, pageUUID string) (*types.WikiPage, error) {
	var page types.WikiPage
	err := tx.Where("tenant_id = ? AND knowledge_base_id = ? AND id = ? AND status <> ?", tenantID, kbID, pageUUID, types.WikiPageStatusArchived).
		First(&page).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
		}
		return nil, err
	}
	return &page, nil
}

func (r *citationProfileRepository) loadCitationProfileNodeCounts(tx *gorm.DB, scope *types.CitationProfileScope, pageUUIDs []string) (map[string]citationProfileNodeCount, error) {
	counts := make(map[string]citationProfileNodeCount, len(pageUUIDs))
	if len(pageUUIDs) == 0 {
		return counts, nil
	}
	currentSourceRefs, err := r.loadCurrentWikiSourceRefKeys(tx, scope, pageUUIDs)
	if err != nil {
		return nil, err
	}
	var links []types.EvidenceNodeLink
	err = tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND page_uuid IN ?",
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		scope.SubjectEpoch,
		scope.ID,
		pageUUIDs,
	).Find(&links).Error
	if err != nil {
		return nil, err
	}
	events, err := r.loadCitationProfileEventsByID(tx, scope, citationProfileLinkEventIDs(links))
	if err != nil {
		return nil, err
	}
	return citationProfileNodeCountsFromLinks(scope, links, currentSourceRefs, events), nil
}

func citationProfileNodeCountsFromLinks(
	scope *types.CitationProfileScope,
	links []types.EvidenceNodeLink,
	currentSourceRefs map[citationProfileSourceRefKey]struct{},
	liveEvents map[string]types.CitationProfileEvent,
) map[string]citationProfileNodeCount {
	counts := make(map[string]citationProfileNodeCount, len(links))
	for _, link := range links {
		if _, ok := liveEvents[link.EventID]; !ok {
			continue
		}
		count := counts[link.PageUUID]
		count.AuthorizedEvidenceCount++
		relationState := link.RelationState
		if relationState == types.EvidenceRelationCurrent {
			if _, ok := currentSourceRefs[citationProfileSourceRefKeyFromLink(link)]; !ok {
				relationState = types.EvidenceRelationHistorical
				count.StaleMapping = true
			}
		}
		switch relationState {
		case types.EvidenceRelationCurrent:
			count.CurrentLinkCount++
		case types.EvidenceRelationHistorical:
			count.HistoricalLinkCount++
		case types.EvidenceRelationDisputed:
			count.DisputedLinkCount++
		}
		if link.MappingRevision > count.LatestMappingRevision {
			count.LatestMappingRevision = link.MappingRevision
			count.LatestUniverseWatermark = link.UniverseWatermark
		}
		counts[link.PageUUID] = count
	}
	return counts
}

type citationProfileSourceRefKey struct {
	SourceKnowledgeID string
	PageUUID          string
	PageVersion       int
	NormalizedRef     string
}

func (r *citationProfileRepository) loadCurrentWikiSourceRefKeys(tx *gorm.DB, scope *types.CitationProfileScope, pageUUIDs []string) (map[citationProfileSourceRefKey]struct{}, error) {
	keys := make(map[citationProfileSourceRefKey]struct{})
	if scope == nil || len(pageUUIDs) == 0 {
		return keys, nil
	}
	pageUUIDs = citationUniqueStrings(pageUUIDs)
	if len(pageUUIDs) == 0 {
		return keys, nil
	}
	var refs []types.WikiSourceRefIndex
	err := tx.Where(
		"tenant_id = ? AND knowledge_base_id = ? AND page_uuid IN ? AND lifecycle_state = ?",
		scope.TenantID,
		scope.KnowledgeBaseID,
		pageUUIDs,
		"current",
	).Find(&refs).Error
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		keys[citationProfileSourceRefKeyFromIndex(ref)] = struct{}{}
	}
	return keys, nil
}

func citationProfileSourceRefKeyFromLink(link types.EvidenceNodeLink) citationProfileSourceRefKey {
	normalizedRef := strings.TrimSpace(link.NormalizedRef)
	if normalizedRef == "" {
		normalizedRef = strings.TrimSpace(link.SourceKnowledgeID)
	}
	return citationProfileSourceRefKey{
		SourceKnowledgeID: strings.TrimSpace(link.SourceKnowledgeID),
		PageUUID:          strings.TrimSpace(link.PageUUID),
		PageVersion:       link.PageVersion,
		NormalizedRef:     normalizedRef,
	}
}

func citationProfileSourceRefKeyFromIndex(ref types.WikiSourceRefIndex) citationProfileSourceRefKey {
	normalizedRef := strings.TrimSpace(ref.NormalizedRef)
	if normalizedRef == "" {
		normalizedRef = strings.TrimSpace(ref.SourceKnowledgeID)
	}
	return citationProfileSourceRefKey{
		SourceKnowledgeID: strings.TrimSpace(ref.SourceKnowledgeID),
		PageUUID:          strings.TrimSpace(ref.PageUUID),
		PageVersion:       ref.PageVersion,
		NormalizedRef:     normalizedRef,
	}
}

func citationUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	unique := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func (r *citationProfileRepository) loadCitationProfileEvidenceLinks(tx *gorm.DB, scope *types.CitationProfileScope, pageUUID string, afterRelationID string, limit int) ([]types.EvidenceNodeLink, bool, error) {
	query := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND page_uuid = ?",
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		scope.SubjectEpoch,
		scope.ID,
		pageUUID,
	)
	if afterRelationID = strings.TrimSpace(afterRelationID); afterRelationID != "" {
		query = query.Where("id > ?", afterRelationID)
	}
	var links []types.EvidenceNodeLink
	if err := query.Order("id ASC").Limit(limit + 1).Find(&links).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(links) > limit
	if hasMore {
		links = links[:limit]
	}
	return links, hasMore, nil
}

func (r *citationProfileRepository) loadCitationProfileEventsByID(tx *gorm.DB, scope *types.CitationProfileScope, eventIDs []string) (map[string]types.CitationProfileEvent, error) {
	events := make(map[string]types.CitationProfileEvent, len(eventIDs))
	if len(eventIDs) == 0 {
		return events, nil
	}
	var rows []types.CitationProfileEvent
	err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND id IN ? AND retracted_at IS NULL",
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		scope.SubjectEpoch,
		scope.ID,
		eventIDs,
	).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		events[row.ID] = row
	}
	return events, nil
}

func (r *citationProfileRepository) loadCitationProfileRunsByID(tx *gorm.DB, scope *types.CitationProfileScope, runIDs []string) (map[string]types.EvidenceResolutionRun, error) {
	runs := make(map[string]types.EvidenceResolutionRun, len(runIDs))
	if len(runIDs) == 0 {
		return runs, nil
	}
	var rows []types.EvidenceResolutionRun
	err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND id IN ?",
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		scope.SubjectEpoch,
		scope.ID,
		runIDs,
	).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		runs[row.ID] = row
	}
	return runs, nil
}

func (r *citationProfileRepository) loadCitationProfileCorrectionsForPage(tx *gorm.DB, scope *types.CitationProfileScope, pageUUID string, eventIDs []string) (map[string][]types.CitationProfileCorrection, error) {
	items := make(map[string][]types.CitationProfileCorrection)
	if len(eventIDs) == 0 {
		return items, nil
	}
	var rows []types.CitationProfileCorrection
	err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND page_uuid = ? AND event_id IN ?",
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		scope.SubjectEpoch,
		scope.ID,
		pageUUID,
		eventIDs,
	).Order("created_at ASC, id ASC").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		key := citationProfileCorrectionKey(row.EventID, row.PageUUID)
		items[key] = append(items[key], row)
	}
	return items, nil
}

func citationProfilePageIDs(pages []types.WikiPage) []string {
	ids := make([]string, 0, len(pages))
	seen := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		if page.ID == "" {
			continue
		}
		if _, ok := seen[page.ID]; ok {
			continue
		}
		seen[page.ID] = struct{}{}
		ids = append(ids, page.ID)
	}
	return ids
}

func citationProfileLinkEventIDs(links []types.EvidenceNodeLink) []string {
	ids := make([]string, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, link := range links {
		if link.EventID == "" {
			continue
		}
		if _, ok := seen[link.EventID]; ok {
			continue
		}
		seen[link.EventID] = struct{}{}
		ids = append(ids, link.EventID)
	}
	return ids
}

func citationProfileLinkRunIDs(links []types.EvidenceNodeLink) []string {
	ids := make([]string, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, link := range links {
		if link.ResolutionRunID == "" {
			continue
		}
		if _, ok := seen[link.ResolutionRunID]; ok {
			continue
		}
		seen[link.ResolutionRunID] = struct{}{}
		ids = append(ids, link.ResolutionRunID)
	}
	return ids
}

func citationProfileNodeDTO(scope *types.CitationProfileScope, page types.WikiPage, count citationProfileNodeCount) types.CitationProfileNodeDTO {
	overlay := types.EvidenceRelationUnknown
	if count.DisputedLinkCount > 0 {
		overlay = types.EvidenceRelationDisputed
	} else if count.CurrentLinkCount > 0 {
		overlay = types.EvidenceRelationCurrent
	} else if count.HistoricalLinkCount > 0 {
		overlay = types.EvidenceRelationHistorical
	}
	return types.CitationProfileNodeDTO{
		PageUUID:                page.ID,
		PageVersion:             strconv.Itoa(page.Version),
		Title:                   page.Title,
		Slug:                    page.Slug,
		PageType:                page.PageType,
		Overlay:                 overlay,
		AuthorizedEvidenceCount: count.AuthorizedEvidenceCount,
		CurrentLinkCount:        count.CurrentLinkCount,
		HistoricalLinkCount:     count.HistoricalLinkCount,
		DisputedLinkCount:       count.DisputedLinkCount,
		StaleMapping:            citationProfileNodeStale(scope, count),
		EvidenceHref:            fmt.Sprintf("/api/v1/knowledgebase/%s/citation-profile/nodes/%s/evidence", scope.KnowledgeBaseID, page.ID),
		UpdatedAt:               citationTime(page.UpdatedAt),
	}
}

func citationProfileNodeStale(scope *types.CitationProfileScope, count citationProfileNodeCount) bool {
	if scope == nil || count.AuthorizedEvidenceCount == 0 {
		return false
	}
	if count.StaleMapping {
		return true
	}
	if count.LatestMappingRevision > 0 && count.LatestMappingRevision < scope.MappingRevision {
		return true
	}
	currentWatermark := strings.TrimSpace(scope.SourceUniverseWatermark)
	return currentWatermark != "" && count.LatestUniverseWatermark != "" && count.LatestUniverseWatermark != currentWatermark
}

func citationProfileEvidenceItem(
	scope *types.CitationProfileScope,
	link types.EvidenceNodeLink,
	event types.CitationProfileEvent,
	run types.EvidenceResolutionRun,
	corrections []types.CitationProfileCorrection,
	currentSourceRefs map[citationProfileSourceRefKey]struct{},
) types.CitationProfileEvidenceItemDTO {
	resolvedAt := ""
	if run.ResolvedAt != nil {
		resolvedAt = citationTime(*run.ResolvedAt)
	} else if event.ResolvedAt != nil {
		resolvedAt = citationTime(*event.ResolvedAt)
	}
	occurredAt := event.CreatedAt
	if event.MessageCompletedAt != nil && !event.MessageCompletedAt.IsZero() {
		occurredAt = *event.MessageCompletedAt
	}
	runMappingRevision := run.RunMappingRevision
	if runMappingRevision == 0 {
		runMappingRevision = link.MappingRevision
	}
	runWatermark := strings.TrimSpace(run.RunUniverseWatermark)
	if runWatermark == "" {
		runWatermark = link.UniverseWatermark
	}
	staleMapping := citationProfileLinkStale(scope, link, currentSourceRefs)
	overlay := link.RelationState
	if overlay == types.EvidenceRelationCurrent && staleMapping {
		overlay = types.EvidenceRelationHistorical
	}
	return types.CitationProfileEvidenceItemDTO{
		EventID:                      event.ID,
		RunID:                        link.ResolutionRunID,
		RelationID:                   link.ID,
		Overlay:                      overlay,
		ClaimCode:                    types.CitationProfileEvidenceClaimCode,
		MessageID:                    event.MessageID,
		OriginReferenceIndex:         event.OriginReferenceIndex,
		SourceKnowledgeID:            event.SourceKnowledgeID,
		SourceResultID:               event.SourceResultID,
		SourceChunkIndex:             event.SourceChunkIndex,
		KnowledgeRecordVersion:       citationProfileKnowledgeRecordVersion(event),
		AuthoritativeKnowledgeBaseID: citationProfileAuthoritativeKB(event),
		PageUUID:                     link.PageUUID,
		PageVersionAtResolution:      strconv.Itoa(link.PageVersion),
		OccurredAt:                   citationTime(occurredAt),
		ResolvedAt:                   resolvedAt,
		RunMappingRevision:           strconv.FormatUint(runMappingRevision, 10),
		RunUniverseWatermark:         runWatermark,
		StaleMapping:                 staleMapping,
		CorrectionState:              citationProfileCorrectionState(event, corrections),
		CorrectionHistory:            citationProfileCorrectionHistory(corrections),
	}
}

func citationProfileKnowledgeRecordVersion(event types.CitationProfileEvent) string {
	var snapshot map[string]interface{}
	if len(event.KnowledgeSnapshot) > 0 && json.Unmarshal(event.KnowledgeSnapshot, &snapshot) == nil {
		if updatedAt, ok := snapshot["updated_at"].(string); ok && strings.TrimSpace(updatedAt) != "" {
			return strings.TrimSpace(updatedAt)
		}
	}
	return event.MessageVersion
}

func citationProfileAuthoritativeKB(event types.CitationProfileEvent) string {
	var proof map[string]interface{}
	if len(event.KnowledgeBaseProof) > 0 && json.Unmarshal(event.KnowledgeBaseProof, &proof) == nil {
		if kbID, ok := proof["knowledge_base_id"].(string); ok && strings.TrimSpace(kbID) != "" {
			return strings.TrimSpace(kbID)
		}
	}
	return event.KnowledgeBaseID
}

func citationProfileLinkStale(scope *types.CitationProfileScope, link types.EvidenceNodeLink, currentSourceRefs map[citationProfileSourceRefKey]struct{}) bool {
	if scope == nil {
		return false
	}
	if link.RelationState == types.EvidenceRelationCurrent {
		if _, ok := currentSourceRefs[citationProfileSourceRefKeyFromLink(link)]; !ok {
			return true
		}
	}
	if link.MappingRevision > 0 && link.MappingRevision < scope.MappingRevision {
		return true
	}
	currentWatermark := strings.TrimSpace(scope.SourceUniverseWatermark)
	return currentWatermark != "" && link.UniverseWatermark != "" && link.UniverseWatermark != currentWatermark
}

func citationProfileCorrectionState(event types.CitationProfileEvent, corrections []types.CitationProfileCorrection) string {
	state := types.CitationProfileCorrectionStateNone
	for _, correction := range corrections {
		switch correction.CorrectionType {
		case types.CitationCorrectionConfirmRelevant:
			state = types.CitationProfileCorrectionStateConfirmed
		case types.CitationCorrectionRejectMapping:
			state = types.CitationProfileCorrectionStateRejected
		case types.CitationCorrectionRetractEvent:
			state = types.CitationProfileCorrectionStateEventRetracted
		}
	}
	if event.RetractedAt != nil {
		state = types.CitationProfileCorrectionStateEventRetracted
	}
	return state
}

func citationProfileCorrectionHistory(corrections []types.CitationProfileCorrection) []types.CitationProfileCorrectionHistoryItem {
	history := make([]types.CitationProfileCorrectionHistoryItem, 0, len(corrections))
	for _, correction := range corrections {
		history = append(history, types.CitationProfileCorrectionHistoryItem{
			CorrectionID: correction.ID,
			Action:       correction.CorrectionType,
			ReasonCode:   correction.Reason,
			CreatedAt:    citationTime(correction.CreatedAt),
		})
	}
	return history
}

func citationProfileCorrectionKey(eventID string, pageUUID string) string {
	return eventID + "\x00" + pageUUID
}

func citationProfileCursorMatchesScope(scope *types.CitationProfileScope, cursor *types.CitationProfileCursor, endpoint string, kbID string, pageSize int) error {
	if cursor == nil {
		return nil
	}
	if scope == nil {
		return types.ErrCitationProfileChanged
	}
	snapshot := citationProfileScopeSnapshotDTO(scope, time.Now().UTC())
	if cursor.SchemaVersion != types.CitationProfileCursorSchemaV1 ||
		cursor.Endpoint != endpoint ||
		cursor.KnowledgeBaseID != kbID ||
		cursor.SubjectEpoch != scope.SubjectEpoch ||
		cursor.ReadVersion != snapshot.ReadVersion ||
		cursor.EventCutoff != snapshot.EventCutoff ||
		cursor.CorrectionCutoff != snapshot.CorrectionCutoff ||
		cursor.ActiveRunPointerCutoff != snapshot.ActiveRunPointerCutoff ||
		cursor.CurrentIndexWatermark != snapshot.CurrentIndexWatermark ||
		cursor.CurrentWikiUniverseWatermark != snapshot.CurrentWikiUniverseWatermark ||
		cursor.PageSize != pageSize {
		return types.ErrCitationProfileChanged
	}
	return nil
}

func citationProfileCursorFromSnapshot(endpoint string, kbID string, pageSize int, snapshot *types.CitationProfileSnapshot, lastPageUUID string, lastRelationID string) *types.CitationProfileCursor {
	cursor := &types.CitationProfileCursor{
		SchemaVersion:   types.CitationProfileCursorSchemaV1,
		Endpoint:        endpoint,
		KnowledgeBaseID: kbID,
		PageSize:        pageSize,
		Sort:            "id_asc",
		LastPageUUID:    lastPageUUID,
		LastRelationID:  lastRelationID,
	}
	if snapshot != nil {
		cursor.SubjectEpoch = snapshot.SubjectEpoch
		cursor.ReadVersion = snapshot.ReadVersion
		cursor.EventCutoff = snapshot.EventCutoff
		cursor.CorrectionCutoff = snapshot.CorrectionCutoff
		cursor.ActiveRunPointerCutoff = snapshot.ActiveRunPointerCutoff
		cursor.CurrentIndexWatermark = snapshot.CurrentIndexWatermark
		cursor.CurrentWikiUniverseWatermark = snapshot.CurrentWikiUniverseWatermark
	}
	return cursor
}

func citationProfileEncodeCursor(cursor *types.CitationProfileCursor) (*string, error) {
	if cursor == nil {
		return nil, nil
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return &encoded, nil
}

func citationProfileScopeSnapshotDTO(scope *types.CitationProfileScope, capturedAt time.Time) *types.CitationProfileSnapshot {
	if scope == nil {
		return &types.CitationProfileSnapshot{ReadVersion: "0", CapturedAt: citationTime(capturedAt)}
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
		CapturedAt:                   citationTime(capturedAt),
	}
}
