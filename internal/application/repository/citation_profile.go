package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

var errCitationProfileBlindDeleteRace = errors.New("citation profile blind delete raced with a terminal scope change")

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
	currentACLWhere, currentACLArgs, err := citationProfileACLCurrentDatabaseSQL(
		"citation_profile_scopes",
		r.db.Dialector.Name(),
	)
	if err != nil {
		return nil, err
	}
	selectArgs := make([]interface{}, 0, len(currentACLArgs)+1)
	selectArgs = append(selectArgs, true)
	selectArgs = append(selectArgs, currentACLArgs...)
	projection := "citation_profile_scopes.*, CASE WHEN citation_profile_scopes.enabled = ? " +
		"AND citation_profile_scopes.deleted_at IS NULL AND citation_profile_scopes.fenced_at IS NULL AND " +
		currentACLWhere + " THEN TRUE ELSE FALSE END AS acl_current"

	var scope types.CitationProfileScope
	err = r.db.WithContext(ctx).
		Select(projection, selectArgs...).
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
	var aclBinding types.CitationProfileACLBinding
	if enabled {
		var ok bool
		aclBinding, ok = types.CitationProfileACLBindingFromContext(ctx)
		if !ok || !citationProfileACLBindingMatchesContext(ctx, tenantID, subjectID, kbID, aclBinding) {
			return nil, fmt.Errorf("%w: enrollment requires server-derived ACL binding", types.ErrCitationProfileUnavailable)
		}
	}

	expectedText := ""
	if expectedReadVersion != nil {
		expectedText = fmt.Sprintf("%d", *expectedReadVersion)
	}
	requestDigest := citationRequestDigest(types.CitationOperationEnrollment, kbID, fmt.Sprintf("%t", enabled), expectedText)

	var resolved *types.CitationProfileScope
	var verifiedExistingScope *types.CitationProfileScope
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, err := r.loadLiveCitationScopeForUpdate(tx, tenantID, subjectID, kbID)
		if err != nil {
			return err
		}
		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		retiredFencedScope := false

		if scope != nil {
			if scope.FencedAt != nil {
				if !enabled {
					return types.ErrCitationProfileDeleted
				}
				if expectedReadVersion != nil && *expectedReadVersion != 0 && scope.ProfileReadVersion != *expectedReadVersion {
					return types.ErrCitationProfileChanged
				}
				if err := r.retireCitationProfileFencedScopeForReenrollment(tx, scope, now); err != nil {
					return err
				}
				scope = nil
				retiredFencedScope = true
			}
		}

		if scope != nil {
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
				if replayed.ACLCheckState == types.CitationProfileACLStateCurrent {
					if err := r.ensureCitationProfileACLCurrentTx(tx, replayed); err != nil {
						return err
					}
					snapshot := *replayed
					verifiedExistingScope = &snapshot
				}
				return nil
			}
			if expectedReadVersion != nil && scope.ProfileReadVersion != *expectedReadVersion {
				return types.ErrCitationProfileChanged
			}
			if enabled && !citationProfileScopeACLBindingEqual(scope, aclBinding) {
				if err := r.refreshCitationProfileACLBinding(tx, scope, aclBinding, now); err != nil {
					return err
				}
			}
			if !enabled {
				resultSummary, err := citationProfileOperationResult(scope, map[string]interface{}{
					"enabled":      false,
					"receipt_code": types.CitationProfileReceiptHiddenAndFenced,
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
				if scope.ACLCheckState == types.CitationProfileACLStateCurrent {
					if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
						return err
					}
					snapshot := *scope
					verifiedExistingScope = &snapshot
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
			}

			if err := r.fenceCitationProfileScopeForDelete(tx, scope, "", types.CitationOperationEnrollment, expectedReadVersion, now); err != nil {
				return err
			}
		}

		if expectedReadVersion != nil && *expectedReadVersion != 0 && !retiredFencedScope {
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
			ID:                       uuid.NewString(),
			TenantID:                 tenantID,
			SubjectID:                subjectID,
			KnowledgeBaseID:          kbID,
			SubjectEpoch:             uuid.NewString(),
			ProfileReadVersion:       1,
			ProfilePolicyVersion:     types.CitationProfilePolicyVersion,
			RetentionPolicyVersion:   types.CitationProfileRetentionPolicyVersion,
			Enabled:                  true,
			ACLCheckState:            types.CitationProfileACLStateUnknown,
			NextACLCheckAt:           citationTimePointer(now),
			ACLPrincipalType:         aclBinding.PrincipalType,
			ACLPrincipalID:           aclBinding.PrincipalID,
			ACLAuthenticatedTenantID: aclBinding.AuthenticatedTenantID,
			ACLAPIKeyID:              aclBinding.APIKeyID,
			ACLAccessPath:            aclBinding.AccessPath,
			ACLAccessPathID:          aclBinding.AccessPathID,
			ACLGeneration:            1,
			CreatedAt:                now,
			UpdatedAt:                now,
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
	if err != nil {
		return nil, err
	}
	if verifiedExistingScope != nil {
		if err := r.verifyCitationProfileReadSnapshotFresh(ctx, verifiedExistingScope); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

func (r *citationProfileRepository) retireCitationProfileFencedScopeForReenrollment(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
	now time.Time,
) error {
	if scope == nil || scope.FencedAt == nil || scope.DeletedAt != nil {
		return types.ErrCitationProfileChanged
	}
	nextVersion := scope.ProfileReadVersion + 1
	result := tx.Model(&types.CitationProfileScope{}).
		Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND deleted_at IS NULL AND fenced_at IS NOT NULL",
			scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch).
		Where("profile_read_version = ?", scope.ProfileReadVersion).
		Updates(map[string]interface{}{
			"deleted_at":           now,
			"profile_read_version": nextVersion,
			"updated_at":           now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return types.ErrCitationProfileChanged
	}
	if err := terminateCitationProfileWorkForDelete(tx, scope, now); err != nil {
		return err
	}
	if err := revokeCitationProfileExportsForScope(tx, scope, now); err != nil {
		return err
	}
	scope.DeletedAt = &now
	scope.ProfileReadVersion = nextVersion
	scope.UpdatedAt = now
	return nil
}

func citationProfileACLBindingMatchesContext(ctx context.Context, tenantID uint64, subjectID, kbID string, binding types.CitationProfileACLBinding) bool {
	binding = binding.Normalize()
	if !binding.Valid() || strings.TrimSpace(types.SessionOwnerIDFromContext(ctx)) != subjectID {
		return false
	}
	if authenticatedTenantID, ok := types.AuthenticatedTenantIDFromContext(ctx); !ok || authenticatedTenantID != binding.AuthenticatedTenantID {
		return false
	}
	if binding.AccessPath == types.CitationProfileACLAccessPathOwner && binding.AuthenticatedTenantID != tenantID {
		return false
	}
	if binding.AccessPath == types.CitationProfileACLAccessPathKBShare && binding.AccessPathID != kbID {
		return false
	}
	principal, ok := types.PrincipalFromContext(ctx)
	if !ok || principal.Type != binding.PrincipalType || principal.ID != binding.PrincipalID {
		return false
	}
	apiScope, hasAPIKey := types.TenantAPIKeyScopeFromContext(ctx)
	if binding.APIKeyID != 0 {
		return hasAPIKey && apiScope.KeyID == binding.APIKeyID
	}
	return !hasAPIKey || apiScope.KeyID == 0
}

func citationProfileScopeACLBindingEqual(scope *types.CitationProfileScope, binding types.CitationProfileACLBinding) bool {
	return scope != nil &&
		scope.ACLPrincipalType == binding.PrincipalType &&
		scope.ACLPrincipalID == binding.PrincipalID &&
		scope.ACLAuthenticatedTenantID == binding.AuthenticatedTenantID &&
		scope.ACLAPIKeyID == binding.APIKeyID &&
		scope.ACLAccessPath == binding.AccessPath &&
		scope.ACLAccessPathID == binding.AccessPathID
}

func (r *citationProfileRepository) refreshCitationProfileACLBinding(tx *gorm.DB, scope *types.CitationProfileScope, binding types.CitationProfileACLBinding, _ time.Time) error {
	now, err := citationProfileDatabaseNow(tx)
	if err != nil {
		return err
	}
	nextGeneration := scope.ACLGeneration + 1
	nextReadVersion := scope.ProfileReadVersion + 1
	result := tx.Model(&types.CitationProfileScope{}).
		Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND acl_generation = ? AND deleted_at IS NULL AND fenced_at IS NULL", scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch, scope.ACLGeneration).
		Updates(map[string]interface{}{
			"acl_principal_type":          binding.PrincipalType,
			"acl_principal_id":            binding.PrincipalID,
			"acl_authenticated_tenant_id": binding.AuthenticatedTenantID,
			"acl_api_key_id":              binding.APIKeyID,
			"acl_access_path":             binding.AccessPath,
			"acl_access_path_id":          binding.AccessPathID,
			"acl_generation":              nextGeneration,
			"acl_check_state":             types.CitationProfileACLStateUnknown,
			"next_acl_check_at":           now,
			"acl_check_lease_until":       nil,
			"acl_check_lease_token":       "",
			"profile_read_version":        nextReadVersion,
			"updated_at":                  now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return types.ErrCitationProfileChanged
	}
	scope.ACLPrincipalType = binding.PrincipalType
	scope.ACLPrincipalID = binding.PrincipalID
	scope.ACLAuthenticatedTenantID = binding.AuthenticatedTenantID
	scope.ACLAPIKeyID = binding.APIKeyID
	scope.ACLAccessPath = binding.AccessPath
	scope.ACLAccessPathID = binding.AccessPathID
	scope.ACLGeneration = nextGeneration
	scope.ACLCheckState = types.CitationProfileACLStateUnknown
	scope.NextACLCheckAt = citationTimePointer(now)
	scope.ACLCheckLeaseUntil = nil
	scope.ACLCheckLeaseToken = ""
	scope.ProfileReadVersion = nextReadVersion
	scope.UpdatedAt = now
	return pauseCitationProfileOutboxRetryBudget(tx, scope, now, now)
}

func citationTimePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
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
	var verifiedScope *types.CitationProfileScope
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
		if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
			return err
		}
		scopeSnapshot := *scope
		verifiedScope = &scopeSnapshot
		if existing, found, err := r.findCitationProfileOperationByIdem(
			tx, tenantID, subjectID, kbID, scope.SubjectEpoch, types.CitationOperationExport, idempotencyKey, requestDigest,
		); err != nil {
			return err
		} else if found {
			if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
				return err
			}
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

		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
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
		if err != nil {
			return err
		}
		return r.ensureCitationProfileACLCurrentTx(tx, scope)
	})
	if err != nil {
		return nil, err
	}
	if err := r.verifyCitationProfileReadSnapshotFresh(ctx, verifiedScope); err != nil {
		return nil, err
	}
	return operation, nil
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
	var verifiedScope *types.CitationProfileScope
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
		if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
			return err
		}
		scopeSnapshot := *scope
		verifiedScope = &scopeSnapshot
		operation, err = r.loadCitationProfileOperationByID(tx, tenantID, subjectID, kbID, scope.SubjectEpoch, types.CitationOperationExport, operationID)
		if err != nil {
			return err
		}
		if operation.Status == types.CitationProfileOperationStatusRevoked {
			return types.ErrCitationProfileNotFound
		}
		expiredWhere, err := citationProfileExpiredAtDatabaseSQL(
			"citation_profile_operations",
			tx.Dialector.Name(),
		)
		if err != nil {
			return err
		}
		var expiredCount int64
		if err := tx.Model(&types.CitationProfileOperation{}).
			Where(
				"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND operation_type = ?",
				operation.ID,
				tenantID,
				subjectID,
				kbID,
				scope.SubjectEpoch,
				types.CitationOperationExport,
			).
			Where(expiredWhere).
			Count(&expiredCount).Error; err != nil {
			return err
		}
		if expiredCount == 1 {
			operation.Status = types.CitationProfileOperationStatusExpired
		}
		return r.ensureCitationProfileACLCurrentTx(tx, scope)
	})
	if err != nil {
		return nil, err
	}
	if err := r.verifyCitationProfileReadSnapshotFresh(ctx, verifiedScope); err != nil {
		return nil, err
	}
	return operation, nil
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

		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
		resultSummary, err := citationProfileOperationResult(scope, map[string]interface{}{
			"receipt_code": types.CitationProfileReceiptHiddenAndFenced,
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
		if scope == nil {
			scope, err = r.loadSharedCitationScopeForBlindDelete(tx, tenantID, subjectID, kbID)
			if err != nil {
				return err
			}
		}
		if scope == nil {
			operation = citationSyntheticOperation(kbID, types.CitationOperationDeleteBlind)
			return nil
		}

		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
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
		if scope.FencedAt != nil {
			if err := r.finalizeCitationProfileFencedScopeForBlindDelete(tx, scope, operation.ID, now); err != nil {
				if errors.Is(err, types.ErrCitationProfileChanged) {
					return errCitationProfileBlindDeleteRace
				}
				return err
			}
			return nil
		}
		if err := r.fenceCitationProfileScopeForDelete(tx, scope, operation.ID, types.CitationOperationDeleteBlind, nil, now); err != nil {
			if errors.Is(err, types.ErrCitationProfileChanged) {
				return errCitationProfileBlindDeleteRace
			}
			return err
		}
		return nil
	})
	if errors.Is(err, errCitationProfileBlindDeleteRace) {
		return citationSyntheticOperation(kbID, types.CitationOperationDeleteBlind), nil
	}
	if err != nil {
		return nil, err
	}
	return operation, nil
}

