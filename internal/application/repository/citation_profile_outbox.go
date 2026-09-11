package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ interfaces.CitationProfileOutboxRepository = (*citationProfileRepository)(nil)

// NewCitationProfileOutboxRepository exposes the background-worker facet of
// the citation repository without widening the request-path interface.
func NewCitationProfileOutboxRepository(db *gorm.DB) interfaces.CitationProfileOutboxRepository {
	return &citationProfileRepository{db: db}
}

const (
	citationProfileOutboxDefaultLease  = 2 * time.Minute
	citationProfileOutboxMaxBatch      = 100
	citationProfileOutboxMaxRetryDelay = 5 * time.Minute
)

// ClaimCitationProfileEventOutbox atomically leases due work. PostgreSQL uses
// SKIP LOCKED so replicas receive disjoint rows; SQLite relies on its
// single-writer transaction and the conditional update below. A stale
// delivering row is treated exactly like pending work, which is what makes a
// process crash recoverable without deleting the durable event.
func (r *citationProfileRepository) ClaimCitationProfileEventOutbox(
	ctx context.Context,
	workerID string,
	limit int,
	now time.Time,
	lease time.Duration,
) ([]types.CitationProfileEventOutbox, error) {
	if r == nil || r.db == nil {
		return nil, types.ErrCitationProfileUnavailable
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, errors.New("citation profile outbox worker id is required")
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > citationProfileOutboxMaxBatch {
		limit = citationProfileOutboxMaxBatch
	}
	// now is retained only for interface compatibility. Eligibility, ACL
	// freshness, lock timestamps, and lease deadlines all use database time.
	_ = now
	lease = citationProfileBoundedWorkerLease(lease)
	dialect := r.db.Dialector.Name()
	currentACLWhere, currentACLArgs, err := citationProfileACLCurrentDatabaseSQL("s", dialect)
	if err != nil {
		return nil, err
	}
	outboxDueSQL, err := citationProfileColumnDueAtDatabaseNowSQL("citation_profile_event_outbox.next_attempt_at", dialect)
	if err != nil {
		return nil, err
	}
	outboxLeaseDueSQL, err := citationProfileColumnDueAtDatabaseNowSQL("citation_profile_event_outbox.lease_until", dialect)
	if err != nil {
		return nil, err
	}
	updateDueSQL, err := citationProfileColumnDueAtDatabaseNowSQL("next_attempt_at", dialect)
	if err != nil {
		return nil, err
	}
	updateLeaseDueSQL, err := citationProfileColumnDueAtDatabaseNowSQL("lease_until", dialect)
	if err != nil {
		return nil, err
	}

	claimed := make([]types.CitationProfileEventOutbox, 0, limit)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&types.CitationProfileEventOutbox{}).
			Where("(citation_profile_event_outbox.status = ? OR (citation_profile_event_outbox.status = ? AND (citation_profile_event_outbox.lease_until IS NULL OR "+outboxLeaseDueSQL+")))", types.CitationProfileOutboxStatusPending, types.CitationProfileOutboxStatusDelivering).
			Where("citation_profile_event_outbox.delivered_at IS NULL AND citation_profile_event_outbox.deadletter_at IS NULL AND "+outboxDueSQL).
			Where("EXISTS (SELECT 1 FROM citation_profile_scopes AS s WHERE s.id = citation_profile_event_outbox.scope_id AND s.tenant_id = citation_profile_event_outbox.tenant_id AND s.subject_id = citation_profile_event_outbox.subject_id AND s.knowledge_base_id = citation_profile_event_outbox.knowledge_base_id AND s.subject_epoch = citation_profile_event_outbox.subject_epoch AND s.enabled = ? AND s.deleted_at IS NULL AND s.fenced_at IS NULL AND "+currentACLWhere+")",
				append([]interface{}{true}, currentACLArgs...)...).
			Order("citation_profile_event_outbox.created_at ASC, citation_profile_event_outbox.id ASC").
			Limit(limit)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{
				Strength: "UPDATE",
				Table:    clause.Table{Name: clause.CurrentTable},
				Options:  "SKIP LOCKED",
			})
		}
		var candidates []types.CitationProfileEventOutbox
		if err := query.Find(&candidates).Error; err != nil {
			return err
		}
		for i := range candidates {
			candidate := &candidates[i]
			leaseStartedAt, err := citationProfileDatabaseNow(tx)
			if err != nil {
				return err
			}
			leaseUntil := leaseStartedAt.Add(lease)
			result := tx.Model(&types.CitationProfileEventOutbox{}).
				Where(
					"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ?",
					candidate.ID,
					candidate.TenantID,
					candidate.SubjectID,
					candidate.KnowledgeBaseID,
					candidate.SubjectEpoch,
					candidate.ScopeID,
					candidate.EventID,
				).
				Where("(status = ? OR (status = ? AND (lease_until IS NULL OR "+updateLeaseDueSQL+")))", types.CitationProfileOutboxStatusPending, types.CitationProfileOutboxStatusDelivering).
				Where("delivered_at IS NULL AND deadletter_at IS NULL AND "+updateDueSQL).
				Where("EXISTS (SELECT 1 FROM citation_profile_scopes AS s WHERE s.id = ? AND s.tenant_id = ? AND s.subject_id = ? AND s.knowledge_base_id = ? AND s.subject_epoch = ? AND s.enabled = ? AND s.deleted_at IS NULL AND s.fenced_at IS NULL AND "+currentACLWhere+")",
					append([]interface{}{candidate.ScopeID, candidate.TenantID, candidate.SubjectID, candidate.KnowledgeBaseID, candidate.SubjectEpoch, true}, currentACLArgs...)...).
				Updates(map[string]interface{}{
					"status":      types.CitationProfileOutboxStatusDelivering,
					"locked_at":   leaseStartedAt,
					"lease_until": leaseUntil,
					"locked_by":   workerID,
					"updated_at":  leaseStartedAt,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				continue
			}
			var stored types.CitationProfileEventOutbox
			if err := tx.Where(
				"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND locked_by = ?",
				candidate.ID, candidate.TenantID, candidate.SubjectID, candidate.KnowledgeBaseID,
				candidate.SubjectEpoch, candidate.ScopeID, candidate.EventID, workerID,
			).First(&stored).Error; err != nil {
				return err
			}
			claimed = append(claimed, stored)
		}
		return nil
	})
	return claimed, err
}

