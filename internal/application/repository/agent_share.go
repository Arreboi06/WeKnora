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
	ErrAgentShareNotFound      = errors.New("agent share not found")
	ErrAgentShareAlreadyExists = errors.New("agent already shared to this organization")
)

// agentShareRepository implements AgentShareRepository interface
type agentShareRepository struct {
	db                 *gorm.DB
	citationProfileACL citationProfileACLInvalidationGate
}

// NewAgentShareRepository creates a new agent share repository
func NewAgentShareRepository(db *gorm.DB, citationProfileConfig *types.CitationProfileConfig) interfaces.AgentShareRepository {
	return &agentShareRepository{db: db, citationProfileACL: newCitationProfileACLInvalidationGate(citationProfileConfig)}
}

// Create creates a new agent share record
func (r *agentShareRepository) Create(ctx context.Context, share *types.AgentShare) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&types.AgentShare{}).
			Where("agent_id = ? AND source_tenant_id = ? AND organization_id = ? AND deleted_at IS NULL",
				share.AgentID, share.SourceTenantID, share.OrganizationID).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAgentShareAlreadyExists
		}
		if err := tx.Create(share).Error; err != nil {
			return err
		}
		return r.citationProfileACL.invalidate(tx, citationProfileAgentShareMutation(share), time.Now().UTC())
	})
}

// GetByID gets a share record by ID
func (r *agentShareRepository) GetByID(ctx context.Context, id string) (*types.AgentShare, error) {
	var share types.AgentShare
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&share).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentShareNotFound
		}
		return nil, err
	}
	return &share, nil
}

// GetByAgentAndOrg gets a share record by agent ID and organization ID
func (r *agentShareRepository) GetByAgentAndOrg(ctx context.Context, agentID string, orgID string) (*types.AgentShare, error) {
	var share types.AgentShare
	err := r.db.WithContext(ctx).
		Where("agent_id = ? AND organization_id = ?", agentID, orgID).
		First(&share).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentShareNotFound
		}
		return nil, err
	}
	return &share, nil
}

// Update updates a share record
func (r *agentShareRepository) Update(ctx context.Context, share *types.AgentShare) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var previous types.AgentShare
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", share.ID).First(&previous).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Model(&types.AgentShare{}).Where("id = ?", share.ID).Updates(share).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		mutations := []types.CitationProfileACLMutation{citationProfileAgentShareMutation(&previous)}
		if previous.SourceTenantID != share.SourceTenantID || previous.AgentID != share.AgentID {
			mutations = append(mutations, citationProfileAgentShareMutation(share))
		}
		return r.citationProfileACL.invalidateMany(tx, mutations, now)
	})
}

// Delete soft deletes a share record
func (r *agentShareRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var share types.AgentShare
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&share).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.AgentShare{}).Error; err != nil {
			return err
		}
		return r.citationProfileACL.invalidate(tx, citationProfileAgentShareMutation(&share), time.Now().UTC())
	})
}

// DeleteByAgentIDAndSourceTenant soft deletes all share records for an agent (id, tenant_id)
func (r *agentShareRepository) DeleteByAgentIDAndSourceTenant(ctx context.Context, agentID string, sourceTenantID uint64) error {
	return r.deleteCitationProfileAgentShares(ctx, "agent_id = ? AND source_tenant_id = ?", agentID, sourceTenantID)
}

// DeleteByOrganizationID soft deletes all share records for an organization
func (r *agentShareRepository) DeleteByOrganizationID(ctx context.Context, orgID string) error {
	return r.deleteCitationProfileAgentShares(ctx, "organization_id = ?", orgID)
}