func (r *citationProfileRepository) finalizeCitationProfileFencedScopeForBlindDelete(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
	operationID string,
	now time.Time,
) error {
	if scope == nil || scope.FencedAt == nil || scope.DeletedAt != nil {
		return types.ErrCitationProfileChanged
	}
	nextVersion := scope.ProfileReadVersion + 1
	result := tx.Model(&types.CitationProfileScope{}).
		Where("id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND deleted_at IS NULL AND fenced_at IS NOT NULL",
			scope.ID, scope.TenantID, scope.SubjectID, scope.KnowledgeBaseID, scope.SubjectEpoch).
		Where("profile_read_version = ?", scope.ProfileReadVersion).
		Updates(map[string]interface{}{
			"delete_request_id":     strings.TrimSpace(operationID),
			"deleted_at":            now,
			"profile_read_version":  nextVersion,
			"pending_event_count":   0,
			"pending_mapping_count": 0,
			"dirty_mapping_count":   0,
			"updated_at":            now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return types.ErrCitationProfileChanged
	}
	if err := terminateCitationProfileWorkForDelete(tx, scope, now); err != nil {
		return err
	}
	if err := revokeCitationProfileExportsForScope(tx, scope, now); err != nil {
		return err
	}
	scope.DeleteRequestID = strings.TrimSpace(operationID)
	scope.DeletedAt = &now
	scope.ProfileReadVersion = nextVersion
	scope.UpdatedAt = now
	return nil
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
		"enabled":               false,
		"fenced_at":             now,
		"fence_reason":          fenceReason,
		"delete_request_id":     strings.TrimSpace(operationID),
		"deleted_at":            now,
		"profile_read_version":  nextVersion,
		"pending_event_count":   0,
		"pending_mapping_count": 0,
		"updated_at":            now,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return types.ErrCitationProfileChanged
	}
	if err := terminateCitationProfileWorkForDelete(tx, scope, now); err != nil {
		return err
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
		"receipt_code":     types.CitationProfileReceiptHiddenAndFenced,
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

func citationProfileScopeACLCurrent(scope *types.CitationProfileScope, now time.Time) bool {
	return scope != nil && scope.ACLCurrentAt(now)
}

// citationProfileACLCurrentSQL mirrors CitationProfileScope.ACLCurrentAt for
// compare-and-swap paths that must fence expiry without first materializing a
// scope. alias is always an internal, static table name or alias.
func citationProfileACLCurrentSQL(alias string, now time.Time) (string, []interface{}) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	return citationProfileACLCurrentSQLAt(
		alias,
		alias+".acl_checked_at",
		alias+".next_acl_check_at",
		"?",
		[]interface{}{now},
	)
}

// citationProfileACLCurrentDatabaseSQL is the production authorization gate.
// PostgreSQL statement_timestamp() is fixed for one statement but, unlike
// CURRENT_TIMESTAMP, advances inside a long transaction. SQLite guarantees
// that every 'now' use in one sqlite3_step observes the same instant. Neither
// path trusts an application-node wall clock.
func citationProfileACLCurrentDatabaseSQL(alias string, dialect string) (string, []interface{}, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres":
		predicate, args := citationProfileACLCurrentSQLAt(
			alias,
			alias+".acl_checked_at",
			alias+".next_acl_check_at",
			"statement_timestamp()",
			nil,
		)
		return predicate, args, nil
	case "sqlite":
		predicate, args := citationProfileACLCurrentSQLAt(
			alias,
			"julianday("+alias+".acl_checked_at)",
			"julianday("+alias+".next_acl_check_at)",
			"julianday('now')",
			nil,
		)
		return predicate, args, nil
	default:
		return "", nil, fmt.Errorf("citation profile ACL clock: unsupported database dialect %q", dialect)
	}
}