// StartCitationProfileEventOutboxAttempt charges only work about to execute.
// The claim timestamp and attempt count fence stale copies, including leases
// reclaimed by the same process. Renewal gives this attempt its own lease.
func (r *citationProfileRepository) StartCitationProfileEventOutboxAttempt(
	ctx context.Context, claim *types.CitationProfileEventOutbox, now time.Time,
) (int, error) {
	if r == nil || r.db == nil {
		return 0, types.ErrCitationProfileUnavailable
	}
	if !validCitationProfileOutboxClaim(claim) {
		return 0, types.ErrCitationProfileOutboxLeaseLost
	}
	_ = now
	dialect := r.db.Dialector.Name()
	currentACLWhere, currentACLArgs, err := citationProfileACLCurrentDatabaseSQL("s", dialect)
	if err != nil {
		return 0, err
	}
	leaseCurrentSQL, err := citationProfileColumnAfterDatabaseNowSQL("lease_until", dialect)
	if err != nil {
		return 0, err
	}
	leaseDuration := citationProfileBoundedWorkerLease(claim.LeaseUntil.Sub(*claim.LockedAt))
	var renewed types.CitationProfileEventOutbox
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		databaseNow, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		leaseUntil := databaseNow.Add(leaseDuration)
		result := applyCitationProfileOutboxClaim(tx.Model(&types.CitationProfileEventOutbox{}), claim).
			Where("status = ? AND delivered_at IS NULL AND deadletter_at IS NULL AND lease_until IS NOT NULL", types.CitationProfileOutboxStatusDelivering).
			Where(leaseCurrentSQL).
			Where("EXISTS (SELECT 1 FROM citation_profile_scopes AS s WHERE s.id = ? AND s.tenant_id = ? AND s.subject_id = ? AND s.knowledge_base_id = ? AND s.subject_epoch = ? AND s.enabled = ? AND s.deleted_at IS NULL AND s.fenced_at IS NULL AND "+currentACLWhere+")",
				append([]interface{}{claim.ScopeID, claim.TenantID, claim.SubjectID, claim.KnowledgeBaseID, claim.SubjectEpoch, true}, currentACLArgs...)...).
			Updates(map[string]interface{}{
				"attempt_count": gorm.Expr("attempt_count + 1"),
				"locked_at":     databaseNow,
				"lease_until":   leaseUntil,
				"updated_at":    databaseNow,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileOutboxLeaseLost
		}
		return tx.Where("id = ? AND locked_by = ?", claim.ID, claim.LockedBy).First(&renewed).Error
	})
	if err != nil {
		return 0, err
	}
	claim.AttemptCount = renewed.AttemptCount
	claim.LockedAt = renewed.LockedAt
	claim.LeaseUntil = renewed.LeaseUntil
	claim.UpdatedAt = renewed.UpdatedAt
	return claim.AttemptCount, nil
}

