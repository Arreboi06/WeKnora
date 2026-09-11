package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *citationProfileRepository) ApplyCorrection(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	kbID string,
	expectedReadVersion uint64,
	idempotencyKey string,
	action string,
	eventID string,
	pageUUID string,
	reasonCode string,
) (*types.CitationProfileCorrection, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	subjectID = strings.TrimSpace(subjectID)
	kbID = strings.TrimSpace(kbID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	action = strings.TrimSpace(action)
	eventID = strings.TrimSpace(eventID)
	pageUUID = strings.TrimSpace(pageUUID)
	reasonCode = citationTrim(reasonCode, 512)
	if tenantID == 0 || subjectID == "" || kbID == "" || idempotencyKey == "" || action == "" || eventID == "" || pageUUID == "" {
		return nil, fmt.Errorf("%w: correction requires scope, action, event, page and idempotency", types.ErrCitationProfileInvalidRequest)
	}
	switch action {
	case types.CitationCorrectionConfirmRelevant, types.CitationCorrectionRejectMapping, types.CitationCorrectionRetractEvent:
	default:
		return nil, fmt.Errorf("%w: unsupported correction action", types.ErrCitationProfileInvalidRequest)
	}

	var applied *types.CitationProfileCorrection
	var verifiedScope *types.CitationProfileScope
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadLiveCitationScopeForUpdate(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		if scope == nil || !scope.Enabled {
			return types.ErrCitationProfileNotFound
		}
		if scope.FencedAt != nil {
			return types.ErrCitationProfileDeleted
		}
		if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
			return err
		}
		if existing, found, err := r.findCitationProfileCorrectionByIdem(
			tx, tenantID, subjectID, kbID, scope.SubjectEpoch, idempotencyKey, expectedReadVersion, action, eventID, pageUUID, reasonCode,
		); err != nil {
			return err
		} else if found {
			if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
				return err
			}
			applied = existing
			scopeSnapshot := *scope
			verifiedScope = &scopeSnapshot
			return nil
		}
		if scope.ProfileReadVersion != expectedReadVersion {
			return types.ErrCitationProfileChanged
		}

		event, err := r.loadCitationCorrectionEvent(tx, scope, eventID)
		if err != nil {
			return err
		}
		if event.RetractedAt != nil {
			return types.ErrCitationProfileDeleted
		}
		link, err := r.loadCitationCorrectionLinkForUpdate(tx, scope, event, pageUUID)
		if err != nil {
			return err
		}

		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		nextVersion := scope.ProfileReadVersion + 1
		if err := r.applyCitationCorrectionMutation(tx, scope, event, link, action, now); err != nil {
			return err
		}
		correction := &types.CitationProfileCorrection{
			ID:                   uuid.NewString(),
			TenantID:             scope.TenantID,
			SubjectID:            scope.SubjectID,
			KnowledgeBaseID:      scope.KnowledgeBaseID,
			SubjectEpoch:         scope.SubjectEpoch,
			ScopeID:              scope.ID,
			EventID:              event.ID,
			PageUUID:             pageUUID,
			PageVersion:          &link.PageVersion,
			CorrectionType:       action,
			Reason:               reasonCode,
			ActorID:              subjectID,
			ExpectedReadVersion:  scope.ProfileReadVersion,
			ResultingReadVersion: nextVersion,
			IdempotencyKey:       idempotencyKey,
			CreatedAt:            now,
		}
		if err := tx.Create(correction).Error; err != nil {
			return err
		}
		result := tx.Model(&types.CitationProfileScope{}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND profile_read_version = ? AND deleted_at IS NULL AND fenced_at IS NULL",
				scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ProfileReadVersion).
			Updates(map[string]interface{}{
				"profile_read_version": nextVersion,
				"updated_at":           now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return types.ErrCitationProfileChanged
		}
		if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
			return err
		}
		var current types.CitationProfileScope
		if err := tx.Where(
			"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ?",
			scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch,
		).First(&current).Error; err != nil {
			return err
		}
		if err := r.ensureCitationProfileACLCurrentTx(tx, &current); err != nil {
			return err
		}
		// Return the database's persisted representation, not the pre-insert Go
		// value. PostgreSQL stores timestamptz at microsecond precision, so using
		// the in-memory nanoseconds for the first response would make an otherwise
		// identical idempotent replay produce different snapshot cutoffs.
		var persisted types.CitationProfileCorrection
		if err := tx.Where(
			"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ?",
			correction.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID,
		).First(&persisted).Error; err != nil {
			return err
		}
		verifiedScope = &current
		applied = &persisted
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := r.verifyCitationProfileReadSnapshotFresh(ctx, verifiedScope); err != nil {
		return nil, err
	}
	return applied, nil
}