func citationProfileExpiredAtDatabaseSQL(alias string, dialect string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "postgres":
		return alias + ".expires_at IS NOT NULL AND " + alias + ".expires_at <= statement_timestamp()", nil
	case "sqlite":
		return alias + ".expires_at IS NOT NULL AND julianday(" + alias + ".expires_at) <= julianday('now')", nil
	default:
		return "", fmt.Errorf("citation profile expiry clock: unsupported database dialect %q", dialect)
	}
}

func citationProfileACLCurrentSQLAt(
	alias string,
	checkedAtExpression string,
	nextCheckAtExpression string,
	clockExpression string,
	clockArgs []interface{},
) (string, []interface{}) {
	predicate := fmt.Sprintf(`
		%[1]s.acl_check_state = ?
		AND %[1]s.acl_checked_at IS NOT NULL
		AND %[2]s <= %[4]s
		AND %[1]s.next_acl_check_at IS NOT NULL
		AND %[3]s > %[4]s
		AND %[3]s > %[2]s
		AND %[1]s.acl_generation > 0
		AND %[1]s.acl_check_lease_token = ''
		AND %[1]s.acl_check_lease_until IS NULL
		AND %[1]s.tenant_id > 0
		AND TRIM(%[1]s.knowledge_base_id) <> ''
		AND TRIM(%[1]s.acl_principal_id) <> ''
		AND %[1]s.acl_authenticated_tenant_id > 0
		AND (
			(%[1]s.acl_principal_type = ? AND %[1]s.acl_api_key_id = 0)
			OR (%[1]s.acl_principal_type IN (?, ?) AND %[1]s.acl_api_key_id > 0)
		)
		AND (
			(%[1]s.acl_access_path = ? AND TRIM(%[1]s.acl_access_path_id) = '' AND %[1]s.acl_authenticated_tenant_id = %[1]s.tenant_id)
			OR (%[1]s.acl_access_path = ? AND TRIM(%[1]s.acl_access_path_id) = TRIM(%[1]s.knowledge_base_id))
			OR (%[1]s.acl_access_path = ? AND TRIM(%[1]s.acl_access_path_id) <> '')
		)`, alias, checkedAtExpression, nextCheckAtExpression, clockExpression)
	args := []interface{}{types.CitationProfileACLStateCurrent}
	args = append(args, clockArgs...)
	args = append(args, clockArgs...)
	args = append(args,
		types.PrincipalWebUser,
		types.PrincipalAPITenant,
		types.PrincipalAPIExternalUser,
		types.CitationProfileACLAccessPathOwner,
		types.CitationProfileACLAccessPathKBShare,
		types.CitationProfileACLAccessPathAgentShare,
	)
	return predicate, args
}

