package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type citationProfileRepository struct {
	db *gorm.DB
}

func NewCitationProfileRepository(db *gorm.DB) interfaces.CitationProfileRepository {
	return &citationProfileRepository{db: db}
}

func (r *citationProfileRepository) GetScopeStatus(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileScope, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("citation profile repository requires database")
	}

	var scope types.CitationProfileScope
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", tenantID, subjectID, kbID).
		Order("CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END").
		Order("updated_at DESC").
		First(&scope).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &scope, nil
}

func (r *citationProfileRepository) SetEnrollment(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	enabled bool,
	expectedReadVersion *uint64,
	idempotencyKey string,
) (*types.CitationProfileScope, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if tenantID == 0 || subjectID == "" || kbID == "" || idempotencyKey == "" {
		return nil, fmt.Errorf("%w: enrollment requires scope and idempotency", types.ErrCitationProfileInvalidRequest)
	}

	expectedText := ""
	if expectedReadVersion != nil {
		expectedText = fmt.Sprintf("%d", *expectedReadVersion)
	}
	requestDigest := citationRequestDigest(types.CitationOperationEnrollment, kbID, fmt.Sprintf("%t", enabled), expectedText)

	var resolved *types.CitationProfileScope
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadLiveCitationScopeForUpdate(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		now := time.Now().UTC()

		if scope != nil {
			if scope.FencedAt != nil {
				return types.ErrCitationProfileDeleted
			}
			if existing, found, err := r.findCitationProfileOperationByIdem(
				tx, tenantID, subjectID, kbID, scope.SubjectEpoch, types.CitationOperationEnrollment, idempotencyKey, requestDigest,
			); err != nil {
				return err
			} else if found {
				replayed, err := r.loadCitationScopeByID(tx, tenantID, subjectID, kbID, existing.ScopeID)
				if err != nil {
					if errors.Is(err, types.ErrCitationProfileNotFound) {
						resolved = nil
						return nil
					}
					return err
				}
				resolved = replayed
				return nil
			}
			if expectedReadVersion != nil && scope.ProfileReadVersion != *expectedReadVersion {
				return types.ErrCitationProfileChanged
			}
			if !enabled {
				resultSummary, err := citationProfileOperationResult(scope, map[string]interface{}{
					"enabled":      false,
					"receipt_code": types.CitationProfileReceiptHiddenPurgeScheduled,
					"hidden_at":    citationTime(now),
				}, now)
				if err != nil {
					return err
				}
				operation, err := createCitationProfileOperation(tx, scope, types.CitationOperationEnrollment, idempotencyKey, requestDigest, map[string]interface{}{
					"enabled":               false,
					"expected_read_version": expectedText,
				}, types.CitationProfileOperationStatusAccepted, resultSummary, "", nil, now)
				if err != nil {
					return err
				}
				if err := r.fenceCitationProfileScopeForDelete(tx, scope, operation.ID, types.CitationOperationEnrollment, expectedReadVersion, now); err != nil {
					return err
				}
				resolved = scope
				return nil
			}
			if scope.Enabled {
				resultSummary, err := citationProfileOperationResult(scope, map[string]interface{}{"enabled": true}, now)
				if err != nil {
					return err
				}
				_, err = createCitationProfileOperation(tx, scope, types.CitationOperationEnrollment, idempotencyKey, requestDigest, map[string]interface{}{
					"enabled":               true,
					"expected_read_version": expectedText,
				}, types.CitationProfileOperationStatusAccepted, resultSummary, "", nil, now)
				if err != nil {
					return err
				}
				resolved = scope
				return nil
			}

			if err := r.fenceCitationProfileScopeForDelete(tx, scope, "", types.CitationOperationEnrollment, expectedReadVersion, now); err != nil {
				return err
			}
		}

		if expectedReadVersion != nil && *expectedReadVersion != 0 {
			return types.ErrCitationProfileChanged
		}
		if !enabled {
			resolved = nil
			return nil
		}

		var activeScopes int64
		if err := tx.Model(&types.CitationProfileScope{}).
			Where("tenant_id = ? AND subject_id = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", tenantID, subjectID, true).
			Count(&activeScopes).Error; err != nil {
			return err
		}
		if activeScopes >= int64(types.CitationProfileDefaultLimits().ActiveScopes) {
			return types.ErrCitationProfileQuotaExceeded
		}
		scope = &types.CitationProfileScope{
			ID:                     uuid.NewString(),
			TenantID:               tenantID,
			SubjectID:              subjectID,
			KnowledgeBaseID:        kbID,
			SubjectEpoch:           uuid.NewString(),
			ProfileReadVersion:     1,
			ProfilePolicyVersion:   types.CitationProfilePolicyVersion,
			RetentionPolicyVersion: types.CitationProfileRetentionPolicyVersion,
			Enabled:                true,
			ACLCheckState:          types.CitationProfileACLStateCurrent,
			CreatedAt:              now,
			UpdatedAt:              now,
		}
		if err := tx.Create(scope).Error; err != nil {
			return err
		}

		resultSummary, err := citationProfileOperationResult(scope, map[string]interface{}{"enabled": true}, now)
		if err != nil {
			return err
		}
		_, err = createCitationProfileOperation(tx, scope, types.CitationOperationEnrollment, idempotencyKey, requestDigest, map[string]interface{}{
			"enabled":               true,
			"expected_read_version": expectedText,
		}, types.CitationProfileOperationStatusAccepted, resultSummary, "", nil, now)
		if err != nil {
			return err
		}
		resolved = scope
		return nil
	})
	return resolved, err
}