func (r *agentShareRepository) deleteCitationProfileAgentShares(ctx context.Context, predicate string, values ...interface{}) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var shares []types.AgentShare
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where(predicate, values...).
			Order("source_tenant_id ASC").Order("agent_id ASC").Order("id ASC").
			Find(&shares).Error; err != nil {
			return err
		}
		if err := tx.Where(predicate, values...).Delete(&types.AgentShare{}).Error; err != nil {
			return err
		}
		sort.Slice(shares, func(i, j int) bool {
			if shares[i].SourceTenantID != shares[j].SourceTenantID {
				return shares[i].SourceTenantID < shares[j].SourceTenantID
			}
			if shares[i].AgentID != shares[j].AgentID {
				return shares[i].AgentID < shares[j].AgentID
			}
			return shares[i].ID < shares[j].ID
		})
		now := time.Now().UTC()
		mutations := make([]types.CitationProfileACLMutation, 0, len(shares))
		var lastSource uint64
		var lastAgent string
		for i := range shares {
			if i > 0 && shares[i].SourceTenantID == lastSource && shares[i].AgentID == lastAgent {
				continue
			}
			mutations = append(mutations, citationProfileAgentShareMutation(&shares[i]))
			lastSource = shares[i].SourceTenantID
			lastAgent = shares[i].AgentID
		}
		if len(mutations) == 0 {
			return nil
		}
		return r.citationProfileACL.invalidateMany(tx, mutations, now)
	})
}

func invalidateCitationProfileAgentShareTx(tx *gorm.DB, share *types.AgentShare, now time.Time) error {
	return invalidateCitationProfileACLTx(tx, citationProfileAgentShareMutation(share), now)
}

func citationProfileAgentShareMutation(share *types.AgentShare) types.CitationProfileACLMutation {
	return types.CitationProfileACLMutation{
		SourceTenantID: share.SourceTenantID,
		AccessPath:     types.CitationProfileACLAccessPathAgentShare,
		AccessPathID:   share.AgentID,
	}
}

// ListByAgent lists all share records for an agent
func (r *agentShareRepository) ListByAgent(ctx context.Context, agentID string) ([]*types.AgentShare, error) {
	var shares []*types.AgentShare
	err := r.db.WithContext(ctx).
		Preload("Organization").
		Where("agent_id = ?", agentID).
		Order("created_at DESC").
		Find(&shares).Error
	if err != nil {
		return nil, err
	}
	return shares, nil
}

// ListByOrganization lists all share records for an organization (excluding deleted agents)
func (r *agentShareRepository) ListByOrganization(ctx context.Context, orgID string) ([]*types.AgentShare, error) {
	var shares []*types.AgentShare
	err := r.db.WithContext(ctx).
		Joins("JOIN custom_agents ON custom_agents.id = agent_shares.agent_id AND custom_agents.tenant_id = agent_shares.source_tenant_id AND custom_agents.deleted_at IS NULL").
		Preload("Agent").
		Preload("Organization").
		Where("agent_shares.organization_id = ? AND agent_shares.deleted_at IS NULL", orgID).
		Order("agent_shares.created_at DESC").
		Find(&shares).Error
	if err != nil {
		return nil, err
	}
	return shares, nil
}

// ListByOrganizations lists all share records for the given organizations (batch).
func (r *agentShareRepository) ListByOrganizations(ctx context.Context, orgIDs []string) ([]*types.AgentShare, error) {
	if len(orgIDs) == 0 {
		return nil, nil
	}
	var shares []*types.AgentShare
	err := r.db.WithContext(ctx).
		Joins("JOIN custom_agents ON custom_agents.id = agent_shares.agent_id AND custom_agents.tenant_id = agent_shares.source_tenant_id AND custom_agents.deleted_at IS NULL").
		Preload("Agent").
		Preload("Organization").
		Where("agent_shares.organization_id IN ? AND agent_shares.deleted_at IS NULL", orgIDs).
		Order("agent_shares.created_at DESC").
		Find(&shares).Error
	if err != nil {
		return nil, err
	}
	return shares, nil
}