func (r *citationProfileRepository) ensureCitationProfileACLCurrentTx(
	tx *gorm.DB,
	scope *types.CitationProfileScope,
) error {
	if tx == nil || scope == nil {
		return types.ErrCitationProfileUnavailable
	}
	currentACLWhere, currentACLArgs, err := citationProfileACLCurrentDatabaseSQL(
		"citation_profile_scopes",
		tx.Dialector.Name(),
	)
	if err != nil {
		return err
	}
	var count int64
	err = tx.Model(&types.CitationProfileScope{}).
		Where(
			"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL",
			scope.ID,
			scope.TenantID,
			scope.SubjectID,
			scope.KnowledgeBaseID,
			scope.SubjectEpoch,
			true,
		).
		Where(currentACLWhere, currentACLArgs...).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count != 1 {
		return types.ErrCitationProfileUnavailable
	}
	return nil
}

// citationAdmissionRejection is an expected, redacted admission outcome. It
// must not abort the message transaction: the assistant message is still a
// valid product record, while the particular reference is not admitted as
// Fact A. The code deliberately contains no tenant, subject, knowledge, or KB
// identifiers so it is safe to log at an outer boundary.
type citationAdmissionRejection struct {
	code string
}

func (e *citationAdmissionRejection) Error() string {
	if e == nil || e.code == "" {
		return "citation profile admission rejected"
	}
	return "citation profile admission rejected: " + e.code
}