func (r *citationProfileRepository) CreateExportOperation(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	expectedReadVersion uint64,
	idempotencyKey string,
	format string,
) (*types.CitationProfileOperation, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	format = strings.TrimSpace(format)
	if tenantID == 0 || subjectID == "" || kbID == "" || idempotencyKey == "" || format == "" {
		return nil, fmt.Errorf("%w: export requires scope, format and idempotency", types.ErrCitationProfileInvalidRequest)
	}
	requestDigest := citationRequestDigest(types.CitationOperationExport, kbID, fmt.Sprintf("%d", expectedReadVersion), format)

	var operation *types.CitationProfileOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadLiveCitationScopeForUpdate(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		if scope == nil {
			deleted, err := r.loadDeletedCitationScope(tx, tenantID, subjectID, kbID)
			if err != nil {
				return err
			}
			if deleted != nil {
				return types.ErrCitationProfileDeleted
			}
			return types.ErrCitationProfileNotFound
		}
		if !scope.Enabled {
			return types.ErrCitationProfileNotFound
		}
		if scope.FencedAt != nil {
			return types.ErrCitationProfileDeleted
		}
		if !citationProfileScopeACLCurrent(scope) {
			return types.ErrCitationProfileUnavailable
		}
		if existing, found, err := r.findCitationProfileOperationByIdem(
			tx, tenantID, subjectID, kbID, scope.SubjectEpoch, types.CitationOperationExport, idempotencyKey, requestDigest,
		); err != nil {
			return err
		} else if found {
			operation = existing
			return nil
		}
		if scope.ProfileReadVersion != expectedReadVersion {
			return types.ErrCitationProfileChanged
		}

		var pending int64
		if err := tx.Model(&types.CitationProfileOperation{}).
			Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND completed_at IS NULL",
				tenantID, subjectID, kbID, scope.SubjectEpoch).
			Count(&pending).Error; err != nil {
			return err
		}
		if pending >= int64(types.CitationProfileDefaultLimits().PendingOperations) {
			return types.ErrCitationProfileQuotaExceeded
		}

		now := time.Now().UTC()
		events, eventsTruncated, err := r.loadCitationProfileExportEvents(tx, tenantID, subjectID, kbID, scope.SubjectEpoch)
		if err != nil {
			return err
		}
		links, linksTruncated, err := r.loadCitationProfileExportLinks(tx, tenantID, subjectID, kbID, scope.SubjectEpoch, events)
		if err != nil {
			return err
		}
		payload, err := citationProfileExportPayload(scope, events, links, eventsTruncated, linksTruncated, now)
		if err != nil {
			return err
		}
		expiresAt := now.Add(time.Hour)
		operation, err = createCitationProfileOperation(tx, scope, types.CitationOperationExport, idempotencyKey, requestDigest, map[string]interface{}{
			"expected_read_version": fmt.Sprintf("%d", expectedReadVersion),
			"format":                format,
		}, types.CitationProfileOperationStatusReady, payload, "db:result_summary", &expiresAt, now)
		return err
	})
	return operation, err
}

func (r *citationProfileRepository) GetExportOperation(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	operationID string,
) (*types.CitationProfileOperation, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	operationID = strings.TrimSpace(operationID)
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	if tenantID == 0 || subjectID == "" || kbID == "" || operationID == "" {
		return nil, fmt.Errorf("%w: export operation requires scope and id", types.ErrCitationProfileInvalidRequest)
	}

	var operation *types.CitationProfileOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadLiveCitationScopeForUpdate(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		if scope == nil {
			deleted, err := r.loadDeletedCitationScope(tx, tenantID, subjectID, kbID)
			if err != nil {
				return err
			}
			if deleted != nil {
				return types.ErrCitationProfileDeleted
			}
			return types.ErrCitationProfileNotFound
		}
		if !scope.Enabled {
			return types.ErrCitationProfileNotFound
		}
		if scope.FencedAt != nil {
			return types.ErrCitationProfileDeleted
		}
		if !citationProfileScopeACLCurrent(scope) {
			return types.ErrCitationProfileUnavailable
		}
		operation, err = r.loadCitationProfileOperationByID(tx, tenantID, subjectID, kbID, scope.SubjectEpoch, types.CitationOperationExport, operationID)
		if err != nil {
			return err
		}
		if operation.Status == types.CitationProfileOperationStatusRevoked {
			return types.ErrCitationProfileNotFound
		}
		return nil
	})
	return operation, err
}

func (r *citationProfileRepository) RequestCurrentACLDelete(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	expectedReadVersion *uint64,
	idempotencyKey string,
) (*types.CitationProfileOperation, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if tenantID == 0 || subjectID == "" || kbID == "" || idempotencyKey == "" {
		return nil, fmt.Errorf("%w: delete requires scope and idempotency", types.ErrCitationProfileInvalidRequest)
	}
	expectedText := ""
	if expectedReadVersion != nil {
		expectedText = fmt.Sprintf("%d", *expectedReadVersion)
	}
	requestDigest := citationRequestDigest(types.CitationOperationDeleteCurrentACL, kbID, expectedText)

	var operation *types.CitationProfileOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadLiveCitationScopeForUpdate(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		if scope == nil || scope.FencedAt != nil {
			operation = citationSyntheticOperation(kbID, types.CitationOperationDeleteCurrentACL)
			return nil
		}
		if existing, found, err := r.findCitationProfileOperationByIdem(
			tx, tenantID, subjectID, kbID, scope.SubjectEpoch, types.CitationOperationDeleteCurrentACL, idempotencyKey, requestDigest,
		); err != nil {
			return err
		} else if found {
			operation = existing
			return nil
		}
		if expectedReadVersion != nil && scope.ProfileReadVersion != *expectedReadVersion {
			return types.ErrCitationProfileChanged
		}

		now := time.Now().UTC()
		resultSummary, err := citationProfileOperationResult(scope, map[string]interface{}{
			"receipt_code": types.CitationProfileReceiptHiddenPurgeScheduled,
			"hidden_at":    citationTime(now),
		}, now)
		if err != nil {
			return err
		}
		operation, err = createCitationProfileOperation(tx, scope, types.CitationOperationDeleteCurrentACL, idempotencyKey, requestDigest, map[string]interface{}{
			"expected_read_version": expectedText,
		}, types.CitationProfileOperationStatusAccepted, resultSummary, "", nil, now)
		if err != nil {
			return err
		}
		return r.fenceCitationProfileScopeForDelete(tx, scope, operation.ID, types.CitationOperationDeleteCurrentACL, expectedReadVersion, now)
	})
	return operation, err
}