// RetryCitationProfileEventOutbox releases a still-owned lease and schedules
// another attempt. The error itself is intentionally never persisted: worker
// errors can contain IDs, credentials, SQL, or provider responses.
func (r *citationProfileRepository) RetryCitationProfileEventOutbox(
	ctx context.Context,
	claim *types.CitationProfileEventOutbox,
	now time.Time,
	nextAttemptAt time.Time,
	cause error,
) error {
	if r == nil || r.db == nil {
		return types.ErrCitationProfileUnavailable
	}
	if !validCitationProfileOutboxClaim(claim) {
		return types.ErrCitationProfileOutboxLeaseLost
	}
	callerNow := now.UTC()
	if !nextAttemptAt.IsZero() {
		nextAttemptAt = nextAttemptAt.UTC()
	}
	retryDelay := time.Duration(0)
	if !now.IsZero() && !nextAttemptAt.IsZero() && nextAttemptAt.After(callerNow) {
		retryDelay = nextAttemptAt.Sub(callerNow)
		if retryDelay > citationProfileOutboxMaxRetryDelay {
			retryDelay = citationProfileOutboxMaxRetryDelay
		}
	}
	leaseCurrentSQL, err := citationProfileColumnAfterDatabaseNowSQL("lease_until", r.db.Dialector.Name())
	if err != nil {
		return err
	}
	code, message := citationProfileOutboxFailure(cause)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		databaseNow, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		effectiveNextAttemptAt := databaseNow.Add(retryDelay)
		result := applyCitationProfileOutboxClaim(tx.Model(&types.CitationProfileEventOutbox{}), claim).
			Where("status = ? AND delivered_at IS NULL AND deadletter_at IS NULL AND lease_until IS NOT NULL", types.CitationProfileOutboxStatusDelivering).
			Where(leaseCurrentSQL).
			Updates(map[string]interface{}{
				"status":             types.CitationProfileOutboxStatusPending,
				"next_attempt_at":    effectiveNextAttemptAt,
				"locked_at":          nil,
				"lease_until":        nil,
				"locked_by":          "",
				"last_error_code":    code,
				"last_error_message": message,
				"updated_at":         databaseNow,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileOutboxLeaseLost
		}
		return nil
	})
}

