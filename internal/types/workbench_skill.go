package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type WorkbenchSkillRunState string

const (
	WorkbenchSkillRunStateQueued    WorkbenchSkillRunState = "QUEUED"
	WorkbenchSkillRunStateRunning   WorkbenchSkillRunState = "RUNNING"
	WorkbenchSkillRunStateSucceeded WorkbenchSkillRunState = "SUCCEEDED"
	WorkbenchSkillRunStateFailed    WorkbenchSkillRunState = "FAILED"
	WorkbenchSkillRunStateCancelled WorkbenchSkillRunState = "CANCELLED"
	WorkbenchSkillRunStateLost      WorkbenchSkillRunState = "LOST"
)

func (s WorkbenchSkillRunState) IsValid() bool {
	switch s {
	case WorkbenchSkillRunStateQueued, WorkbenchSkillRunStateRunning, WorkbenchSkillRunStateSucceeded,
		WorkbenchSkillRunStateFailed, WorkbenchSkillRunStateCancelled, WorkbenchSkillRunStateLost:
		return true
	default:
		return false
	}
}

func (s WorkbenchSkillRunState) IsTerminal() bool {
	switch s {
	case WorkbenchSkillRunStateSucceeded, WorkbenchSkillRunStateFailed, WorkbenchSkillRunStateCancelled, WorkbenchSkillRunStateLost:
		return true
	default:
		return false
	}
}

type WorkbenchSkillRun struct {
	ID                    string                 `json:"id" gorm:"column:id;primaryKey"`
	TenantID              uint64                 `json:"tenant_id" gorm:"column:tenant_id"`
	ChatSessionID         string                 `json:"chat_session_id" gorm:"column:chat_session_id"`
	WorkbenchSessionID    string                 `json:"workbench_id" gorm:"column:workbench_session_id"`
	WorkbenchJobID        string                 `json:"job_id" gorm:"column:workbench_job_id"`
	CommandID             string                 `json:"command_id" gorm:"column:command_id"`
	SkillName             string                 `json:"skill_name" gorm:"column:skill_name"`
	SkillOperation        string                 `json:"skill_operation" gorm:"column:skill_operation"`
	OutputFileRef         WorkbenchFileRef       `json:"output_file_ref" gorm:"column:output_file_ref;type:jsonb"`
	OutputArtifactID      string                 `json:"output_artifact_id,omitempty" gorm:"column:output_artifact_id"`
	OutputArtifactVersion int                    `json:"output_artifact_version,omitempty" gorm:"column:output_artifact_version"`
	State                 WorkbenchSkillRunState `json:"state" gorm:"column:state"`
	StateVersion          int64                  `json:"state_version" gorm:"column:state_version"`
	CreatedBy             string                 `json:"created_by" gorm:"column:created_by"`
	TerminalReason        string                 `json:"terminal_reason,omitempty" gorm:"column:terminal_reason"`
	CreatedAt             time.Time              `json:"created_at" gorm:"column:created_at"`
	UpdatedAt             time.Time              `json:"updated_at" gorm:"column:updated_at"`
	ClosedAt              *time.Time             `json:"closed_at,omitempty" gorm:"column:closed_at"`
}

func (WorkbenchSkillRun) TableName() string { return "workbench_skill_runs" }

func (r *WorkbenchSkillRun) BeforeCreate(tx *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.State == "" {
		r.State = WorkbenchSkillRunStateQueued
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	return nil
}