func isCitationAdmissionRejection(err error) bool {
	var rejection *citationAdmissionRejection
	return errors.As(err, &rejection)
}

func (r *citationProfileRepository) lockCitationSession(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	sessionID string,
) error {
	var session types.Session
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("id = ? AND tenant_id = ? AND user_id = ? AND deleted_at IS NULL", sessionID, tenantID, subjectID).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return gorm.ErrRecordNotFound
	}
	return err
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
		if err := r.lockCitationSession(tx, tenantID, subjectID, message.SessionID); err != nil {
			return err
		}
		result := tx.Model(&types.Message{}).
			Where("id = ? AND session_id = ? AND deleted_at IS NULL", message.ID, message.SessionID).
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
		scopeUpdatedAt, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
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
		orderedScopeIDs := make([]string, 0, len(scopeDeltas))
		for scopeID := range scopeDeltas {
			orderedScopeIDs = append(orderedScopeIDs, scopeID)
		}
		sort.Strings(orderedScopeIDs)
		for _, scopeID := range orderedScopeIDs {
			delta := scopeDeltas[scopeID]
			if delta.Count <= 0 {
				continue
			}
			currentACLWhere, currentACLArgs, err := citationProfileACLCurrentDatabaseSQL(
				"citation_profile_scopes",
				tx.Dialector.Name(),
			)
			if err != nil {
				return err
			}
			result := tx.Model(&types.CitationProfileScope{}).
				Where(
					"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL",
					scopeID,
					tenantID,
					subjectID,
					delta.KnowledgeBaseID,
					delta.SubjectEpoch,
					true,
				).
				Where(currentACLWhere, currentACLArgs...).
				Updates(map[string]interface{}{
					"profile_read_version": gorm.Expr("profile_read_version + ?", delta.Count),
					"pending_event_count":  gorm.Expr("pending_event_count + ?", delta.Count),
					"updated_at":           scopeUpdatedAt,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return fmt.Errorf("%w: scope identity changed before event commit", types.ErrCitationProfileChanged)
			}
		}
		if len(orderedScopeIDs) > 0 {
			// Re-check every participating scope together at the final
			// rollback-capable point. A per-scope CAS is insufficient: an
			// earlier scope can naturally cross its ACL deadline while later
			// scopes are still being updated.
			currentACLWhere, currentACLArgs, err := citationProfileACLCurrentDatabaseSQL(
				"citation_profile_scopes",
				tx.Dialector.Name(),
			)
			if err != nil {
				return err
			}
			var currentScopeCount int64
			if err := tx.Model(&types.CitationProfileScope{}).
				Where(
					"id IN ? AND tenant_id = ? AND subject_id = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL",
					orderedScopeIDs,
					tenantID,
					subjectID,
					true,
				).
				Where(currentACLWhere, currentACLArgs...).
				Count(&currentScopeCount).Error; err != nil {
				return err
			}
			if currentScopeCount != int64(len(orderedScopeIDs)) {
				return types.ErrCitationProfileUnavailable
			}
		}
		eventCount = len(events)
		return nil
	})
	if err != nil {
		return eventCount, err
	}
	pendingEventIDs, err := r.pendingCitationEventIDsForMessage(ctx, tenantID, subjectID, message.ID)
	if err != nil {
		return eventCount, &types.CitationProfilePostCommitError{Cause: err}
	}
	eventIDs = append(eventIDs, pendingEventIDs...)
	eventIDs = uniqueCitationEventIDs(eventIDs)
	var resolveErrs []error
	for _, eventID := range eventIDs {
		if _, resolveErr := r.ResolveEvidenceEvent(ctx, tenantID, subjectID, eventID); resolveErr != nil {
			resolveErrs = append(resolveErrs, resolveErr)
		}
	}
	resolveErr := errors.Join(resolveErrs...)
	if resolveErr != nil {
		return eventCount, &types.CitationProfilePostCommitError{Cause: resolveErr}
	}
	return eventCount, nil
}

// pendingCitationEventIDsForMessage discovers already-committed work without
// reconstructing event-time evidence from mutable Knowledge rows. The scope
// predicates make a current delete/fence win over a late completion retry.
func (r *citationProfileRepository) pendingCitationEventIDsForMessage(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	messageID string,
) ([]string, error) {
	var eventIDs []string
	err := r.db.WithContext(ctx).
		Table("citation_profile_events AS e").
		Select("e.id").
		Joins("JOIN citation_profile_scopes AS s ON s.id = e.scope_id AND s.tenant_id = e.tenant_id AND s.subject_id = e.subject_id AND s.knowledge_base_id = e.knowledge_base_id AND s.subject_epoch = e.subject_epoch").
		Joins("LEFT JOIN citation_profile_event_outbox AS o ON o.event_id = e.id AND o.tenant_id = e.tenant_id AND o.subject_id = e.subject_id AND o.knowledge_base_id = e.knowledge_base_id AND o.subject_epoch = e.subject_epoch AND o.scope_id = e.scope_id").
		Where("e.tenant_id = ? AND e.subject_id = ? AND e.message_id = ? AND e.retracted_at IS NULL", tenantID, subjectID, messageID).
		Where("s.enabled = ? AND s.deleted_at IS NULL AND s.fenced_at IS NULL", true).
		Where("(e.status = ? AND (o.id IS NULL OR o.status NOT IN (?, ?)) OR (COALESCE(e.active_run_id, '') <> '' AND (o.id IS NULL OR o.status NOT IN (?, ?))))",
			types.CitationProfileEventStatusPendingResolution,
			types.CitationProfileOutboxStatusDelivered,
			types.CitationProfileOutboxStatusDeadletter,
			types.CitationProfileOutboxStatusDelivered,
			types.CitationProfileOutboxStatusDeadletter,
		).
		Group("e.id, e.created_at").
		Order("e.created_at ASC, e.id ASC").
		Pluck("e.id", &eventIDs).Error
	return eventIDs, err
}