func (r *citationProfileRepository) findCitationProfileCorrectionByIdem(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	kbID string,
	subjectEpoch string,
	idempotencyKey string,
	expectedReadVersion uint64,
	action string,
	eventID string,
	pageUUID string,
	reasonCode string,
) (*types.CitationProfileCorrection, bool, error) {
	var correction types.CitationProfileCorrection
	err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND idempotency_key = ?",
		tenantID, subjectID, kbID, subjectEpoch, idempotencyKey,
	).Order("created_at DESC, id DESC").First(&correction).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if correction.ExpectedReadVersion != expectedReadVersion ||
		correction.CorrectionType != action ||
		correction.EventID != eventID ||
		correction.PageUUID != pageUUID ||
		correction.Reason != reasonCode {
		return nil, false, types.ErrCitationProfileIdempotencyConflict
	}
	return &correction, true, nil
}

func (r *citationProfileRepository) loadCitationCorrectionEvent(tx *gorm.DB, scope *types.CitationProfileScope, eventID string) (*types.CitationProfileEvent, error) {
	var event types.CitationProfileEvent
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ?",
		eventID,
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		scope.SubjectEpoch,
		scope.ID,
	).First(&event).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
		}
		return nil, err
	}
	return &event, nil
}

func (r *citationProfileRepository) loadCitationCorrectionLinkForUpdate(tx *gorm.DB, scope *types.CitationProfileScope, event *types.CitationProfileEvent, pageUUID string) (*types.EvidenceNodeLink, error) {
	if event == nil || strings.TrimSpace(event.ActiveRunID) == "" {
		return nil, types.ErrCitationProfileNotFound
	}
	var link types.EvidenceNodeLink
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND resolution_run_id = ? AND page_uuid = ?",
		scope.TenantID,
		scope.SubjectID,
		scope.KnowledgeBaseID,
		scope.SubjectEpoch,
		scope.ID,
		event.ID,
		event.ActiveRunID,
		pageUUID,
	).First(&link).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
		}
		return nil, err
	}
	return &link, nil
}

func (r *citationProfileRepository) applyCitationCorrectionMutation(tx *gorm.DB, scope *types.CitationProfileScope, event *types.CitationProfileEvent, link *types.EvidenceNodeLink, action string, now time.Time) error {
	switch action {
	case types.CitationCorrectionConfirmRelevant:
		result := tx.Model(&types.EvidenceNodeLink{}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND page_uuid = ?",
				link.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID, event.ID, link.PageUUID).
			Update("relation_state", types.EvidenceRelationCurrent)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileChanged
		}
		return nil
	case types.CitationCorrectionRejectMapping:
		result := tx.Model(&types.EvidenceNodeLink{}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND page_uuid = ?",
				link.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID, event.ID, link.PageUUID).
			Update("relation_state", types.EvidenceRelationDisputed)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileChanged
		}
		return nil
	case types.CitationCorrectionRetractEvent:
		if event.RetractedAt == nil {
			result := tx.Model(&types.CitationProfileEvent{}).
				Where("id = ? AND tenant_id = ? AND subject_id = ? AND scope_id = ? AND retracted_at IS NULL", event.ID, scope.TenantID, scope.SubjectID, scope.ID).
				Updates(map[string]interface{}{"retracted_at": now, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return types.ErrCitationProfileChanged
			}
		}
		result := tx.Model(&types.EvidenceNodeLink{}).
			Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ?",
				scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID, event.ID).
			Update("relation_state", types.EvidenceRelationDisputed)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return types.ErrCitationProfileChanged
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported correction action", types.ErrCitationProfileInvalidRequest)
	}
}
