package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	citationProfileMinimumWorkerLease     = time.Second
	citationProfileMaximumWorkerLease     = 5 * time.Minute
	citationProfileACLMaximumAllowTTL     = 5 * time.Minute
	citationProfileACLLeaseTokenMaxLength = 128
)

// newCitationProfileACLLeaseToken creates a storage-safe opaque fencing token.
// The bounded worker fingerprint preserves enough diagnostic correlation without
// exposing or trusting an arbitrarily long caller-provided worker identity; the
// random UUID keeps every individual claim independently fenced.
func newCitationProfileACLLeaseToken(workerID string) string {
	workerFingerprint := sha256.Sum256([]byte(workerID))
	return fmt.Sprintf("cpacl:%x:%s", workerFingerprint[:8], uuid.NewString())
}

// citationProfileDatabaseNow returns the database server's statement clock,
// never an application-node wall clock. Epoch seconds keep the scan contract
// identical across PostgreSQL and SQLite.
func citationProfileDatabaseNow(tx *gorm.DB) (time.Time, error) {
	if tx == nil || tx.Dialector == nil {
		return time.Time{}, errors.New("citation profile database clock requires database")
	}
	var query string
	switch strings.ToLower(strings.TrimSpace(tx.Dialector.Name())) {
	case "postgres":
		query = "SELECT EXTRACT(EPOCH FROM statement_timestamp())::double precision"
	case "sqlite":
		query = "SELECT (julianday('now') - 2440587.5) * 86400.0"
	default:
		return time.Time{}, fmt.Errorf("citation profile database clock: unsupported database dialect %q", tx.Dialector.Name())
	}
	var epoch float64
	if err := tx.Raw(query).Row().Scan(&epoch); err != nil {
		return time.Time{}, fmt.Errorf("read citation profile database clock: %w", err)
	}
	if math.IsNaN(epoch) || math.IsInf(epoch, 0) {
		return time.Time{}, errors.New("read citation profile database clock: invalid epoch")
	}
	seconds, fraction := math.Modf(epoch)
	nanoseconds := int64(math.Round(fraction * float64(time.Second)))
	return time.Unix(int64(seconds), nanoseconds).UTC(), nil
}

func citationProfileBoundedWorkerLease(requested time.Duration) time.Duration {
	if requested <= 0 {
		return citationProfileOutboxDefaultLease
	}
	if requested < citationProfileMinimumWorkerLease {
		return citationProfileMinimumWorkerLease
	}
	if requested > citationProfileMaximumWorkerLease {
		return citationProfileMaximumWorkerLease
	}
	return requested
}

func citationProfileColumnAfterDatabaseNowSQL(column, dialect string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres":
		return column + " > statement_timestamp()", nil
	case "sqlite":
		return "julianday(" + column + ") > julianday('now')", nil
	default:
		return "", fmt.Errorf("citation profile database clock: unsupported database dialect %q", dialect)
	}
}

func citationProfileColumnDueAtDatabaseNowSQL(column, dialect string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres":
		return column + " <= statement_timestamp()", nil
	case "sqlite":
		return "julianday(" + column + ") <= julianday('now')", nil
	default:
		return "", fmt.Errorf("citation profile database clock: unsupported database dialect %q", dialect)
	}
}

// citationProfileACLInvalidationGate keeps stable-disabled deployments away
// from scope/outbox writes while still making a mixed enable rollout safe.
// A locally disabled replica takes a shared lock on the cluster feature-state
// row: false means no scope SQL; true means it must invalidate just like a new
// replica. The lock serializes permission commits with the exclusive enable
// transition, closing the old-off-writer race without a polling window.
type citationProfileACLInvalidationGate struct {
	enabled bool
}

func newCitationProfileACLInvalidationGate(config *types.CitationProfileConfig) citationProfileACLInvalidationGate {
	return citationProfileACLInvalidationGate{enabled: config != nil && config.Enabled}
}

