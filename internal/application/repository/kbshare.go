package repository

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrKBShareNotFound      = errors.New("knowledge base share not found")
	ErrKBShareAlreadyExists = errors.New("knowledge base already shared to this organization")
)

// kbShareRepository implements KBShareRepository interface
type kbShareRepository struct {
	db                 *gorm.DB
	citationProfileACL citationProfileACLInvalidationGate
}

// NewKBShareRepository creates a new knowledge base share repository
func NewKBShareRepository(db *gorm.DB, citationProfileConfig *types.CitationProfileConfig) interfaces.KBShareRepository {
	return &kbShareRepository{db: db, citationProfileACL: newCitationProfileACLInvalidationGate(citationProfileConfig)}
}

// Create creates a new share record
func (r *kbShareRepository) Create(ctx context.Context, share *types.KnowledgeBaseShare) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&types.KnowledgeBaseShare{}).
			Where("knowledge_base_id = ? AND organization_id = ? AND deleted_at IS NULL", share.KnowledgeBaseID, share.OrganizationID).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrKBShareAlreadyExists
		}
		if err := tx.Create(share).Error; err != nil {
			return err
		}
		return r.citationProfileACL.invalidate(tx, citationProfileKBShareMutation(share), time.Now().UTC())
	})
}

// GetByID gets a share record by ID
func (r *kbShareRepository) GetByID(ctx context.Context, id string) (*types.KnowledgeBaseShare, error) {
	var share types.KnowledgeBaseShare
	err := r.db.WithContext(ctx).
		Where("id = ?", id).
		First(&share).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKBShareNotFound
		}
		return nil, err
	}
	return &share, nil
}

// GetByKBAndOrg gets a share record by knowledge base ID and organization ID
func (r *kbShareRepository) GetByKBAndOrg(ctx context.Context, kbID string, orgID string) (*types.KnowledgeBaseShare, error) {
	var share types.KnowledgeBaseShare
	err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ? AND organization_id = ?", kbID, orgID).
		First(&share).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKBShareNotFound
		}
		return nil, err
	}
	return &share, nil
}

// Update updates a share record
func (r *kbShareRepository) Update(ctx context.Context, share *types.KnowledgeBaseShare) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous types.KnowledgeBaseShare
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", share.ID).First(&previous).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Model(&types.KnowledgeBaseShare{}).Where("id = ?", share.ID).Updates(share).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		mutations := []types.CitationProfileACLMutation{citationProfileKBShareMutation(&previous)}
		if previous.SourceTenantID != share.SourceTenantID || previous.KnowledgeBaseID != share.KnowledgeBaseID {
			mutations = append(mutations, citationProfileKBShareMutation(share))
		}
		return r.citationProfileACL.invalidateMany(tx, mutations, now)
	})
}

// Delete soft deletes a share record
func (r *kbShareRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var share types.KnowledgeBaseShare
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&share).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.KnowledgeBaseShare{}).Error; err != nil {
			return err
		}
		return r.citationProfileACL.invalidate(tx, citationProfileKBShareMutation(&share), time.Now().UTC())
	})
}

// DeleteByKnowledgeBaseID soft deletes all share records for a knowledge base (e.g. when the KB is deleted)
func (r *kbShareRepository) DeleteByKnowledgeBaseID(ctx context.Context, kbID string) error {
	return r.deleteCitationProfileKBShares(ctx, "knowledge_base_id = ?", kbID)
}

// DeleteByOrganizationID soft deletes all share records for an organization (e.g. when the org is deleted)
func (r *kbShareRepository) DeleteByOrganizationID(ctx context.Context, orgID string) error {
	return r.deleteCitationProfileKBShares(ctx, "organization_id = ?", orgID)
}