// DeadletterCitationProfileEventOutbox closes work that cannot safely be
// retried (for example an expired lease budget, a deleted scope, or a
// permanently malformed event). The event and scope pending counters are
// updated in the same transaction, so a dead letter cannot leave the read
// snapshot claiming that work is still pending.
func (r *citationProfileRepository) DeadletterCitationProfileEventOutbox(
	ctx context.Context,
	claim *types.CitationProfileEventOutbox,
	now time.Time,
	cause error,
) error {
	if r == nil || r.db == nil {
		return types.ErrCitationProfileUnavailable
	}
	if !validCitationProfileOutboxClaim(claim) {
		return types.ErrCitationProfileOutboxLeaseLost
	}
	_ = now
	leaseCurrentSQL, err := citationProfileColumnAfterDatabaseNowSQL("lease_until", r.db.Dialector.Name())
	if err != nil {
		return err
	}
	code, message := citationProfileOutboxFailure(cause)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Match resolver/delete lock order even when a scope is already fenced.
		// Lease ownership is checked below; losing it rolls back all changes.
		var scope types.CitationProfileScope
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ?",
				claim.ScopeID, claim.TenantID, claim.SubjectID, claim.KnowledgeBaseID, claim.SubjectEpoch).
			First(&scope).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var event types.CitationProfileEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ?",
				claim.EventID, claim.TenantID, claim.SubjectID, claim.KnowledgeBaseID, claim.SubjectEpoch, claim.ScopeID).
			First(&event).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var owned types.CitationProfileEventOutbox
		err := applyCitationProfileOutboxClaim(tx.Clauses(clause.Locking{Strength: "UPDATE"}), claim).
			Where("status = ? AND delivered_at IS NULL AND deadletter_at IS NULL AND lease_until IS NOT NULL", types.CitationProfileOutboxStatusDelivering).
			Where(leaseCurrentSQL).
			First(&owned).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return types.ErrCitationProfileOutboxLeaseLost
		}
		if err != nil {
			return err
		}
		databaseNow, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		if event.RetractedAt == nil && event.Status == types.CitationProfileEventStatusPendingResolution && event.ActiveRunID != "" {
			// An unfinished published pointer is inconsistent. Retire it without
			// rewriting completed runs or exposing any of its partial links.
			if err := tx.Model(&types.EvidenceResolutionRun{}).
				Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND resolved_at IS NULL AND failed_at IS NULL",
					event.ActiveRunID, claim.TenantID, claim.SubjectID, claim.KnowledgeBaseID, claim.SubjectEpoch, claim.ScopeID, claim.EventID).
				Updates(map[string]interface{}{
					"status": types.CitationProfileEventStatusFailed, "failed_at": databaseNow,
					"error_code": code, "error_message": message, "updated_at": databaseNow,
				}).Error; err != nil {
				return err
			}
			if err := tx.Model(&types.EvidenceNodeLink{}).
				Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND resolution_run_id = ? AND relation_state = ?",
					claim.TenantID, claim.SubjectID, claim.KnowledgeBaseID, claim.SubjectEpoch, claim.ScopeID, claim.EventID, event.ActiveRunID, types.EvidenceRelationCurrent).
				Update("relation_state", types.EvidenceRelationHistorical).Error; err != nil {
				return err
			}
		}
		result := applyCitationProfileOutboxClaim(
			tx.Model(&types.CitationProfileEventOutbox{}),
			claim,
		).
			Where("status = ? AND delivered_at IS NULL AND deadletter_at IS NULL AND lease_until IS NOT NULL", types.CitationProfileOutboxStatusDelivering).
			Where(leaseCurrentSQL).
			Updates(map[string]interface{}{
				"status":             types.CitationProfileOutboxStatusDeadletter,
				"deadletter_at":      databaseNow,
				"locked_at":          nil,
				"lease_until":        nil,
				"locked_by":          "",
				"last_error_code":    code,
				"last_error_message": message,
				"updated_at":         databaseNow,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileOutboxLeaseLost
		}

		// Only a pending event consumes a scope slot. Retractions or a run
		// published by another idempotent worker are already terminal.
		eventResult := tx.Model(&types.CitationProfileEvent{}).
			Where(
				"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND retracted_at IS NULL AND status = ?",
				claim.EventID,
				claim.TenantID,
				claim.SubjectID,
				claim.KnowledgeBaseID,
				claim.SubjectEpoch,
				claim.ScopeID,
				types.CitationProfileEventStatusPendingResolution,
			).
			Updates(map[string]interface{}{
				"status":         types.CitationProfileEventStatusFailed,
				"failed_reason":  code,
				"active_run_id":  "",
				"resolved_at":    nil,
				"pending_reason": "",
				"updated_at":     databaseNow,
			})
		if eventResult.Error != nil {
			return eventResult.Error
		}
		if eventResult.RowsAffected != 1 {
			return nil
		}

		scopeUpdates := map[string]interface{}{
			"profile_read_version": gorm.Expr("profile_read_version + 1"),
			"pending_event_count":  gorm.Expr("CASE WHEN pending_event_count > 0 THEN pending_event_count - 1 ELSE 0 END"),
			"active_run_id":        gorm.Expr("CASE WHEN active_run_id = ? THEN '' ELSE active_run_id END", event.ActiveRunID),
			"updated_at":           databaseNow,
		}
		if event.PendingReason == "wiki_source_ref_drift" {
			scopeUpdates["dirty_mapping_count"] = gorm.Expr("CASE WHEN dirty_mapping_count > 0 THEN dirty_mapping_count - 1 ELSE 0 END")
		}
		scopeResult := tx.Model(&types.CitationProfileScope{}).
			Where(
				"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND deleted_at IS NULL AND fenced_at IS NULL",
				claim.ScopeID,
				claim.TenantID,
				claim.SubjectID,
				claim.KnowledgeBaseID,
				claim.SubjectEpoch,
			).
			Updates(scopeUpdates)
		return scopeResult.Error
	})
}