func uniqueCitationEventIDs(eventIDs []string) []string {
	if len(eventIDs) < 2 {
		return eventIDs
	}
	seen := make(map[string]struct{}, len(eventIDs))
	unique := make([]string, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		if _, exists := seen[eventID]; exists {
			continue
		}
		seen[eventID] = struct{}{}
		unique = append(unique, eventID)
	}
	return unique
}

func (r *citationProfileRepository) ResolveEvidenceEvent(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	eventID string,
) (*types.EvidenceResolutionRun, error) {
	return r.resolveEvidenceEvent(ctx, tenantID, subjectID, eventID, nil)
}

func (r *citationProfileRepository) resolveEvidenceEvent(
	ctx context.Context,
	tenantID uint64,
	subjectID string,
	eventID string,
	claimedOutbox *types.CitationProfileEventOutbox,
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
		eventSeed, err := r.loadCitationEvent(tx, tenantID, subjectID, eventID)
		if err != nil {
			return err
		}
		if err := lockCitationProfileSourceUniverseShared(tx, tenantID, eventSeed.KnowledgeBaseID); err != nil {
			return err
		}
		// Every mutating path takes the scope lock before the event lock.
		// Read the identity first, lock the authoritative scope, then reload
		// the event under lock so delete/correction/resolution serialize in one
		// order without allowing a stale identity to publish a run.
		scope, err := r.loadActiveCitationScopeByID(tx, tenantID, subjectID, eventSeed.KnowledgeBaseID, eventSeed.SubjectEpoch, eventSeed.ScopeID)
		if err != nil {
			return err
		}
		event, err := r.loadCitationEventForUpdate(tx, tenantID, subjectID, eventID)
		if err != nil {
			return err
		}
		if event.KnowledgeBaseID != eventSeed.KnowledgeBaseID ||
			event.SubjectEpoch != eventSeed.SubjectEpoch ||
			event.ScopeID != eventSeed.ScopeID {
			return types.ErrCitationProfileChanged
		}
		if event.RetractedAt != nil {
			return types.ErrCitationProfileDeleted
		}
		resolutionClaim, outboxAlreadyDelivered, err := lockCitationProfileOutboxForResolution(tx, event, claimedOutbox)
		if err != nil {
			return err
		}
		if event.ActiveRunID != "" {
			run, err := r.loadEvidenceResolutionRun(tx, tenantID, subjectID, event.ActiveRunID)
			if err != nil {
				return err
			}
			if !citationProfileResolutionRunIsTerminalForEvent(run, event) {
				return types.ErrCitationProfileUnavailable
			}
			if !outboxAlreadyDelivered {
				deliveredAt, err := citationProfileDatabaseNow(tx)
				if err != nil {
					return err
				}
				if err := markCitationEventOutboxDelivered(tx, event, resolutionClaim, deliveredAt); err != nil {
					return err
				}
			}
			if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
				return err
			}
			resolvedRun = run
			return nil
		}
		if event.Status != types.CitationProfileEventStatusPendingResolution {
			return fmt.Errorf("%w: event is not pending", types.ErrCitationProfileChanged)
		}
		if outboxAlreadyDelivered {
			return types.ErrCitationProfileOutboxLeaseLost
		}
		rows, err := r.loadCurrentWikiSourceRefRows(tx, tenantID, event.KnowledgeBaseID, event.SourceKnowledgeID)
		if err != nil {
			return err
		}
		linkStates, err := r.loadCitationResolutionLinkStates(tx, event, rows)
		if err != nil {
			return err
		}

		now, err := citationProfileDatabaseNow(tx)
		if err != nil {
			return err
		}
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
			links = buildEvidenceNodeLinks(event, run, rows, linkStates, now)
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
			"active_run_id":  run.ID,
			"status":         runStatus,
			"pending_reason": "",
			"updated_at":     now,
		}
		if tooManyLinks {
			eventUpdates["failed_reason"] = run.ErrorMessage
		} else {
			eventUpdates["resolved_at"] = now
		}
		result := tx.Model(&types.CitationProfileEvent{}).
			Where(
				"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND retracted_at IS NULL AND COALESCE(active_run_id, '') = ''",
				event.ID,
				tenantID,
				subjectID,
				event.KnowledgeBaseID,
				event.SubjectEpoch,
				event.ScopeID,
			).
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
		if event.PendingReason == "wiki_source_ref_drift" {
			scopeUpdates["dirty_mapping_count"] = gorm.Expr("CASE WHEN dirty_mapping_count > 0 THEN dirty_mapping_count - 1 ELSE 0 END")
		}
		result = tx.Model(&types.CitationProfileScope{}).
			Where(
				"id = ? AND tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL",
				scope.ID,
				tenantID,
				subjectID,
				event.KnowledgeBaseID,
				event.SubjectEpoch,
				true,
			).
			Updates(scopeUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("citation profile scope changed before run publish: %s", scope.ID)
		}
		if err := markCitationEventOutboxDelivered(tx, event, resolutionClaim, now); err != nil {
			return err
		}
		if err := r.ensureCitationProfileACLCurrentTx(tx, scope); err != nil {
			return err
		}

		resolvedRun = run
		return nil
	})
	return resolvedRun, err
}