func (g citationProfileACLInvalidationGate) invalidate(
	tx *gorm.DB,
	mutation types.CitationProfileACLMutation,
	now time.Time,
) error {
	active, err := g.activeForMutation(tx)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	return invalidateCitationProfileACLTx(tx, mutation, now)
}

func (g citationProfileACLInvalidationGate) invalidateMany(
	tx *gorm.DB,
	mutations []types.CitationProfileACLMutation,
	now time.Time,
) error {
	if len(mutations) == 0 {
		return nil
	}
	active, err := g.activeForMutation(tx)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	return invalidateCitationProfileACLMutationsTx(tx, mutations, now)
}

func (g citationProfileACLInvalidationGate) invalidateTenantDeletion(
	tx *gorm.DB,
	tenantID uint64,
	now time.Time,
) error {
	active, err := g.activeForMutation(tx)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}
	return invalidateCitationProfileACLForTenantDeletionTx(tx, tenantID, now)
}

func (g citationProfileACLInvalidationGate) activeForMutation(tx *gorm.DB) (bool, error) {
	if g.enabled {
		return true, nil
	}
	if tx == nil {
		return false, errors.New("citation profile ACL feature-state check requires database transaction")
	}
	var state types.CitationProfileACLRuntimeState
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Select("id", "enabled").First(&state, "id = ?", 1).Error; err != nil {
		return false, fmt.Errorf("lock citation profile ACL runtime state for permission mutation: %w", err)
	}
	return state.Enabled, nil
}

// NewCitationProfileACLRepository exposes the authority-refresh queue without
// widening the request-path repository interface.
func NewCitationProfileACLRepository(db *gorm.DB) interfaces.CitationProfileACLRepository {
	return &citationProfileRepository{db: db}
}

// PrepareCitationProfileACLFeatureState serializes the first enabled transition
// through a singleton database row. The marker is deliberately monotonic:
// after any replica enables the feature, a locally disabled or rollback-config
// replica must keep permission invalidation active for enabled peers. Clearing
// it safely requires a separate operator-coordinated fleet decommission after
// all enabled replicas are drained; process startup must never infer that.
func (r *citationProfileRepository) PrepareCitationProfileACLFeatureState(ctx context.Context, enabled bool) error {
	if r == nil || r.db == nil {
		return errors.New("citation profile ACL repository requires database")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state types.CitationProfileACLRuntimeState
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&state, "id = ?", 1).Error; err != nil {
			return fmt.Errorf("lock citation profile ACL runtime state: %w", err)
		}
		if state.Enabled || !enabled {
			return nil
		}
		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", true).
			Where(
				"COALESCE(acl_check_state, '') <> ? OR acl_checked_at IS NOT NULL OR acl_check_lease_token <> '' OR acl_check_lease_until IS NOT NULL",
				types.CitationProfileACLStateUnknown,
			)
		if err := invalidateCitationProfileACLQueryTx(tx, query, now); err != nil {
			return err
		}
		result := tx.Model(&types.CitationProfileACLRuntimeState{}).
			Where("id = ? AND enabled = ? AND transition_generation = ?", state.ID, state.Enabled, state.TransitionGeneration).
			Updates(map[string]interface{}{
				"enabled":               true,
				"transition_generation": state.TransitionGeneration + 1,
				"changed_at":            now,
				"updated_at":            now,
			})
		if result.Error != nil {
			return fmt.Errorf("advance citation profile ACL runtime state: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return errors.New("advance citation profile ACL runtime state: transition lost")
		}
		return nil
	})
}

// FenceCitationProfileACLForStartup synchronously quarantines every live scope
// that could still carry an authority grant from an earlier process lifetime.
// This closes feature-off -> permission-revocation -> feature-on gaps before
// HTTP serving begins. Already-UNKNOWN, unleased scopes are left untouched so
// concurrent rolling starts converge instead of repeatedly advancing them.
func (r *citationProfileRepository) FenceCitationProfileACLForStartup(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("citation profile ACL repository requires database")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", true).
			Where(
				"COALESCE(acl_check_state, '') <> ? OR acl_checked_at IS NOT NULL OR acl_check_lease_token <> '' OR acl_check_lease_until IS NOT NULL",
				types.CitationProfileACLStateUnknown,
			)
		return invalidateCitationProfileACLQueryTx(tx, query, now)
	})
}