func (r *citationProfileRepository) RequestBlindDelete(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileOperation, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	if tenantID == 0 || subjectID == "" || kbID == "" {
		return nil, fmt.Errorf("%w: blind delete requires authenticated scope", types.ErrCitationProfileInvalidRequest)
	}

	var operation *types.CitationProfileOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadLiveCitationScopeForUpdate(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		if scope == nil || scope.FencedAt != nil {
			operation = citationSyntheticOperation(kbID, types.CitationOperationDeleteBlind)
			return nil
		}

		now := time.Now().UTC()
		operationID := uuid.NewString()
		resultSummary, err := citationProfileOperationResult(scope, map[string]interface{}{
			"receipt_code": types.CitationProfileReceiptAccepted,
			"hidden_at":    citationTime(now),
		}, now)
		if err != nil {
			return err
		}
		requestDigest := citationRequestDigest(types.CitationOperationDeleteBlind, kbID, operationID)
		operation, err = createCitationProfileOperation(tx, scope, types.CitationOperationDeleteBlind, operationID, requestDigest, map[string]interface{}{}, types.CitationProfileOperationStatusAccepted, resultSummary, "", nil, now)
		if err != nil {
			return err
		}
		if err := r.fenceCitationProfileScopeForDelete(tx, scope, operation.ID, types.CitationOperationDeleteBlind, nil, now); err != nil {
			if errors.Is(err, types.ErrCitationProfileChanged) {
				operation = citationSyntheticOperation(kbID, types.CitationOperationDeleteBlind)
				return nil
			}
			return err
		}
		return nil
	})
	return operation, err
}

func (r *citationProfileRepository) fenceCitationProfileScopeForDelete(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
	operationID string,
	fenceReason string,
	expectedReadVersion *uint64,
	now time.Time,
) error {
	if scope == nil {
		return nil
	}
	nextVersion := scope.ProfileReadVersion + 1
	query := tx.Model(&types.CitationProfileScope{}).
		Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND deleted_at IS NULL AND fenced_at IS NULL",
			scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch)
	if expectedReadVersion != nil {
		query = query.Where("profile_read_version = ?", *expectedReadVersion)
	}
	result := query.Updates(map[string]interface{}{
		"enabled":              false,
		"fenced_at":            now,
		"fence_reason":         fenceReason,
		"delete_request_id":    strings.TrimSpace(operationID),
		"deleted_at":           now,
		"profile_read_version": nextVersion,
		"updated_at":           now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return types.ErrCitationProfileChanged
	}
	if err := revokeCitationProfileExportsForScope(tx, scope, now); err != nil {
		return err
	}
	scope.Enabled = false
	scope.FencedAt = &now
	scope.FenceReason = fenceReason
	scope.DeleteRequestID = strings.TrimSpace(operationID)
	scope.DeletedAt = &now
	scope.ProfileReadVersion = nextVersion
	scope.UpdatedAt = now
	return nil
}

func revokeCitationProfileExportsForScope(tx *gorm.DB, scope *types.CitationProfileScope, now time.Time) error {
	if scope == nil {
		return nil
	}
	revokedSummary, err := citationJSON(map[string]interface{}{
		"contract_version": types.CitationProfileContractVersion,
		"revoked_at":       citationTime(now),
		"receipt_code":     types.CitationProfileReceiptHiddenPurgeScheduled,
	})
	if err != nil {
		return err
	}
	return tx.Model(&types.CitationProfileOperation{}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND operation_type = ? AND status IN ?",
			scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID, types.CitationOperationExport,
			[]string{types.CitationProfileOperationStatusPreparing, types.CitationProfileOperationStatusReady}).
		Updates(map[string]interface{}{
			"status":         types.CitationProfileOperationStatusRevoked,
			"result_summary": revokedSummary,
			"artifact_uri":   "",
			"expires_at":     now,
			"completed_at":   now,
			"updated_at":     now,
		}).Error
}

func citationProfileScopeACLCurrent(scope *types.CitationProfileScope) bool {
	if scope == nil {
		return false
	}
	state := strings.TrimSpace(scope.ACLCheckState)
	return state == "" || state == types.CitationProfileACLStateCurrent
}
func (r *citationProfileRepository) CompleteAssistantMessageWithEvents(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	message *types.Message,
) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("citation profile repository requires database")
	}
	if message == nil {
		return 0, errors.New("citation profile requires message")
	}
	if strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.SessionID) == "" {
		return 0, errors.New("citation profile requires persisted assistant message")
	}
	subjectID = strings.TrimSpace(subjectID)
	if tenantID == 0 || subjectID == "" {
		return 0, errors.New("citation profile requires scoped subject")
	}

	var eventCount int
	var eventIDs []string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&types.Message{}).
			Where("id = ? AND session_id = ?", message.ID, message.SessionID).
			Updates(message)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		events, outboxRows, scopeDeltas, err := r.buildCompletedAnswerEvents(tx, tenantID, subjectID, message)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		eventIDs = make([]string, 0, len(events))
		for _, event := range events {
			eventIDs = append(eventIDs, event.ID)
		}
		if err := tx.Create(&events).Error; err != nil {
			return err
		}
		if err := tx.Create(&outboxRows).Error; err != nil {
			return err
		}
		for scopeID, delta := range scopeDeltas {
			if delta <= 0 {
				continue
			}
			result := tx.Model(&types.CitationProfileScope{}).
				Where("id = ? AND tenant_id = ? AND subject_id = ? AND deleted_at IS NULL AND fenced_at IS NULL", scopeID, tenantID, subjectID).
				Updates(map[string]interface{}{
					"profile_read_version": gorm.Expr("profile_read_version + ?", delta),
					"pending_event_count":  gorm.Expr("pending_event_count + ?", delta),
					"updated_at":           message.UpdatedAt,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("citation profile scope changed before event commit: %s", scopeID)
			}
		}
		eventCount = len(events)
		return nil
	})
	if err != nil {
		return eventCount, err
	}
	for _, eventID := range eventIDs {
		if _, resolveErr := r.ResolveEvidenceEvent(ctx, tenantID, subjectID, eventID); resolveErr != nil {
			return eventCount, resolveErr
		}
	}
	return eventCount, nil
}