func validCitationProfileOutboxClaim(claim *types.CitationProfileEventOutbox) bool {
	return claim != nil &&
		strings.TrimSpace(claim.ID) != "" &&
		claim.TenantID != 0 &&
		strings.TrimSpace(claim.SubjectID) != "" &&
		strings.TrimSpace(claim.KnowledgeBaseID) != "" &&
		strings.TrimSpace(claim.SubjectEpoch) != "" &&
		strings.TrimSpace(claim.ScopeID) != "" &&
		strings.TrimSpace(claim.EventID) != "" &&
		claim.LockedAt != nil &&
		claim.LeaseUntil != nil &&
		claim.LeaseUntil.After(*claim.LockedAt) &&
		strings.TrimSpace(claim.LockedBy) != ""
}

func applyCitationProfileOutboxClaim(tx *gorm.DB, claim *types.CitationProfileEventOutbox) *gorm.DB {
	return tx.Where(
		"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND locked_by = ? AND locked_at = ? AND lease_until = ? AND attempt_count = ?",
		claim.ID,
		claim.TenantID,
		claim.SubjectID,
		claim.KnowledgeBaseID,
		claim.SubjectEpoch,
		claim.ScopeID,
		claim.EventID,
		claim.LockedBy,
		claim.LockedAt,
		claim.LeaseUntil,
		claim.AttemptCount,
	)
}