func (r *citationProfileRepository) ResolveClaimedEvidenceEvent(
	ctx context.Context,
	claim *types.CitationProfileEventOutbox,
) (*types.EvidenceResolutionRun, error) {
	if !validCitationProfileOutboxClaim(claim) {
		return nil, types.ErrCitationProfileOutboxLeaseLost
	}
	return r.resolveEvidenceEvent(ctx, claim.TenantID, claim.SubjectID, claim.EventID, claim)
}

func citationProfileResolutionRunIsTerminalForEvent(
	run *types.EvidenceResolutionRun,
	event *types.CitationProfileEvent,
) bool {
	if run == nil || event == nil ||
		run.EventID != event.ID ||
		run.KnowledgeBaseID != event.KnowledgeBaseID ||
		run.SubjectEpoch != event.SubjectEpoch ||
		run.ScopeID != event.ScopeID ||
		run.Status != event.Status {
		return false
	}
	switch run.Status {
	case types.CitationProfileEventStatusResolved, types.CitationProfileEventStatusResolvedEmpty:
		return run.ResolvedAt != nil && run.FailedAt == nil
	case types.CitationProfileEventStatusFailed:
		return run.FailedAt != nil && run.ResolvedAt == nil
	default:
		return false
	}
}

type citationProfileScopeDelta struct {
	KnowledgeBaseID string
	SubjectEpoch    string
	Count           int
}

func (r *citationProfileRepository) buildCompletedAnswerEvents(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	message *types.Message,
) ([]types.CitationProfileEvent, []types.CitationProfileEventOutbox, map[string]citationProfileScopeDelta, error) {
	enqueuedAt, err := citationProfileDatabaseNow(tx)
	if err != nil {
		return nil, nil, nil, err
	}
	now := message.UpdatedAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	messageVersion := now.Format(time.RFC3339Nano)
	contentHash := citationSHA256Hex(message.Content)

	events := make([]types.CitationProfileEvent, 0, len(message.KnowledgeReferences))
	outboxRows := make([]types.CitationProfileEventOutbox, 0, len(message.KnowledgeReferences))
	scopeDeltas := make(map[string]citationProfileScopeDelta)
	type candidate struct {
		originReferenceIndex int
		reference            *types.SearchResult
		knowledge            *types.Knowledge
		knowledgeBaseID      string
	}
	candidates := make([]candidate, 0, len(message.KnowledgeReferences))
	knowledgeBaseIDs := make([]string, 0, len(message.KnowledgeReferences))
	seenKnowledgeBases := make(map[string]struct{}, len(message.KnowledgeReferences))

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
			if isCitationAdmissionRejection(err) {
				continue
			}
			return nil, nil, nil, err
		}
		kbID := strings.TrimSpace(knowledge.KnowledgeBaseID)
		if kbID == "" {
			continue
		}
		if referencedKB := strings.TrimSpace(ref.KnowledgeBaseID); referencedKB != "" && referencedKB != kbID {
			continue
		}
		candidates = append(candidates, candidate{
			originReferenceIndex: i,
			reference:            ref,
			knowledge:            knowledge,
			knowledgeBaseID:      kbID,
		})
		if _, ok := seenKnowledgeBases[kbID]; !ok {
			seenKnowledgeBases[kbID] = struct{}{}
			knowledgeBaseIDs = append(knowledgeBaseIDs, kbID)
		}
	}

	scopesByKB, err := r.loadActiveCitationScopes(tx, tenantID, subjectID, knowledgeBaseIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, candidate := range candidates {
		i := candidate.originReferenceIndex
		ref := candidate.reference
		knowledge := candidate.knowledge
		kbID := candidate.knowledgeBaseID
		scope := scopesByKB[kbID]
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
			if isCitationAdmissionRejection(err) {
				continue
			}
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
			NextAttemptAt:   enqueuedAt,
			CreatedAt:       enqueuedAt,
			UpdatedAt:       enqueuedAt,
		})
		delta := scopeDeltas[scope.ID]
		delta.KnowledgeBaseID = kbID
		delta.SubjectEpoch = scope.SubjectEpoch
		delta.Count++
		scopeDeltas[scope.ID] = delta
	}

	return events, outboxRows, scopeDeltas, nil
}

func (r *citationProfileRepository) loadCitationKnowledge(tx *gorm.DB, tenantID uint64, knowledgeID string) (*types.Knowledge, error) {
	var knowledge types.Knowledge
	err := tx.Where("tenant_id = ? AND id = ?", tenantID, knowledgeID).First(&knowledge).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, &citationAdmissionRejection{code: "knowledge_not_found"}
		}
		return nil, err
	}
	return &knowledge, nil
}

