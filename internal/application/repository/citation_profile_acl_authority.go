package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

type citationProfileACLAuthority struct {
	db *gorm.DB
}

// NewCitationProfileACLAuthority constructs the strict background authority.
// It queries the durable grant rows directly and never reuses request
// middleware paths that intentionally collapse or swallow dependency errors.
func NewCitationProfileACLAuthority(db *gorm.DB) interfaces.CitationProfileACLAuthority {
	return &citationProfileACLAuthority{db: db}
}

func (a *citationProfileACLAuthority) CheckCitationProfileACL(
	ctx context.Context,
	scope *types.CitationProfileScope,
) (types.CitationProfileACLAuthorityResult, error) {
	if a == nil || a.db == nil {
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionError), errors.New("citation profile ACL authority requires database")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionError), err
	}
	if !citationProfileScopeHasAuthorityBinding(scope) {
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionUnknown), nil
	}

	db := a.db.WithContext(ctx)
	tenantsLive, err := citationProfileAuthorityTenantsLive(db, scope)
	if err != nil {
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionError), err
	}
	if !tenantsLive {
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionDeny), nil
	}
	kbLive, err := citationProfileAuthorityKnowledgeBaseLive(db, scope)
	if err != nil {
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionError), err
	}
	if !kbLive {
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionDeny), nil
	}

	switch scope.ACLPrincipalType {
	case types.PrincipalWebUser:
		userLive, err := citationProfileAuthorityWebUserLive(db, scope.ACLPrincipalID)
		if err != nil {
			return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionError), err
		}
		if !userLive {
			return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionDeny), nil
		}
		memberLive, err := citationProfileAuthorityTenantMemberLive(db, scope.ACLPrincipalID, scope.ACLAuthenticatedTenantID)
		if err != nil {
			return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionError), err
		}
		if !memberLive {
			return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionDeny), nil
		}
		decision, err := citationProfileAuthorityAccessPath(db, scope)
		return citationProfileACLAuthorityResult(decision), err
	case types.PrincipalAPITenant, types.PrincipalAPIExternalUser:
		keyLive, validUntil, err := citationProfileAuthorityAPIKeyLive(db, scope)
		if err != nil {
			return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionError), err
		}
		if !keyLive {
			return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionDeny), nil
		}
		decision, err := citationProfileAuthorityAccessPath(db, scope)
		result := citationProfileACLAuthorityResult(decision)
		if err == nil && decision == types.CitationProfileACLDecisionAllow {
			result.ValidUntil = validUntil
		}
		return result, err
	default:
		return citationProfileACLAuthorityResult(types.CitationProfileACLDecisionUnknown), nil
	}
}

func citationProfileACLAuthorityResult(decision types.CitationProfileACLDecision) types.CitationProfileACLAuthorityResult {
	return types.CitationProfileACLAuthorityResult{Decision: decision}
}

func citationProfileAuthorityTenantsLive(db *gorm.DB, scope *types.CitationProfileScope) (bool, error) {
	tenantIDs := []uint64{scope.TenantID}
	if scope.ACLAuthenticatedTenantID != scope.TenantID {
		tenantIDs = append(tenantIDs, scope.ACLAuthenticatedTenantID)
	}
	var count int64
	err := db.Model(&types.Tenant{}).
		Where("id IN ?", tenantIDs).
		Count(&count).Error
	return count == int64(len(tenantIDs)), err
}

func citationProfileScopeHasAuthorityBinding(scope *types.CitationProfileScope) bool {
	return scope != nil && scope.ACLAuthorityBindingValid()
}

func citationProfileAuthorityKnowledgeBaseLive(db *gorm.DB, scope *types.CitationProfileScope) (bool, error) {
	var count int64
	err := db.Model(&types.KnowledgeBase{}).
		Where("id = ? AND tenant_id = ?", scope.KnowledgeBaseID, scope.TenantID).
		Count(&count).Error
	return count == 1, err
}

func citationProfileAuthorityWebUserLive(db *gorm.DB, userID string) (bool, error) {
	var count int64
	err := db.Model(&types.User{}).
		Where("id = ? AND is_active = ?", strings.TrimSpace(userID), true).
		Count(&count).Error
	return count == 1, err
}

func citationProfileAuthorityTenantMemberLive(db *gorm.DB, userID string, tenantID uint64) (bool, error) {
	var count int64
	err := db.Model(&types.TenantMember{}).
		Where("user_id = ? AND tenant_id = ? AND status = ?", strings.TrimSpace(userID), tenantID, types.TenantMemberStatusActive).
		Count(&count).Error
	return count == 1, err
}