// lockCitationProfileOutboxForResolution is called only after the enclosing
// resolver has locked scope then event. A background worker must prove the
// exact persistent lease it received. A foreground caller may atomically take
// only due pending work or an expired/legacy lease; it can never bypass a live
// worker lease.
func lockCitationProfileOutboxForResolution(
	tx *gorm.DB,
	event *types.CitationProfileEvent,
	claim *types.CitationProfileEventOutbox,
) (*types.CitationProfileEventOutbox, bool, error) {
	if tx == nil || event == nil {
		return nil, false, types.ErrCitationProfileOutboxLeaseLost
	}
	dialect := tx.Dialector.Name()
	leaseCurrentSQL, err := citationProfileColumnAfterDatabaseNowSQL("lease_until", dialect)
	if err != nil {
		return nil, false, err
	}
	identityWhere := "id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ?"
	if claim != nil {
		if !validCitationProfileOutboxClaim(claim) ||
			claim.TenantID != event.TenantID || claim.SubjectID != event.SubjectID ||
			claim.KnowledgeBaseID != event.KnowledgeBaseID || claim.SubjectEpoch != event.SubjectEpoch ||
			claim.ScopeID != event.ScopeID || claim.EventID != event.ID {
			return nil, false, types.ErrCitationProfileOutboxLeaseLost
		}
		var owned types.CitationProfileEventOutbox
		err := applyCitationProfileOutboxClaim(tx.Clauses(clause.Locking{Strength: "UPDATE"}), claim).
			Where("status = ? AND delivered_at IS NULL AND deadletter_at IS NULL AND lease_until IS NOT NULL", types.CitationProfileOutboxStatusDelivering).
			Where(leaseCurrentSQL).
			First(&owned).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, types.ErrCitationProfileOutboxLeaseLost
		}
		return &owned, false, err
	}

	var row types.CitationProfileEventOutbox
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ?",
			event.TenantID, event.SubjectID, event.KnowledgeBaseID, event.SubjectEpoch, event.ScopeID, event.ID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Legacy/imported pending events may predate the durable outbox. The
		// resolver already owns the scope and event locks, so it can repair that
		// invariant and acquire the first attempt atomically. ON CONFLICT is
		// still required because an older writer may create the unique event row
		// without taking the event lock; in that case we reload and apply the
		// same due/live-lease checks as every other foreground resolution.
		databaseNow, clockErr := citationProfileDatabaseNow(tx)
		if clockErr != nil {
			return nil, false, clockErr
		}
		workerID := "citation-profile-foreground:" + uuid.NewString()
		leaseUntil := databaseNow.Add(citationProfileOutboxDefaultLease)
		row = types.CitationProfileEventOutbox{
			ID:              uuid.NewString(),
			TenantID:        event.TenantID,
			SubjectID:       event.SubjectID,
			KnowledgeBaseID: event.KnowledgeBaseID,
			SubjectEpoch:    event.SubjectEpoch,
			ScopeID:         event.ScopeID,
			EventID:         event.ID,
			Status:          types.CitationProfileOutboxStatusDelivering,
			AttemptCount:    1,
			NextAttemptAt:   databaseNow,
			LockedAt:        &databaseNow,
			LeaseUntil:      &leaseUntil,
			LockedBy:        workerID,
			CreatedAt:       databaseNow,
			UpdatedAt:       databaseNow,
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if created.Error != nil {
			return nil, false, created.Error
		}
		if created.RowsAffected == 1 {
			return &row, false, nil
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ?",
				event.TenantID, event.SubjectID, event.KnowledgeBaseID, event.SubjectEpoch, event.ScopeID, event.ID).
			First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, types.ErrCitationProfileOutboxLeaseLost
		}
	}
	if err != nil {
		return nil, false, err
	}
	if row.DeliveredAt != nil && row.DeadletterAt == nil && row.Status == types.CitationProfileOutboxStatusDelivered {
		return nil, true, nil
	}
	if row.DeliveredAt != nil || row.DeadletterAt != nil {
		return nil, false, types.ErrCitationProfileOutboxLeaseLost
	}
	databaseNow, err := citationProfileDatabaseNow(tx)
	if err != nil {
		return nil, false, err
	}
	if row.NextAttemptAt.After(databaseNow) {
		return nil, false, types.ErrCitationProfileOutboxLeaseLost
	}
	switch row.Status {
	case types.CitationProfileOutboxStatusPending:
	case types.CitationProfileOutboxStatusDelivering:
		if row.LeaseUntil != nil && row.LeaseUntil.After(databaseNow) {
			return nil, false, types.ErrCitationProfileOutboxLeaseLost
		}
	default:
		return nil, false, types.ErrCitationProfileOutboxLeaseLost
	}

	workerID := "citation-profile-foreground:" + uuid.NewString()
	leaseUntil := databaseNow.Add(citationProfileOutboxDefaultLease)
	update := tx.Model(&types.CitationProfileEventOutbox{}).
		Where(identityWhere, row.ID, row.TenantID, row.SubjectID, row.KnowledgeBaseID, row.SubjectEpoch, row.ScopeID, row.EventID).
		Where("status = ? AND locked_by = ? AND attempt_count = ? AND delivered_at IS NULL AND deadletter_at IS NULL",
			row.Status, row.LockedBy, row.AttemptCount)
	if row.LockedAt == nil {
		update = update.Where("locked_at IS NULL")
	} else {
		update = update.Where("locked_at = ?", row.LockedAt)
	}
	if row.LeaseUntil == nil {
		update = update.Where("lease_until IS NULL")
	} else {
		update = update.Where("lease_until = ?", row.LeaseUntil)
	}
	result := update.
		Updates(map[string]interface{}{
			"status":        types.CitationProfileOutboxStatusDelivering,
			"attempt_count": gorm.Expr("attempt_count + 1"),
			"locked_at":     databaseNow,
			"lease_until":   leaseUntil,
			"locked_by":     workerID,
			"updated_at":    databaseNow,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, false, types.ErrCitationProfileOutboxLeaseLost
	}
	var owned types.CitationProfileEventOutbox
	if err := tx.Where("id = ? AND locked_by = ?", row.ID, workerID).First(&owned).Error; err != nil {
		return nil, false, err
	}
	return &owned, false, nil
}