func (r *citationProfileRepository) ResolveEvidenceEvent(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	eventID string,
) (*types.EvidenceResolutionRun, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("citation profile repository requires database")
	}
	eventID = strings.TrimSpace(eventID)
	subjectID = strings.TrimSpace(subjectID)
	if tenantID == 0 || subjectID == "" || eventID == "" {
		return nil, errors.New("citation profile resolver requires scoped event")
	}

	var resolvedRun *types.EvidenceResolutionRun
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		event, err := r.loadCitationEventForUpdate(tx, tenantID, subjectID, eventID)
		if err != nil {
			return err
		}
		if event.ActiveRunID != "" {
			run, err := r.loadEvidenceResolutionRun(tx, tenantID, subjectID, event.ActiveRunID)
			if err != nil {
				return err
			}
			if err := markCitationEventOutboxDelivered(tx, event, time.Now().UTC()); err != nil {
				return err
			}
			resolvedRun = run
			return nil
		}
		if event.Status != types.CitationProfileEventStatusPendingResolution {
			return fmt.Errorf("citation profile event is not pending: %s", event.Status)
		}

		scope, err := r.loadActiveCitationScopeByID(tx, tenantID, subjectID, event.KnowledgeBaseID, event.SubjectEpoch, event.ScopeID)
		if err != nil {
			return err
		}
		rows, err := r.loadCurrentWikiSourceRefRows(tx, tenantID, event.KnowledgeBaseID, event.SourceKnowledgeID)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		mappingRevision, watermark := citationResolutionSnapshot(scope, rows, event.SourceKnowledgeID)
		runStatus := types.CitationProfileEventStatusResolved
		if len(rows) == 0 {
			runStatus = types.CitationProfileEventStatusResolvedEmpty
		}
		limits := types.CitationProfileDefaultLimits()
		tooManyLinks := len(rows) > limits.LinksPerEvent
		if tooManyLinks {
			runStatus = types.CitationProfileEventStatusFailed
		}

		run := &types.EvidenceResolutionRun{
			ID:                   uuid.NewString(),
			TenantID:             tenantID,
			SubjectID:            subjectID,
			KnowledgeBaseID:      event.KnowledgeBaseID,
			SubjectEpoch:         event.SubjectEpoch,
			ScopeID:              scope.ID,
			EventID:              event.ID,
			Status:               runStatus,
			RunMappingRevision:   mappingRevision,
			RunUniverseWatermark: watermark,
			InputHash:            citationEvidenceInputHash(event, mappingRevision, watermark),
			InputCount:           1,
			StartedAt:            now,
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		var links []types.EvidenceNodeLink
		if tooManyLinks {
			run.OutputHash = citationSHA256Hex("too_many_links")
			run.ErrorCode = "too_many_links"
			run.ErrorMessage = fmt.Sprintf("resolved %d links, limit is %d", len(rows), limits.LinksPerEvent)
			run.FailedAt = &now
		} else {
			links = buildEvidenceNodeLinks(event, run, rows, now)
			run.OutputCount = len(links)
			run.OutputHash = citationEvidenceOutputHash(links)
			run.ResolvedAt = &now
		}
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		if len(links) > 0 {
			if err := tx.Create(&links).Error; err != nil {
				return err
			}
		}

		eventUpdates := map[string]interface{}{
			"active_run_id": run.ID,
			"status":        runStatus,
			"updated_at":    now,
		}
		if tooManyLinks {
			eventUpdates["failed_reason"] = run.ErrorMessage
		} else {
			eventUpdates["resolved_at"] = now
		}
		result := tx.Model(&types.CitationProfileEvent{}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND COALESCE(active_run_id, '') = ''", event.ID, tenantID, subjectID).
			Updates(eventUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("citation profile event active run changed before publish: %s", event.ID)
		}

		scopeUpdates := map[string]interface{}{
			"profile_read_version":      gorm.Expr("profile_read_version + 1"),
			"pending_event_count":       gorm.Expr("CASE WHEN pending_event_count > 0 THEN pending_event_count - 1 ELSE 0 END"),
			"active_run_id":             run.ID,
			"mapping_revision":          mappingRevision,
			"source_universe_watermark": watermark,
			"updated_at":                now,
		}
		result = tx.Model(&types.CitationProfileScope{}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND subject_epoch = ? AND deleted_at IS NULL AND fenced_at IS NULL",
				scope.ID, tenantID, subjectID, event.SubjectEpoch).
			Updates(scopeUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("citation profile scope changed before run publish: %s", scope.ID)
		}
		if err := markCitationEventOutboxDelivered(tx, event, now); err != nil {
			return err
		}

		resolvedRun = run
		return nil
	})
	return resolvedRun, err
}
func (r *citationProfileRepository) buildCompletedAnswerEvents(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	message *types.Message,
) ([]types.CitationProfileEvent, []types.CitationProfileEventOutbox, map[string]int, error) {
	now := message.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	messageVersion := now.Format(time.RFC3339Nano)
	contentHash := citationSHA256Hex(message.Content)

	events := make([]types.CitationProfileEvent, 0, len(message.KnowledgeReferences))
	outboxRows := make([]types.CitationProfileEventOutbox, 0, len(message.KnowledgeReferences))
	scopeDeltas := make(map[string]int)
	scopesByKB := make(map[string]*types.CitationProfileScope)

	for i, ref := range message.KnowledgeReferences {
		if ref == nil {
			continue
		}
		knowledgeID := strings.TrimSpace(ref.KnowledgeID)
		if knowledgeID == "" {
			continue
		}
		knowledge, err := r.loadCitationKnowledge(tx, tenantID, knowledgeID)
		if err != nil {
			return nil, nil, nil, err
		}
		kbID := strings.TrimSpace(knowledge.KnowledgeBaseID)
		if kbID == "" {
			return nil, nil, nil, fmt.Errorf("citation profile knowledge has no knowledge base: %s", knowledgeID)
		}
		scope, ok := scopesByKB[kbID]
		if !ok {
			loaded, err := r.loadActiveCitationScope(tx, tenantID, subjectID, kbID)
			if err != nil {
				return nil, nil, nil, err
			}
			scope = loaded
			scopesByKB[kbID] = scope
		}
		if scope == nil {
			continue
		}
		producerKey := citationProducerEventKey(
			tenantID,
			subjectID,
			kbID,
			scope.SubjectEpoch,
			message.ID,
			i,
			knowledge.ID,
			ref.ID,
			ref.ChunkIndex,
		)
		exists, err := r.citationEventExists(tx, tenantID, subjectID, kbID, scope.SubjectEpoch, producerKey)
		if err != nil {
			return nil, nil, nil, err
		}
		if exists {
			continue
		}

		sourceRefSnapshot, err := citationReferenceSnapshot(ref)
		if err != nil {
			return nil, nil, nil, err
		}
		knowledgeSnapshot, err := citationKnowledgeSnapshot(knowledge)
		if err != nil {
			return nil, nil, nil, err
		}
		knowledgeBaseProof, err := r.citationKnowledgeBaseProof(tx, tenantID, knowledge, now)
		if err != nil {
			return nil, nil, nil, err
		}

		eventID := uuid.NewString()
		chunkIndex := ref.ChunkIndex
		normalizedRef := types.WikiSourceKnowledgeID(knowledge.ID)
		if normalizedRef == "" {
			normalizedRef = knowledge.ID
		}

		events = append(events, types.CitationProfileEvent{
			ID:                   eventID,
			TenantID:             tenantID,
			SubjectID:            subjectID,
			KnowledgeBaseID:      kbID,
			SubjectEpoch:         scope.SubjectEpoch,
			ScopeID:              scope.ID,
			SessionID:            message.SessionID,
			MessageID:            message.ID,
			MessageVersion:       messageVersion,
			MessageCompletedAt:   &now,
			OriginReferenceIndex: i,
			SourceKnowledgeID:    knowledge.ID,
			SourceResultID:       citationTrim(ref.ID, 128),
			SourceChunkIndex:     &chunkIndex,
			SourceRefRaw:         citationRawRef(knowledge.ID, ref.ID, ref.ChunkIndex),
			SourceRefNormalized:  citationTrim(normalizedRef, 512),
			SourceRefsSnapshot:   sourceRefSnapshot,
			KnowledgeSnapshot:    knowledgeSnapshot,
			KnowledgeBaseProof:   knowledgeBaseProof,
			ProducerEventKey:     producerKey,
			ContentHash:          contentHash,
			Status:               types.CitationProfileEventStatusPendingResolution,
			CreatedAt:            now,
			UpdatedAt:            now,
		})
		outboxRows = append(outboxRows, types.CitationProfileEventOutbox{
			ID:              uuid.NewString(),
			TenantID:        tenantID,
			SubjectID:       subjectID,
			KnowledgeBaseID: kbID,
			SubjectEpoch:    scope.SubjectEpoch,
			ScopeID:         scope.ID,
			EventID:         eventID,
			Status:          types.CitationProfileOutboxStatusPending,
			NextAttemptAt:   now,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		scopeDeltas[scope.ID]++
	}

	return events, outboxRows, scopeDeltas, nil
}

func (r *citationProfileRepository) loadCitationKnowledge(tx *gorm.DB, tenantID uint64, knowledgeID string) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	err := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).First(&knowledge).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("citation profile knowledge proof not found: %s", knowledgeID)
		}
		return nil, err
	}
	return &knowledge, nil
}