// InvalidateCitationProfileACL fences any in-flight authority result before a
// permission mutation can leave a cached ALLOW current. Callers supply the
// narrowest stable grant identity they know; omitted fields intentionally act
// as wildcards so broad changes such as API-key revocation can invalidate all
// knowledge bases covered by that key.
func (r *citationProfileRepository) InvalidateCitationProfileACL(
	ctx context.Context,
	mutation types.CitationProfileACLMutation,
) error {
	if r == nil || r.db == nil {
		return errors.New("citation profile ACL repository requires database")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return invalidateCitationProfileACLTx(tx, mutation, time.Time{})
	})
}

func invalidateCitationProfileACLTx(tx *gorm.DB, mutation types.CitationProfileACLMutation, now time.Time) error {
	return invalidateCitationProfileACLMutationsTx(tx, []types.CitationProfileACLMutation{mutation}, now)
}

func invalidateCitationProfileACLMutationsTx(
	tx *gorm.DB,
	mutations []types.CitationProfileACLMutation,
	now time.Time,
) error {
	if tx == nil {
		return errors.New("citation profile ACL invalidation requires database transaction")
	}
	if len(mutations) == 0 {
		return fmt.Errorf("%w: ACL invalidation requires at least one identity field", types.ErrCitationProfileInvalidRequest)
	}
	// Permission repositories historically pass their process clock here. It
	// cannot schedule an authority refresh or pause a retry budget safely.
	_ = now
	databaseNow, err := citationProfileDatabaseNow(tx)
	if err != nil {
		return err
	}
	now = databaseNow
	predicates := make([]string, 0, len(mutations))
	args := make([]interface{}, 0, len(mutations)*8)
	for i := range mutations {
		mutation := mutations[i]
		mutation.PrincipalType = strings.TrimSpace(mutation.PrincipalType)
		mutation.PrincipalID = strings.TrimSpace(mutation.PrincipalID)
		mutation.KnowledgeBaseID = strings.TrimSpace(mutation.KnowledgeBaseID)
		mutation.AccessPath = strings.TrimSpace(mutation.AccessPath)
		mutation.AccessPathID = strings.TrimSpace(mutation.AccessPathID)
		if mutation.PrincipalType == "" && mutation.PrincipalID == "" &&
			mutation.AuthenticatedTenantID == 0 && mutation.APIKeyID == 0 &&
			mutation.SourceTenantID == 0 && mutation.KnowledgeBaseID == "" &&
			mutation.AccessPath == "" && mutation.AccessPathID == "" {
			return fmt.Errorf("%w: ACL invalidation requires at least one identity field", types.ErrCitationProfileInvalidRequest)
		}
		parts := make([]string, 0, 8)
		if mutation.PrincipalType != "" {
			parts = append(parts, "acl_principal_type = ?")
			args = append(args, mutation.PrincipalType)
		}
		if mutation.PrincipalID != "" {
			parts = append(parts, "acl_principal_id = ?")
			args = append(args, mutation.PrincipalID)
		}
		if mutation.AuthenticatedTenantID != 0 {
			parts = append(parts, "acl_authenticated_tenant_id = ?")
			args = append(args, mutation.AuthenticatedTenantID)
		}
		if mutation.APIKeyID != 0 {
			parts = append(parts, "acl_api_key_id = ?")
			args = append(args, mutation.APIKeyID)
		}
		if mutation.SourceTenantID != 0 {
			parts = append(parts, "tenant_id = ?")
			args = append(args, mutation.SourceTenantID)
		}
		if mutation.KnowledgeBaseID != "" {
			parts = append(parts, "knowledge_base_id = ?")
			args = append(args, mutation.KnowledgeBaseID)
		}
		if mutation.AccessPath != "" {
			parts = append(parts, "acl_access_path = ?")
			args = append(args, mutation.AccessPath)
		}
		if mutation.AccessPathID != "" {
			parts = append(parts, "acl_access_path_id = ?")
			args = append(args, mutation.AccessPathID)
		}
		predicates = append(predicates, "("+strings.Join(parts, " AND ")+")")
	}

	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", true).
		Where(strings.Join(predicates, " OR "), args...)
	return invalidateCitationProfileACLQueryTx(tx, query, now)
}