func (r *kbShareRepository) deleteCitationProfileKBShares(ctx context.Context, predicate string, value interface{}) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var shares []types.KnowledgeBaseShare
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(predicate, value).
			Order("source_tenant_id ASC").Order("knowledge_base_id ASC").Order("id ASC").
			Find(&shares).Error; err != nil {
			return err
		}
		if err := tx.Where(predicate, value).Delete(&types.KnowledgeBaseShare{}).Error; err != nil {
			return err
		}
		sort.Slice(shares, func(i, j int) bool {
			if shares[i].SourceTenantID != shares[j].SourceTenantID {
				return shares[i].SourceTenantID < shares[j].SourceTenantID
			}
			if shares[i].KnowledgeBaseID != shares[j].KnowledgeBaseID {
				return shares[i].KnowledgeBaseID < shares[j].KnowledgeBaseID
			}
			return shares[i].ID < shares[j].ID
		})
		now := time.Now().UTC()
		mutations := make([]types.CitationProfileACLMutation, 0, len(shares))
		var lastSource uint64
		var lastKB string
		for i := range shares {
			if i > 0 && shares[i].SourceTenantID == lastSource && shares[i].KnowledgeBaseID == lastKB {
				continue
			}
			mutations = append(mutations, citationProfileKBShareMutation(&shares[i]))
			lastSource = shares[i].SourceTenantID
			lastKB = shares[i].KnowledgeBaseID
		}
		if len(mutations) == 0 {
			return nil
		}
		return r.citationProfileACL.invalidateMany(tx, mutations, now)
	})
}

func invalidateCitationProfileKBShareTx(tx *gorm.DB, share *types.KnowledgeBaseShare, now time.Time) error {
	return invalidateCitationProfileACLTx(tx, citationProfileKBShareMutation(share), now)
}

func citationProfileKBShareMutation(share *types.KnowledgeBaseShare) types.CitationProfileACLMutation {
	return types.CitationProfileACLMutation{
		SourceTenantID:  share.SourceTenantID,
		KnowledgeBaseID: share.KnowledgeBaseID,
		AccessPath:      types.CitationProfileACLAccessPathKBShare,
	}
}

// ListByKnowledgeBase lists all share records for a knowledge base
func (r *kbShareRepository) ListByKnowledgeBase(ctx context.Context, kbID string) ([]*types.KnowledgeBaseShare, error) {
	var shares []*types.KnowledgeBaseShare
	err := r.db.WithContext(ctx).
		Joins("JOIN organizations ON organizations.id = kb_shares.organization_id AND organizations.deleted_at IS NULL").
		Preload("Organization").
		Where("kb_shares.knowledge_base_id = ? AND kb_shares.deleted_at IS NULL", kbID).
		Order("kb_shares.created_at DESC").
		Find(&shares).Error

	if err != nil {
		return nil, err
	}
	return shares, nil
}

// ListByOrganization lists all share records for an organization.
// Excludes shares whose knowledge base has been soft-deleted.
func (r *kbShareRepository) ListByOrganization(ctx context.Context, orgID string) ([]*types.KnowledgeBaseShare, error) {
	var shares []*types.KnowledgeBaseShare
	err := r.db.WithContext(ctx).
		Joins("JOIN knowledge_bases ON knowledge_bases.id = kb_shares.knowledge_base_id AND knowledge_bases.deleted_at IS NULL").
		Preload("KnowledgeBase").
		Preload("Organization").
		Where("kb_shares.organization_id = ? AND kb_shares.deleted_at IS NULL", orgID).
		Order("kb_shares.created_at DESC").
		Find(&shares).Error

	if err != nil {
		return nil, err
	}
	return shares, nil
}

// ListByOrganizations lists all share records for the given organizations (batch).
func (r *kbShareRepository) ListByOrganizations(ctx context.Context, orgIDs []string) ([]*types.KnowledgeBaseShare, error) {
	if len(orgIDs) == 0 {
		return nil, nil
	}
	var shares []*types.KnowledgeBaseShare
	err := r.db.WithContext(ctx).
		Joins("JOIN knowledge_bases ON knowledge_bases.id = kb_shares.knowledge_base_id AND knowledge_bases.deleted_at IS NULL").
		Preload("KnowledgeBase").
		Preload("Organization").
		Where("kb_shares.organization_id IN ? AND kb_shares.deleted_at IS NULL", orgIDs).
		Order("kb_shares.created_at DESC").
		Find(&shares).Error
	if err != nil {
		return nil, err
	}
	return shares, nil
}