func (r *citationProfileRepository) loadActiveCitationScope(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileScope, error) {
	var scope types.CitationProfileScope
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL",
			tenantID,
			subjectID,
			kbID,
			true,
		).
		First(&scope).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !citationProfileScopeACLCurrent(&scope) {
		return nil, nil
	}
	return &scope, nil
}

func (r *citationProfileRepository) citationEventExists(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
	producerKey string,
) (bool, error) {
	var existing types.CitationProfileEvent
	err := tx.Select("id").
		Where(
			"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND producer_event_key = ? AND retracted_at IS NULL",
			tenantID,
			subjectID,
			kbID,
			subjectEpoch,
			producerKey,
		).
		First(&existing).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *citationProfileRepository) citationKnowledgeBaseProof(
	tx *gorm.DB,
	tenantID uint64,
	knowledge *types.Knowledge,
	readAt time.Time,
) (json.RawMessage, error) {
	var kb struct {
		ID        string
		TenantID  uint64
		UpdatedAt time.Time
	}
	err := tx.Table("knowledge_bases").
		Select("id, tenant_id, updated_at").
		Where("tenant_id = ? AND id = ? AND deleted_at IS NULL", tenantID, knowledge.KnowledgeBaseID).
		First(&kb).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("citation profile knowledge base proof not found: %s", knowledge.KnowledgeBaseID)
		}
		return nil, err
	}
	return citationJSON(map[string]interface{}{
		"knowledge_id":              knowledge.ID,
		"knowledge_tenant_id":       knowledge.TenantID,
		"knowledge_base_id":         kb.ID,
		"knowledge_base_tenant_id":  kb.TenantID,
		"knowledge_updated_at":      citationTime(knowledge.UpdatedAt),
		"knowledge_base_updated_at": citationTime(kb.UpdatedAt),
		"proof_read_at":             citationTime(readAt),
	})
}