func (r *citationProfileRepository) loadActiveCitationScopes(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	knowledgeBaseIDs []string,
) (map[string]*types.CitationProfileScope, error) {
	result := make(map[string]*types.CitationProfileScope, len(knowledgeBaseIDs))
	if len(knowledgeBaseIDs) == 0 {
		return result, nil
	}
	knowledgeBaseIDs = append([]string(nil), knowledgeBaseIDs...)
	sort.Strings(knowledgeBaseIDs)
	currentACLWhere, currentACLArgs, err := citationProfileACLCurrentDatabaseSQL(
		"citation_profile_scopes",
		tx.Dialector.Name(),
	)
	if err != nil {
		return nil, err
	}
	var scopes []types.CitationProfileScope
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where(
			"tenant_id = ? AND subject_id = ? AND knowledge_base_id IN ? AND enabled = ? AND deleted_at IS NULL AND fenced_at IS NULL",
			tenantID,
			subjectID,
			knowledgeBaseIDs,
			true,
		).
		Where(currentACLWhere, currentACLArgs...).
		Order("id ASC").
		Find(&scopes).Error
	if err != nil {
		return nil, err
	}
	for i := range scopes {
		result[scopes[i].KnowledgeBaseID] = &scopes[i]
	}
	return result, nil
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
			"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND producer_event_key = ?",
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
			return nil, &citationAdmissionRejection{code: "knowledge_base_not_found"}
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
		Where("id = ? AND tenant_id = ? AND subject_id = ?", eventID, tenantID, subjectID).
		First(&event).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
		}
		return nil, err
	}
	return &event, nil
}

func (r *citationProfileRepository) loadCitationEvent(
	tx *gorm.DB,
	tenantID uint64,
	subjectID string,
	eventID string,
) (*types.CitationProfileEvent, error) {
	var event types.CitationProfileEvent
	err := tx.Where("id = ? AND tenant_id = ? AND subject_id = ?", eventID, tenantID, subjectID).
		First(&event).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, types.ErrCitationProfileNotFound
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
			return nil, types.ErrCitationProfileNotFound
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
			return nil, types.ErrCitationProfileDeleted
		}
		return nil, err
	}
	if err := r.ensureCitationProfileACLCurrentTx(tx, &scope); err != nil {
		return nil, err
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
	linkStates map[string]string,
	createdAt time.Time,
) []types.EvidenceNodeLink {
	links := make([]types.EvidenceNodeLink, 0, len(rows))
	for _, row := range rows {
		relationState := types.EvidenceRelationCurrent
		if state := strings.TrimSpace(linkStates[row.PageUUID]); state != "" {
			relationState = state
		}
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
			RelationState:     relationState,
			RelationSource:    "source_ref_index",
			MappingRevision:   run.RunMappingRevision,
			UniverseWatermark: run.RunUniverseWatermark,
			CreatedAt:         createdAt,
		})
	}
	return links
}

func (r *citationProfileRepository) loadCitationResolutionLinkStates(
	tx *gorm.DB,
	event *types.CitationProfileEvent,
	rows []types.WikiSourceRefIndex,
) (map[string]string, error) {
	states := make(map[string]string)
	if event == nil || len(rows) == 0 {
		return states, nil
	}
	pageUUIDs := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		pageUUID := strings.TrimSpace(row.PageUUID)
		if pageUUID == "" {
			continue
		}
		if _, ok := seen[pageUUID]; ok {
			continue
		}
		seen[pageUUID] = struct{}{}
		pageUUIDs = append(pageUUIDs, pageUUID)
	}
	if len(pageUUIDs) == 0 {
		return states, nil
	}
	var corrections []types.CitationProfileCorrection
	if err := tx.Where(
		"tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND subject_epoch = ? AND scope_id = ? AND event_id = ? AND page_uuid IN ?",
		event.TenantID,
		event.SubjectID,
		event.KnowledgeBaseID,
		event.SubjectEpoch,
		event.ScopeID,
		event.ID,
		pageUUIDs,
	).Order("created_at ASC, id ASC").Find(&corrections).Error; err != nil {
		return nil, err
	}
	for _, correction := range corrections {
		switch correction.CorrectionType {
		case types.CitationCorrectionRejectMapping, types.CitationCorrectionRetractEvent:
			states[correction.PageUUID] = types.EvidenceRelationDisputed
		case types.CitationCorrectionConfirmRelevant:
			states[correction.PageUUID] = types.EvidenceRelationCurrent
		}
	}
	return states, nil
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

func (r *citationProfileRepository) loadSharedCitationScopeForBlindDelete(
	tx *gorm.DB,
	authenticatedTenantID uint64,
	subjectID string,
	kbID string,
) (*types.CitationProfileScope, error) {
	var scope types.CitationProfileScope
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("acl_authenticated_tenant_id = ? AND subject_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", authenticatedTenantID, subjectID, kbID).
		Order("tenant_id ASC").
		Order("id ASC").
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
	eventCutoff, correctionCutoff, activeRunPointerCutoff := citationProfileScopeCutoffs(scope)
	return map[string]interface{}{
		"subject_epoch":                   scope.SubjectEpoch,
		"read_version":                    fmt.Sprintf("%d", scope.ProfileReadVersion),
		"event_cutoff":                    eventCutoff,
		"correction_cutoff":               correctionCutoff,
		"active_run_pointer_cutoff":       activeRunPointerCutoff,
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