// ListSharedKBsForTenant lists all knowledge bases shared to organizations that the tenant participates in.
// Excludes shares for soft-deleted organizations and soft-deleted knowledge bases.
func (r *kbShareRepository) ListSharedKBsForTenant(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBaseShare, error) {
	var shares []*types.KnowledgeBaseShare

	// Get shares for organizations the tenant is a member of; exclude deleted orgs and deleted KBs.
	err := r.db.WithContext(ctx).
		Joins("JOIN knowledge_bases ON knowledge_bases.id = kb_shares.knowledge_base_id AND knowledge_bases.deleted_at IS NULL").
		Preload("KnowledgeBase").
		Preload("Organization").
		Joins("JOIN organization_tenant_members otm ON otm.organization_id = kb_shares.organization_id").
		Joins("JOIN organizations ON organizations.id = kb_shares.organization_id AND organizations.deleted_at IS NULL").
		Where("otm.tenant_id = ?", tenantID).
		Where("kb_shares.deleted_at IS NULL").
		Order("kb_shares.created_at DESC").
		Find(&shares).Error

	if err != nil {
		return nil, err
	}
	return shares, nil
}

// CountSharesByKnowledgeBaseID counts the number of organizations a knowledge base is shared with
func (r *kbShareRepository) CountSharesByKnowledgeBaseID(ctx context.Context, kbID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.KnowledgeBaseShare{}).
		Where("knowledge_base_id = ? AND deleted_at IS NULL", kbID).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

// CountSharesByKnowledgeBaseIDs counts the number of shares for multiple knowledge bases at once
func (r *kbShareRepository) CountSharesByKnowledgeBaseIDs(ctx context.Context, kbIDs []string) (map[string]int64, error) {
	if len(kbIDs) == 0 {
		return make(map[string]int64), nil
	}

	type result struct {
		KnowledgeBaseID string `gorm:"column:knowledge_base_id"`
		Count           int64  `gorm:"column:count"`
	}

	var results []result
	err := r.db.WithContext(ctx).Model(&types.KnowledgeBaseShare{}).
		Select("knowledge_base_id, COUNT(*) as count").
		Where("knowledge_base_id IN ? AND deleted_at IS NULL", kbIDs).
		Group("knowledge_base_id").
		Find(&results).Error

	if err != nil {
		return nil, err
	}

	countMap := make(map[string]int64)
	for _, r := range results {
		countMap[r.KnowledgeBaseID] = r.Count
	}
	return countMap, nil
}

// CountByOrganizations returns share counts per organization (only orgs in orgIDs). Excludes deleted KBs.
func (r *kbShareRepository) CountByOrganizations(ctx context.Context, orgIDs []string) (map[string]int64, error) {
	if len(orgIDs) == 0 {
		return make(map[string]int64), nil
	}
	type row struct {
		OrgID string `gorm:"column:organization_id"`
		Count int64  `gorm:"column:count"`
	}
	var rows []row
	err := r.db.WithContext(ctx).Model(&types.KnowledgeBaseShare{}).
		Joins("JOIN knowledge_bases ON knowledge_bases.id = kb_shares.knowledge_base_id AND knowledge_bases.deleted_at IS NULL").
		Select("kb_shares.organization_id as organization_id, COUNT(*) as count").
		Where("kb_shares.organization_id IN ? AND kb_shares.deleted_at IS NULL", orgIDs).
		Group("kb_shares.organization_id").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64)
	for _, o := range orgIDs {
		out[o] = 0
	}
	for _, r := range rows {
		out[r.OrgID] = r.Count
	}
	return out, nil
}