func citationReferenceSnapshot(ref *types.SearchResult) (json.RawMessage, error) {
	return citationJSON(map[string]interface{}{
		"id":                 ref.ID,
		"knowledge_id":       ref.KnowledgeID,
		"knowledge_base_id":  ref.KnowledgeBaseID,
		"knowledge_title":    ref.KnowledgeTitle,
		"chunk_index":        ref.ChunkIndex,
		"start_at":           ref.StartAt,
		"end_at":             ref.EndAt,
		"seq":                ref.Seq,
		"match_type":         ref.MatchType,
		"chunk_type":         ref.ChunkType,
		"parent_chunk_id":    ref.ParentChunkID,
		"sub_chunk_id":       append([]string(nil), ref.SubChunkID...),
		"content_revision":   ref.ContentRevision,
		"content_rewritten":  ref.ContentRewritten,
		"source_ref_version": types.CitationProfileContractVersion,
	})
}

func citationKnowledgeSnapshot(knowledge *types.Knowledge) (json.RawMessage, error) {
	return citationJSON(map[string]interface{}{
		"id":                knowledge.ID,
		"tenant_id":         knowledge.TenantID,
		"knowledge_base_id": knowledge.KnowledgeBaseID,
		"title":             knowledge.Title,
		"file_name":         knowledge.FileName,
		"file_type":         knowledge.FileType,
		"source":            knowledge.Source,
		"channel":           knowledge.Channel,
		"parse_status":      knowledge.ParseStatus,
		"enable_status":     knowledge.EnableStatus,
		"updated_at":        citationTime(knowledge.UpdatedAt),
	})
}

func citationJSON(value interface{}) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func citationProducerEventKey(
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
	messageID string,
	refIndex int,
	knowledgeID string,
	resultID string,
	chunkIndex int,
) string {
	h := sha256.New()
	fmt.Fprintf(
		h,
		"%d\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%d",
		tenantID,
		subjectID,
		kbID,
		subjectEpoch,
		messageID,
		refIndex,
		knowledgeID,
		resultID,
		chunkIndex,
	)
	return hex.EncodeToString(h.Sum(nil))
}

func citationRawRef(knowledgeID string, resultID string, chunkIndex int) string {
	return citationTrim(fmt.Sprintf("%s|%s|%d", knowledgeID, resultID, chunkIndex), 512)
}

func citationSHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func citationTrim(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func citationTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (r *citationProfileRepository) loadCitationEventForUpdate(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	eventID string,
) (*types.CitationProfileEvent, error) {
	var event types.CitationProfileEvent
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND tenant_id = ? AND subject_id = ? AND retracted_at IS NULL", eventID, tenantID, subjectID).
		First(&event).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("citation profile event not found: %s", eventID)
		}
		return nil, err
	}
	return &event, nil
}

func (r *citationProfileRepository) loadEvidenceResolutionRun(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	runID string,
) (*types.EvidenceResolutionRun, error) {
	var run types.EvidenceResolutionRun
	err := tx.Where("id = ? AND tenant_id = ? AND subject_id = ?", runID, tenantID, subjectID).First(&run).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("citation profile evidence run not found: %s", runID)
		}
		return nil, err
	}
	return &run, nil
}

func (r *citationProfileRepository) loadActiveCitationScopeByID(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
	scopeID string,
) (*types.CitationProfileScope, error) {
	var scope types.CitationProfileScope
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL",
			scopeID,
			tenantID,
			subjectID,
			kbID,
			subjectEpoch,
			true,
		).
		First(&scope).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("citation profile active scope not found: %s", scopeID)
		}
		return nil, err
	}
	if !citationProfileScopeACLCurrent(&scope) {
		return nil, types.ErrCitationProfileUnavailable
	}
	return &scope, nil
}

func (r *citationProfileRepository) loadCurrentWikiSourceRefRows(
	tx *gorm.DB,
	tenantID uint64,
	kbID string,
	sourceKnowledgeID string,
) ([]types.WikiSourceRefIndex, error) {
	var rows []types.WikiSourceRefIndex
	err := tx.Where(
		"tenant_id = ? AND knowledge_base_id = ? AND source_knowledge_id = ? AND lifecycle_state = ?",
		tenantID,
		kbID,
		sourceKnowledgeID,
		"current",
	).
		Order("page_uuid ASC, page_version ASC, normalized_ref ASC").
		Find(&rows).Error
	return rows, err
}

func buildEvidenceNodeLinks(
	event *types.CitationProfileEvent,
	run *types.EvidenceResolutionRun,
	rows []types.WikiSourceRefIndex,
	createdAt time.Time,
) []types.EvidenceNodeLink {
	links := make([]types.EvidenceNodeLink, 0, len(rows))
	for _, row := range rows {
		links = append(links, types.EvidenceNodeLink{
			ID:                uuid.NewString(),
			TenantID:          event.TenantID,
			SubjectID:         event.SubjectID,
			KnowledgeBaseID:   event.KnowledgeBaseID,
			SubjectEpoch:      event.SubjectEpoch,
			ScopeID:           event.ScopeID,
			EventID:           event.ID,
			ResolutionRunID:   run.ID,
			SourceKnowledgeID: event.SourceKnowledgeID,
			PageUUID:          row.PageUUID,
			PageVersion:       row.PageVersion,
			NormalizedRef:     row.NormalizedRef,
			RelationState:     types.EvidenceRelationCurrent,
			RelationSource:    "source_ref_index",
			MappingRevision:   row.MappingRevision,
			UniverseWatermark: run.RunUniverseWatermark,
			CreatedAt:         createdAt,
		})
	}
	return links
}