func citationProfileAuthorityAPIKeyLive(db *gorm.DB, scope *types.CitationProfileScope) (bool, *time.Time, error) {
	var key types.TenantAPIKey
	query := db.Where("id = ?", scope.ACLAPIKeyID)
	switch db.Dialector.Name() {
	case "postgres":
		query = query.Where("(expires_at IS NULL OR expires_at > statement_timestamp())")
	case "sqlite":
		query = query.Where("(expires_at IS NULL OR julianday(expires_at) > julianday('now'))")
	default:
		return false, nil, fmt.Errorf("citation profile ACL authority clock: unsupported database dialect %q", db.Dialector.Name())
	}
	err := query.First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if key.IsPlatform() || key.TenantIDValue() != scope.ACLAuthenticatedTenantID || key.RevokedAt != nil {
		return false, nil, nil
	}
	if !(types.TenantAPIKeyScope{
		KeyID:            key.ID,
		ScopeType:        key.ScopeType,
		FullAccess:       key.FullAccess,
		KnowledgeBaseIDs: key.KnowledgeBaseIDs,
		Capabilities:     key.Capabilities,
	}).AllowsKnowledgeBase(scope.KnowledgeBaseID) {
		return false, nil, nil
	}
	allowed := key.FullAccess || (types.TenantAPIKeyScope{Capabilities: key.Capabilities}).HasCapability(types.APIKeyCapabilityRetrieve)
	if !allowed || key.ExpiresAt == nil {
		return allowed, nil, nil
	}
	validUntil := key.ExpiresAt.UTC()
	return true, &validUntil, nil
}

func citationProfileAuthorityAccessPath(db *gorm.DB, scope *types.CitationProfileScope) (types.CitationProfileACLDecision, error) {
	switch scope.ACLAccessPath {
	case types.CitationProfileACLAccessPathOwner:
		if scope.TenantID == scope.ACLAuthenticatedTenantID {
			return types.CitationProfileACLDecisionAllow, nil
		}
		return types.CitationProfileACLDecisionDeny, nil
	case types.CitationProfileACLAccessPathKBShare:
		live, err := citationProfileAuthorityKBShareLive(db, scope)
		if err != nil {
			return types.CitationProfileACLDecisionError, err
		}
		if live {
			return types.CitationProfileACLDecisionAllow, nil
		}
		return types.CitationProfileACLDecisionDeny, nil
	case types.CitationProfileACLAccessPathAgentShare:
		live, err := citationProfileAuthorityAgentShareLive(db, scope)
		if err != nil {
			return types.CitationProfileACLDecisionError, err
		}
		if live {
			return types.CitationProfileACLDecisionAllow, nil
		}
		return types.CitationProfileACLDecisionDeny, nil
	default:
		return types.CitationProfileACLDecisionUnknown, nil
	}
}

func citationProfileAuthorityKBShareLive(db *gorm.DB, scope *types.CitationProfileScope) (bool, error) {
	var count int64
	err := db.Table("kb_shares AS shares").
		Joins("JOIN organizations AS organizations ON organizations.id = shares.organization_id AND organizations.deleted_at IS NULL").
		Joins("JOIN organization_tenant_members AS members ON members.organization_id = shares.organization_id AND members.tenant_id = ?", scope.ACLAuthenticatedTenantID).
		Where("shares.knowledge_base_id = ? AND shares.source_tenant_id = ? AND shares.deleted_at IS NULL", scope.KnowledgeBaseID, scope.TenantID).
		Where("shares.permission IN ? AND members.role IN ?", []types.OrgMemberRole{types.OrgRoleViewer, types.OrgRoleEditor, types.OrgRoleAdmin}, []types.OrgMemberRole{types.OrgRoleViewer, types.OrgRoleEditor, types.OrgRoleAdmin}).
		Count(&count).Error
	return count > 0, err
}

func citationProfileAuthorityAgentShareLive(db *gorm.DB, scope *types.CitationProfileScope) (bool, error) {
	var agent types.CustomAgent
	err := db.Where("id = ? AND tenant_id = ?", scope.ACLAccessPathID, scope.TenantID).First(&agent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	allowed := agent.Config.KBSelectionMode == "all"
	if agent.Config.KBSelectionMode == "selected" {
		for _, kbID := range agent.Config.KnowledgeBases {
			if strings.TrimSpace(kbID) == scope.KnowledgeBaseID {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return false, nil
	}
	var count int64
	err = db.Table("agent_shares AS shares").
		Joins("JOIN organizations AS organizations ON organizations.id = shares.organization_id AND organizations.deleted_at IS NULL").
		Joins("JOIN organization_tenant_members AS members ON members.organization_id = shares.organization_id AND members.tenant_id = ?", scope.ACLAuthenticatedTenantID).
		Where("shares.agent_id = ? AND shares.source_tenant_id = ? AND shares.deleted_at IS NULL", scope.ACLAccessPathID, scope.TenantID).
		Where("shares.permission IN ? AND members.role IN ?", []types.OrgMemberRole{types.OrgRoleViewer, types.OrgRoleEditor, types.OrgRoleAdmin}, []types.OrgMemberRole{types.OrgRoleViewer, types.OrgRoleEditor, types.OrgRoleAdmin}).
		Count(&count).Error
	return count > 0, err
}