// CountByOrganizations returns share counts per organization (only orgs in orgIDs). Excludes deleted agents.
func (r *agentShareRepository) CountByOrganizations(ctx context.Context, orgIDs []string) (map[string]int64, error) {
	if len(orgIDs) == 0 {
		return make(map[string]int64), nil
	}
	type row struct {
		OrgID string `gorm:"column:organization_id"`
		Count int64  `gorm:"column:count"`
	}
	var rows []row
	err := r.db.WithContext(ctx).Model(&types.AgentShare{}).
		Joins("JOIN custom_agents ON custom_agents.id = agent_shares.agent_id AND custom_agents.tenant_id = agent_shares.source_tenant_id AND custom_agents.deleted_at IS NULL").
		Select("agent_shares.organization_id as organization_id, COUNT(*) as count").
		Where("agent_shares.organization_id IN ? AND agent_shares.deleted_at IS NULL", orgIDs).
		Group("agent_shares.organization_id").
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

// ListSharedAgentsForTenant lists all agents shared to organizations that the
// caller's tenant participates in. Plan 3 of #1303 keys this on tenant rather
// than user.
func (r *agentShareRepository) ListSharedAgentsForTenant(ctx context.Context, tenantID uint64) ([]*types.AgentShare, error) {
	var shares []*types.AgentShare
	err := r.db.WithContext(ctx).
		Joins("JOIN custom_agents ON custom_agents.id = agent_shares.agent_id AND custom_agents.tenant_id = agent_shares.source_tenant_id AND custom_agents.deleted_at IS NULL").
		Preload("Agent").
		Preload("Organization").
		Joins("JOIN organization_tenant_members otm ON otm.organization_id = agent_shares.organization_id").
		Joins("JOIN organizations ON organizations.id = agent_shares.organization_id AND organizations.deleted_at IS NULL").
		Where("otm.tenant_id = ?", tenantID).
		Where("agent_shares.deleted_at IS NULL").
		Order("agent_shares.created_at DESC").
		Find(&shares).Error
	if err != nil {
		return nil, err
	}
	return shares, nil
}

// GetShareByAgentIDForTenant returns one share for the given agentID that the
// tenant can reach (tenant participates in some org with the share), excluding
// source_tenant_id == excludeTenantID. Single query.
func (r *agentShareRepository) GetShareByAgentIDForTenant(ctx context.Context, tenantID uint64, agentID string, excludeTenantID uint64) (*types.AgentShare, error) {
	var share types.AgentShare
	tx := r.db.WithContext(ctx).
		Joins("JOIN organization_tenant_members otm ON otm.organization_id = agent_shares.organization_id").
		Joins("JOIN organizations ON organizations.id = agent_shares.organization_id AND organizations.deleted_at IS NULL").
		Joins("JOIN custom_agents ON custom_agents.id = agent_shares.agent_id AND custom_agents.tenant_id = agent_shares.source_tenant_id AND custom_agents.deleted_at IS NULL").
		Where("agent_shares.agent_id = ?", agentID).
		Where("otm.tenant_id = ?", tenantID).
		Where("agent_shares.source_tenant_id != ?", excludeTenantID).
		Where("agent_shares.deleted_at IS NULL").
		Order("agent_shares.id").
		Limit(1).
		Find(&share)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if tx.RowsAffected == 0 {
		return nil, ErrAgentShareNotFound
	}
	return &share, nil
}

// GetShareByAgentIDAndSourceForTenant validates an exact source selector
// against organization membership. This avoids loading every shared agent and
// makes same-ID builtins from multiple workspaces deterministic.
func (r *agentShareRepository) GetShareByAgentIDAndSourceForTenant(
	ctx context.Context,
	tenantID uint64,
	agentID string,
	sourceTenantID uint64,
) (*types.AgentShare, error) {
	var share types.AgentShare
	tx := r.db.WithContext(ctx).
		Joins("JOIN organization_tenant_members otm ON otm.organization_id = agent_shares.organization_id").
		Joins("JOIN organizations ON organizations.id = agent_shares.organization_id AND organizations.deleted_at IS NULL").
		Joins("JOIN custom_agents ON custom_agents.id = agent_shares.agent_id AND custom_agents.tenant_id = agent_shares.source_tenant_id AND custom_agents.deleted_at IS NULL").
		Where("agent_shares.agent_id = ?", agentID).
		Where("agent_shares.source_tenant_id = ?", sourceTenantID).
		Where("otm.tenant_id = ?", tenantID).
		Where("agent_shares.deleted_at IS NULL").
		Order("agent_shares.id").
		Limit(1).
		Find(&share)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if tx.RowsAffected == 0 {
		return nil, ErrAgentShareNotFound
	}
	return &share, nil
}