func citationResolutionSnapshot(
	scope *types.CitationProfileScope,
	rows []types.WikiSourceRefIndex,
	sourceKnowledgeID string,
) (uint64, string) {
	mappingRevision := scope.MappingRevision
	var b strings.Builder
	for _, row := range rows {
		if row.MappingRevision > mappingRevision {
			mappingRevision = row.MappingRevision
		}
		fmt.Fprintf(&b, "%s\x00%d\x00%s\x00%s\x00", row.PageUUID, row.PageVersion, row.NormalizedRef, row.IndexWatermark)
	}
	if b.Len() == 0 {
		watermark := strings.TrimSpace(scope.SourceUniverseWatermark)
		if watermark == "" {
			watermark = "empty:" + sourceKnowledgeID
		}
		return mappingRevision, citationTrim(watermark, 128)
	}
	return mappingRevision, "wm:" + citationSHA256Hex(b.String())
}

func citationEvidenceInputHash(event *types.CitationProfileEvent, mappingRevision uint64, watermark string) string {
	return citationSHA256Hex(fmt.Sprintf("%s:%s:%d:%s", event.ID, event.ProducerEventKey, mappingRevision, watermark))
}

func citationEvidenceOutputHash(links []types.EvidenceNodeLink) string {
	if len(links) == 0 {
		return citationSHA256Hex("links:0")
	}
	var b strings.Builder
	for _, link := range links {
		fmt.Fprintf(&b, "%s\x00%d\x00%s\x00%s\x00", link.PageUUID, link.PageVersion, link.NormalizedRef, link.RelationState)
	}
	return citationSHA256Hex(b.String())
}

func (r *citationProfileRepository) loadDeletedCitationScope(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileScope, error) {
	var scope types.CitationProfileScope
	err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND deleted_at IS NOT NULL",
		tenantID,
		subjectID,
		kbID,
	).
		Order("updated_at DESC").
		First(&scope).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &scope, nil
}
func (r *citationProfileRepository) loadLiveCitationScopeForUpdate(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileScope, error) {
	var scope types.CitationProfileScope
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", tenantID, subjectID, kbID).
		First(&scope).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &scope, nil
}

func (r *citationProfileRepository) loadCitationScopeByID(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	scopeID string,
) (*types.CitationProfileScope, error) {
	if strings.TrimSpace(scopeID) == "" {
		return nil, types.ErrCitationProfileNotFound
	}
	var scope types.CitationProfileScope
	err := tx.Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?", scopeID, tenantID, subjectID, kbID).
		First(&scope).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
		}
		return nil, err
	}
	return &scope, nil
}

func (r *citationProfileRepository) findCitationProfileOperationByIdem(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
	operationType string,
	idempotencyKey string,
	requestDigest string,
) (*types.CitationProfileOperation, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, false, nil
	}
	var operation types.CitationProfileOperation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND operation_type = ? AND idempotency_key = ?",
			tenantID, subjectID, kbID, subjectEpoch, operationType, idempotencyKey).
		Order("created_at DESC").
		First(&operation).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	storedDigest := citationOperationRequestDigest(operation.RequestSnapshot)
	if storedDigest == "" || storedDigest != requestDigest {
		return nil, false, types.ErrCitationProfileIdempotencyConflict
	}
	return &operation, true, nil
}

func (r *citationProfileRepository) loadCitationProfileOperationByID(
	db *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
	operationType string,
	operationID string,
) (*types.CitationProfileOperation, error) {
	var operation types.CitationProfileOperation
	err := db.Where(
		"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND operation_type = ?",
		operationID, tenantID, subjectID, kbID, subjectEpoch, operationType,
	).First(&operation).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
		}
		return nil, err
	}
	return &operation, nil
}

func createCitationProfileOperation(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
	operationType string,
	idempotencyKey string,
	requestDigest string,
	requestFields map[string]interface{},
	status string,
	resultSummary json.RawMessage,
	artifactURI string,
	expiresAt *time.Time,
	now time.Time,
) (*types.CitationProfileOperation, error) {
	if scope == nil {
		return nil, types.ErrCitationProfileNotFound
	}
	requestSnapshot, err := citationProfileOperationRequestSnapshot(requestDigest, requestFields)
	if err != nil {
		return nil, err
	}
	completedAt := &now
	operation := &types.CitationProfileOperation{
		ID:              uuid.NewString(),
		TenantID:        scope.TenantID,
		SubjectID:       scope.SubjectID,
		KnowledgeBaseID: scope.KnowledgeBaseID,
		SubjectEpoch:    scope.SubjectEpoch,
		ScopeID:         scope.ID,
		OperationType:   operationType,
		IdempotencyKey:  strings.TrimSpace(idempotencyKey),
		Status:          status,
		RequestSnapshot: requestSnapshot,
		ResultSummary:   resultSummary,
		ArtifactURI:     artifactURI,
		NextAttemptAt:   now,
		CompletedAt:     completedAt,
		ExpiresAt:       expiresAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if status == types.CitationProfileOperationStatusPreparing {
		operation.CompletedAt = nil
	}
	return operation, tx.Create(operation).Error
}

func citationProfileOperationRequestSnapshot(requestDigest string, fields map[string]interface{}) (json.RawMessage, error) {
	payload := map[string]interface{}{
		"contract_version": types.CitationProfileContractVersion,
		"request_digest":   requestDigest,
	}
	for k, v := range fields {
		payload[k] = v
	}
	return citationJSON(payload)
}

func citationOperationRequestDigest(snapshot json.RawMessage) string {
	var payload map[string]interface{}
	if len(snapshot) == 0 || json.Unmarshal(snapshot, &payload) != nil {
		return ""
	}
	value, _ := payload["request_digest"].(string)
	return strings.TrimSpace(value)
}

func citationProfileOperationResult(scope *types.CitationProfileScope, fields map[string]interface{}, capturedAt time.Time) (json.RawMessage, error) {
	payload := map[string]interface{}{
		"contract_version": types.CitationProfileContractVersion,
		"snapshot":         citationProfileScopeSnapshotData(scope, capturedAt),
	}
	for k, v := range fields {
		payload[k] = v
	}
	return citationJSON(payload)
}

func citationProfileScopeSnapshotData(scope *types.CitationProfileScope, capturedAt time.Time) map[string]interface{} {
	if scope == nil {
		return map[string]interface{}{
			"read_version": "0",
			"captured_at":  citationTime(capturedAt),
		}
	}
	watermark := strings.TrimSpace(scope.SourceUniverseWatermark)
	return map[string]interface{}{
		"subject_epoch":                   scope.SubjectEpoch,
		"read_version":                    fmt.Sprintf("%d", scope.ProfileReadVersion),
		"mapping_revision":                fmt.Sprintf("%d", scope.MappingRevision),
		"source_universe_watermark":       watermark,
		"current_index_watermark":         watermark,
		"current_wiki_universe_watermark": watermark,
		"pending_event_count":             scope.PendingEventCount,
		"pending_mapping_count":           scope.PendingMappingCount,
		"dirty_event_count":               scope.DirtyMappingCount,
		"dirty_mapping_count":             scope.DirtyMappingCount,
		"captured_at":                     citationTime(capturedAt),
	}
}

func citationRequestDigest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func citationSyntheticOperation(kbID string, operationType string) *types.CitationProfileOperation {
	now := time.Now().UTC()
	return &types.CitationProfileOperation{
		ID:              uuid.NewString(),
		KnowledgeBaseID: strings.TrimSpace(kbID),
		OperationType:   operationType,
		Status:          types.CitationProfileOperationStatusAccepted,
		CreatedAt:       now,
		UpdatedAt:       now,
		CompletedAt:     &now,
	}
}

func (r *citationProfileRepository) loadCitationProfileExportEvents(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
) ([]types.CitationProfileEvent, bool, error) {
	limit := types.CitationProfileDefaultLimits().ExportEvents
	var events []types.CitationProfileEvent
	err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND retracted_at IS NULL",
		tenantID, subjectID, kbID, subjectEpoch,
	).Order("created_at ASC, id ASC").Limit(limit + 1).Find(&events).Error
	if err != nil {
		return nil, false, err
	}
	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
	}
	return events, truncated, nil
}