func invalidateCitationProfileACLForTenantDeletionTx(tx *gorm.DB, tenantID uint64, now time.Time) error {
	if tenantID == 0 {
		return fmt.Errorf("%w: tenant ACL invalidation requires tenant", types.ErrCitationProfileInvalidRequest)
	}
	return invalidateCitationProfileACLMutationsTx(tx, []types.CitationProfileACLMutation{
		{SourceTenantID: tenantID},
		{AuthenticatedTenantID: tenantID},
	}, now.UTC())
}

func invalidateCitationProfileACLQueryTx(tx *gorm.DB, query *gorm.DB, now time.Time) error {
	var scopes []types.CitationProfileScope
	if err := query.Order("tenant_id ASC").Order("id ASC").Find(&scopes).Error; err != nil {
		return err
	}
	for i := range scopes {
		scope := &scopes[i]
		pauseStartedAt := citationProfileACLRetryPauseStart(scope, now)
		result := tx.Model(&types.CitationProfileScope{}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ?", scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch).
			Where("acl_generation = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", scope.ACLGeneration, true).
			Updates(map[string]interface{}{
				"acl_generation":        scope.ACLGeneration + 1,
				"acl_check_state":       types.CitationProfileACLStateUnknown,
				"acl_checked_at":        nil,
				"next_acl_check_at":     now,
				"acl_check_lease_token": "",
				"acl_check_lease_until": nil,
				"profile_read_version":  scope.ProfileReadVersion + 1,
				"updated_at":            now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileACLLeaseLost
		}
		if err := pauseCitationProfileOutboxRetryBudget(tx, scope, pauseStartedAt, now); err != nil {
			return err
		}
	}
	return nil
}

func (r *citationProfileRepository) ClaimCitationProfileACLScopes(
	ctx context.Context,
	workerID string,
	limit int,
	now time.Time,
	lease time.Duration,
) ([]types.CitationProfileACLClaim, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("citation profile ACL repository requires database")
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || limit <= 0 || lease <= 0 {
		return nil, fmt.Errorf("%w: ACL claim requires worker, limit and lease", types.ErrCitationProfileInvalidRequest)
	}
	// now is retained for interface compatibility and deterministic runner
	// telemetry only. Lease eligibility and deadlines are database-clock facts.
	_ = now
	lease = citationProfileBoundedWorkerLease(lease)
	dialect := r.db.Dialector.Name()
	nextCheckDueSQL, err := citationProfileColumnDueAtDatabaseNowSQL("next_acl_check_at", dialect)
	if err != nil {
		return nil, err
	}
	leaseDueSQL, err := citationProfileColumnDueAtDatabaseNowSQL("acl_check_lease_until", dialect)
	if err != nil {
		return nil, err
	}
	claims := make([]types.CitationProfileACLClaim, 0, limit)
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []types.CitationProfileScope
		attemptedIDs := make([]string, 0, limit*2)
		for len(candidates) == 0 {
			var fairIDs []string
			fairQuery := tx.Model(&types.CitationProfileScope{}).
				Where("enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", true).
				Where("(next_acl_check_at IS NULL OR " + nextCheckDueSQL + ")").
				Where("(acl_check_lease_until IS NULL OR " + leaseDueSQL + ")")
			if len(attemptedIDs) > 0 {
				fairQuery = fairQuery.Where("id NOT IN ?", attemptedIDs)
			}
			if err := fairQuery.
				Order("CASE WHEN next_acl_check_at IS NULL THEN 0 ELSE 1 END ASC").
				Order("next_acl_check_at ASC").
				Order("tenant_id ASC").
				Order("id ASC").
				Limit(limit).
				Pluck("id", &fairIDs).Error; err != nil {
				return err
			}
			if len(fairIDs) == 0 {
				break
			}
			attemptedIDs = append(attemptedIDs, fairIDs...)
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Where("id IN ?", fairIDs).
				Where("enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", true).
				Where("(next_acl_check_at IS NULL OR " + nextCheckDueSQL + ")").
				Where("(acl_check_lease_until IS NULL OR " + leaseDueSQL + ")").
				Order("tenant_id ASC").
				Order("id ASC").
				Find(&candidates).Error; err != nil {
				return err
			}
		}
		for i := range candidates {
			scope := &candidates[i]
			leaseStartedAt, err := citationProfileDatabaseNow(tx)
			if err != nil {
				return err
			}
			leaseUntil := leaseStartedAt.Add(lease)
			pauseStartedAt := citationProfileACLRetryPauseStart(scope, leaseStartedAt)
			leaseToken := newCitationProfileACLLeaseToken(workerID)
			nextReadVersion := scope.ProfileReadVersion
			declaredState := strings.TrimSpace(scope.ACLCheckState)
			if declaredState == "" || declaredState == types.CitationProfileACLStateCurrent {
				nextReadVersion++
			}
			result := tx.Model(&types.CitationProfileScope{}).
				Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ?", scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch).
				Where("acl_generation = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", scope.ACLGeneration, true).
				Where("(next_acl_check_at IS NULL OR " + nextCheckDueSQL + ")").
				Where("(acl_check_lease_until IS NULL OR " + leaseDueSQL + ")").
				Updates(map[string]interface{}{
					"acl_check_state":       types.CitationProfileACLStateUnknown,
					"acl_checked_at":        nil,
					"acl_check_lease_token": leaseToken,
					"acl_check_lease_until": leaseUntil,
					"profile_read_version":  nextReadVersion,
					"updated_at":            leaseStartedAt,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				continue
			}
			var stored types.CitationProfileScope
			if err := tx.Where(
				"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND acl_check_lease_token = ?",
				scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, leaseToken,
			).First(&stored).Error; err != nil {
				return err
			}
			if err := pauseCitationProfileOutboxRetryBudget(tx, &stored, pauseStartedAt, leaseStartedAt); err != nil {
				return err
			}
			claims = append(claims, types.CitationProfileACLClaim{
				ScopeID:    stored.ID,
				LeaseToken: leaseToken,
				Generation: stored.ACLGeneration,
				Scope:      stored,
			})
		}
		return nil
	})
	return claims, err
}

func (r *citationProfileRepository) ApplyCitationProfileACLResult(
	ctx context.Context,
	claim *types.CitationProfileACLClaim,
	decision types.CitationProfileACLDecision,
	now time.Time,
	nextCheckAt time.Time,
) error {
	if r == nil || r.db == nil {
		return errors.New("citation profile ACL repository requires database")
	}
	if claim == nil || strings.TrimSpace(claim.ScopeID) == "" || strings.TrimSpace(claim.LeaseToken) == "" {
		return fmt.Errorf("%w: ACL result requires a lease", types.ErrCitationProfileInvalidRequest)
	}
	if !validCitationProfileACLDecision(decision) {
		return fmt.Errorf("%w: unsupported ACL decision %q", types.ErrCitationProfileInvalidRequest, decision)
	}
	callerNow := now.UTC()
	if !nextCheckAt.IsZero() {
		nextCheckAt = nextCheckAt.UTC()
	}
	dialect := r.db.Dialector.Name()
	leaseCurrentSQL, err := citationProfileColumnAfterDatabaseNowSQL("acl_check_lease_until", dialect)
	if err != nil {
		return err
	}
	deadlineCurrentSQL, err := citationProfileColumnAfterDatabaseNowSQL("?", dialect)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var scope types.CitationProfileScope
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ?", claim.ScopeID, claim.Scope.TenantID, claim.Scope.SubjectID, claim.Scope.KnowledgeBaseID, claim.Scope.SubjectEpoch).
			Where("acl_check_lease_token = ? AND acl_generation = ? AND acl_check_lease_until IS NOT NULL AND "+leaseCurrentSQL+" AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL", claim.LeaseToken, claim.Generation, true).
			First(&scope).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return types.ErrCitationProfileACLLeaseLost
		}
		if err != nil {
			return err
		}
		databaseNow, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}

		effectiveNextCheckAt := nextCheckAt
		if decision == types.CitationProfileACLDecisionAllow {
			maximumDeadline := databaseNow.Add(citationProfileACLMaximumAllowTTL)
			if effectiveNextCheckAt.IsZero() {
				effectiveNextCheckAt = maximumDeadline
			} else if !effectiveNextCheckAt.After(databaseNow) {
				return fmt.Errorf("%w: ACL ALLOW requires a future database-time validity deadline", types.ErrCitationProfileInvalidRequest)
			}
			if effectiveNextCheckAt.After(maximumDeadline) {
				effectiveNextCheckAt = maximumDeadline
			}
		} else if decision != types.CitationProfileACLDecisionDeny {
			retryDelay := time.Duration(0)
			if !callerNow.IsZero() && !nextCheckAt.IsZero() && nextCheckAt.After(callerNow) {
				retryDelay = nextCheckAt.Sub(callerNow)
				if retryDelay > citationProfileACLMaximumAllowTTL {
					retryDelay = citationProfileACLMaximumAllowTTL
				}
			}
			effectiveNextCheckAt = databaseNow.Add(retryDelay)
		}

		if decision == types.CitationProfileACLDecisionDeny {
			return applyCitationProfileACLDeny(tx, &scope, claim, databaseNow, leaseCurrentSQL)
		}

		state := string(decision)
		if decision == types.CitationProfileACLDecisionAllow {
			state = types.CitationProfileACLStateCurrent
		}
		nextVersion := scope.ProfileReadVersion
		if state == types.CitationProfileACLStateCurrent {
			nextVersion++
		}
		update := tx.Model(&types.CitationProfileScope{}).
			Where("id = ? AND acl_check_lease_token = ? AND acl_generation = ? AND acl_check_lease_until IS NOT NULL", scope.ID, claim.LeaseToken, claim.Generation).
			Where(leaseCurrentSQL)
		if decision == types.CitationProfileACLDecisionAllow {
			update = update.Where(deadlineCurrentSQL, effectiveNextCheckAt)
		}
		result := update.
			Updates(map[string]interface{}{
				"acl_check_state":       state,
				"acl_checked_at":        databaseNow,
				"next_acl_check_at":     effectiveNextCheckAt,
				"acl_check_lease_token": "",
				"acl_check_lease_until": nil,
				"profile_read_version":  nextVersion,
				"updated_at":            databaseNow,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileACLLeaseLost
		}
		if decision == types.CitationProfileACLDecisionAllow {
			return resumeCitationProfileOutboxRetryBudget(tx, &scope, databaseNow)
		}
		return pauseCitationProfileOutboxRetryBudget(tx, &scope, databaseNow, databaseNow)
	})
}