func citationProfileOutboxFailure(cause error) (string, string) {
	switch {
	case errors.Is(cause, types.ErrCitationProfileOutboxExpired):
		return types.CitationProfileOutboxErrorExpired, types.CitationProfileOutboxMessageExpired
	case errors.Is(cause, types.ErrCitationProfileOutboxMaxAttempts):
		return types.CitationProfileOutboxErrorMaxAttempts, types.CitationProfileOutboxMessageMaxAttempts
	case errors.Is(cause, types.ErrCitationProfileDeleted):
		return types.CitationProfileOutboxErrorScopeDeleted, types.CitationProfileOutboxMessageScopeDeleted
	default:
		return types.CitationProfileOutboxErrorResolutionFailed, types.CitationProfileOutboxMessageResolutionFailed
	}
}

// terminateCitationProfileWorkForDelete keeps the durable audit rows but
// closes every not-yet-delivered recovery item when its scope is fenced. This
// runs inside the same transaction as the scope fence, so a worker cannot
// publish evidence after an accepted delete. The predicates are fully scoped
// and never touch another subject or knowledge base.
func terminateCitationProfileWorkForDelete(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
	now time.Time,
) error {
	if tx == nil || scope == nil {
		return nil
	}
	now = now.UTC()
	if result := tx.Model(&types.CitationProfileEvent{}).
		Where(
			"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND retracted_at IS NULL AND COALESCE(active_run_id, '') = '' AND status = ?",
			scope.TenantID,
			scope.SubjectID,
			scope.KnowledgeBaseID,
			scope.SubjectEpoch,
			scope.ID,
			types.CitationProfileEventStatusPendingResolution,
		).
		Updates(map[string]interface{}{
			"status":        types.CitationProfileEventStatusFailed,
			"failed_reason": types.CitationProfileOutboxErrorScopeDeleted,
			"updated_at":    now,
		}); result.Error != nil {
		return result.Error
	}
	if result := tx.Model(&types.CitationProfileEventOutbox{}).
		Where(
			"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND delivered_at IS NULL AND deadletter_at IS NULL",
			scope.TenantID,
			scope.SubjectID,
			scope.KnowledgeBaseID,
			scope.SubjectEpoch,
			scope.ID,
		).
		Updates(map[string]interface{}{
			"status":             types.CitationProfileOutboxStatusDeadletter,
			"deadletter_at":      now,
			"locked_at":          nil,
			"lease_until":        nil,
			"locked_by":          "",
			"last_error_code":    types.CitationProfileOutboxErrorScopeDeleted,
			"last_error_message": types.CitationProfileOutboxMessageScopeDeleted,
			"updated_at":         now,
		}); result.Error != nil {
		return result.Error
	}

	return nil
}

func markCitationEventOutboxDelivered(
	tx *gorm.DB,
	event *types.CitationProfileEvent,
	claim *types.CitationProfileEventOutbox,
	at time.Time,
) error {
	if tx == nil || event == nil || !validCitationProfileOutboxClaim(claim) ||
		claim.TenantID != event.TenantID || claim.SubjectID != event.SubjectID ||
		claim.KnowledgeBaseID != event.KnowledgeBaseID || claim.SubjectEpoch != event.SubjectEpoch ||
		claim.ScopeID != event.ScopeID || claim.EventID != event.ID {
		return types.ErrCitationProfileOutboxLeaseLost
	}
	leaseCurrentSQL, err := citationProfileColumnAfterDatabaseNowSQL("lease_until", tx.Dialector.Name())
	if err != nil {
		return err
	}
	result := applyCitationProfileOutboxClaim(tx.Model(&types.CitationProfileEventOutbox{}), claim).
		Where("status = ? AND delivered_at IS NULL AND deadletter_at IS NULL AND lease_until IS NOT NULL", types.CitationProfileOutboxStatusDelivering).
		Where(leaseCurrentSQL).
		Updates(map[string]interface{}{
			"status":       types.CitationProfileOutboxStatusDelivered,
			"delivered_at": at.UTC(),
			"locked_at":    nil,
			"lease_until":  nil,
			"locked_by":    "",
			"updated_at":   at.UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return types.ErrCitationProfileOutboxLeaseLost
	}
	return nil
}