func (r *citationProfileRepository) loadCitationProfileExportLinks(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
	events []types.CitationProfileEvent,
) ([]types.EvidenceNodeLink, bool, error) {
	if len(events) == 0 {
		return nil, false, nil
	}
	eventIDs := make([]string, 0, len(events))
	for _, event := range events {
		eventIDs = append(eventIDs, event.ID)
	}
	limit := types.CitationProfileDefaultLimits().Links
	var links []types.EvidenceNodeLink
	err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND event_id IN ?",
		tenantID, subjectID, kbID, subjectEpoch, eventIDs,
	).Order("created_at ASC, id ASC").Limit(limit + 1).Find(&links).Error
	if err != nil {
		return nil, false, err
	}
	truncated := len(links) > limit
	if truncated {
		links = links[:limit]
	}
	return links, truncated, nil
}

func citationProfileExportPayload(
	scope *types.CitationProfileScope,
	events []types.CitationProfileEvent,
	links []types.EvidenceNodeLink,
	eventsTruncated bool,
	linksTruncated bool,
	capturedAt time.Time,
) (json.RawMessage, error) {
	payload := map[string]interface{}{
		"schema_version":    types.CitationProfileExportSchemaVersion,
		"contract_version":  types.CitationProfileContractVersion,
		"knowledge_base_id": scope.KnowledgeBaseID,
		"scope":             scope.DTO(),
		"snapshot":          citationProfileScopeSnapshotData(scope, capturedAt),
		"events":            citationProfileExportEvents(events),
		"links":             citationProfileExportLinks(links),
		"truncated": map[string]bool{
			"events": eventsTruncated,
			"links":  linksTruncated,
		},
		"guidance": types.CitationProfileDefaultGuidance(),
	}
	return citationJSON(payload)
}

func citationProfileExportEvents(events []types.CitationProfileEvent) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(events))
	for _, event := range events {
		chunkIndex := interface{}(nil)
		if event.SourceChunkIndex != nil {
			chunkIndex = *event.SourceChunkIndex
		}
		items = append(items, map[string]interface{}{
			"event_id":               event.ID,
			"knowledge_base_id":      event.KnowledgeBaseID,
			"subject_epoch":          event.SubjectEpoch,
			"scope_id":               event.ScopeID,
			"session_id":             event.SessionID,
			"message_id":             event.MessageID,
			"message_version":        event.MessageVersion,
			"message_completed_at":   citationOptionalTime(event.MessageCompletedAt),
			"origin_reference_index": event.OriginReferenceIndex,
			"source_knowledge_id":    event.SourceKnowledgeID,
			"source_result_id":       event.SourceResultID,
			"source_chunk_index":     chunkIndex,
			"source_ref_normalized":  event.SourceRefNormalized,
			"knowledge_base_proof":   event.KnowledgeBaseProof,
			"status":                 event.Status,
			"active_run_id":          event.ActiveRunID,
			"created_at":             citationTime(event.CreatedAt),
			"updated_at":             citationTime(event.UpdatedAt),
		})
	}
	return items
}

func citationProfileExportLinks(links []types.EvidenceNodeLink) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(links))
	for _, link := range links {
		items = append(items, map[string]interface{}{
			"relation_id":            link.ID,
			"event_id":               link.EventID,
			"run_id":                 link.ResolutionRunID,
			"knowledge_base_id":      link.KnowledgeBaseID,
			"subject_epoch":          link.SubjectEpoch,
			"source_knowledge_id":    link.SourceKnowledgeID,
			"page_uuid":              link.PageUUID,
			"page_version":           fmt.Sprintf("%d", link.PageVersion),
			"normalized_ref":         link.NormalizedRef,
			"relation_state":         link.RelationState,
			"relation_source":        link.RelationSource,
			"run_mapping_revision":   fmt.Sprintf("%d", link.MappingRevision),
			"run_universe_watermark": link.UniverseWatermark,
			"created_at":             citationTime(link.CreatedAt),
		})
	}
	return items
}

func citationOptionalTime(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return citationTime(*value)
}
