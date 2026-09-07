package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const WorkbenchPurpose = "workbench"

// WorkbenchBackendLocalProtectedDocker is the only backend key that may be
// treated as qualified by the local capability contract.
const WorkbenchBackendLocalProtectedDocker = "local-protected-docker"

type WorkbenchSessionState string

const (
	WorkbenchStateProvisioning WorkbenchSessionState = "PROVISIONING"
	WorkbenchStateReady        WorkbenchSessionState = "READY"
	WorkbenchStateClosing      WorkbenchSessionState = "CLOSING"
	WorkbenchStateClosed       WorkbenchSessionState = "CLOSED"
	WorkbenchStateLost         WorkbenchSessionState = "LOST"
)

func (s WorkbenchSessionState) IsTerminal() bool {
	switch s {
	case WorkbenchStateClosed, WorkbenchStateLost:
		return true
	default:
		return false
	}
}

func (s WorkbenchSessionState) IsValid() bool {
	switch s {
	case WorkbenchStateProvisioning, WorkbenchStateReady, WorkbenchStateClosing, WorkbenchStateClosed, WorkbenchStateLost:
		return true
	default:
		return false
	}
}

type WorkbenchSession struct {
	ID                 string                `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64                `json:"tenant_id" gorm:"index"`
	ChatSessionID      string                `json:"chat_session_id" gorm:"type:varchar(36);not null"`
	Purpose            string                `json:"purpose" gorm:"type:varchar(32);not null;default:workbench"`
	IncarnationID      string                `json:"incarnation_id" gorm:"type:varchar(36);not null"`
	SandboxConfigID    string                `json:"sandbox_config_id" gorm:"type:varchar(36);not null"`
	BackendType        string                `json:"backend_type" gorm:"type:varchar(32);not null"`
	State              WorkbenchSessionState `json:"state" gorm:"type:varchar(32);not null;default:PROVISIONING"`
	StateVersion       int64                 `json:"state_version" gorm:"not null;default:0"`
	LeaseEpoch         int64                 `json:"lease_epoch" gorm:"not null;default:0"`
	CapabilitySnapshot JSONMap               `json:"capability_snapshot" gorm:"type:jsonb;not null;default:'{}'"`
	PolicySnapshot     JSONMap               `json:"policy_snapshot" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedBy          string                `json:"created_by" gorm:"type:varchar(36);not null"`
	TerminalReason     string                `json:"terminal_reason,omitempty" gorm:"type:varchar(64)"`
	CreatedAt          time.Time             `json:"created_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
	ClosedAt           *time.Time            `json:"closed_at,omitempty"`
}

func (WorkbenchSession) TableName() string {
	return "workbench_sessions"
}

func (s *WorkbenchSession) BeforeCreate(tx *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	if s.Purpose == "" {
		s.Purpose = WorkbenchPurpose
	}
	if s.State == "" {
		s.State = WorkbenchStateProvisioning
	}
	if s.CapabilitySnapshot == nil {
		s.CapabilitySnapshot = JSONMap{}
	}
	if s.PolicySnapshot == nil {
		s.PolicySnapshot = JSONMap{}
	}
	now := time.Now().UTC()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = now
	}
	return nil
}