func validCitationProfileACLDecision(decision types.CitationProfileACLDecision) bool {
	switch decision {
	case types.CitationProfileACLDecisionAllow,
		types.CitationProfileACLDecisionDeny,
		types.CitationProfileACLDecisionUnknown,
		types.CitationProfileACLDecisionError,
		types.CitationProfileACLDecisionTimeout:
		return true
	default:
		return false
	}
}

func applyCitationProfileACLDeny(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
	claim *types.CitationProfileACLClaim,
	now time.Time,
	leaseCurrentSQL string,
) error {
	result := tx.Model(&types.CitationProfileScope{}).
		Where("id = ? AND acl_check_lease_token = ? AND acl_generation = ? AND acl_check_lease_until IS NOT NULL AND deleted_at IS NULL AND fenced_at IS NULL", scope.ID, claim.LeaseToken, claim.Generation).
		Where(leaseCurrentSQL).
		Updates(map[string]interface{}{
			"enabled":               false,
			"acl_check_state":       types.CitationProfileACLStateDenied,
			"acl_checked_at":        now,
			"next_acl_check_at":     nil,
			"acl_check_lease_token": "",
			"acl_check_lease_until": nil,
			"fenced_at":             now,
			"fence_reason":          types.CitationProfileFenceReasonACLDenied,
			"profile_read_version":  scope.ProfileReadVersion + 1,
			"pending_event_count":   0,
			"pending_mapping_count": 0,
			"dirty_mapping_count":   0,
			"updated_at":            now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return types.ErrCitationProfileACLLeaseLost
	}
	if err := terminateCitationProfileWorkForDelete(tx, scope, now); err != nil {
		return err
	}
	return revokeCitationProfileExportsForScope(tx, scope, now)
}

func citationProfileACLRetryPauseStart(scope *types.CitationProfileScope, now time.Time) time.Time {
	now = now.UTC()
	if scope == nil || strings.TrimSpace(scope.ACLCheckState) != types.CitationProfileACLStateCurrent ||
		scope.NextACLCheckAt == nil {
		return now
	}
	expiresAt := scope.NextACLCheckAt.UTC()
	if expiresAt.After(now) {
		return now
	}
	return expiresAt
}

func pauseCitationProfileOutboxRetryBudget(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
	pauseStartedAt time.Time,
	now time.Time,
) error {
	now = now.UTC()
	pauseStartedAt = pauseStartedAt.UTC()
	if pauseStartedAt.IsZero() || pauseStartedAt.After(now) {
		pauseStartedAt = now
	}
	var rows []types.CitationProfileEventOutbox
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ?", scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID).
		Where("delivered_at IS NULL AND deadletter_at IS NULL AND retry_budget_paused_at IS NULL").
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		rowPauseStartedAt := pauseStartedAt
		if rows[i].CreatedAt.After(rowPauseStartedAt) {
			rowPauseStartedAt = rows[i].CreatedAt.UTC()
		}
		if rowPauseStartedAt.After(now) {
			rowPauseStartedAt = now
		}
		result := tx.Model(&types.CitationProfileEventOutbox{}).
			Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ?", rows[i].ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID).
			Where("delivered_at IS NULL AND deadletter_at IS NULL AND retry_budget_paused_at IS NULL").
			Updates(map[string]interface{}{
				"retry_budget_paused_at": rowPauseStartedAt,
				"status":                 types.CitationProfileOutboxStatusPending,
				"locked_at":              nil,
				"lease_until":            nil,
				"locked_by":              "",
				"updated_at":             now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return types.ErrCitationProfileChanged
		}
	}
	return nil
}

func resumeCitationProfileOutboxRetryBudget(tx *gorm.DB, scope *types.CitationProfileScope, now time.Time) error {
	var rows []types.CitationProfileEventOutbox
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ?", scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ID).
		Where("delivered_at IS NULL AND deadletter_at IS NULL AND retry_budget_paused_at IS NOT NULL").
		Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		pausedFor := int64(0)
		if rows[i].RetryBudgetPausedAt != nil && now.After(*rows[i].RetryBudgetPausedAt) {
			pausedFor = int64(now.Sub(*rows[i].RetryBudgetPausedAt) / time.Second)
		}
		if err := tx.Model(&types.CitationProfileEventOutbox{}).
			Where("id = ? AND retry_budget_paused_at = ? AND delivered_at IS NULL AND deadletter_at IS NULL", rows[i].ID, rows[i].RetryBudgetPausedAt).
			Updates(map[string]interface{}{
				"retry_budget_paused_at":      nil,
				"retry_budget_paused_seconds": rows[i].RetryBudgetPausedSeconds + pausedFor,
				"updated_at":                  now,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}
