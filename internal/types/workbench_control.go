package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type WorkbenchJobState string

const (
	WorkbenchJobStateQueued    WorkbenchJobState = "QUEUED"
	WorkbenchJobStateStarting  WorkbenchJobState = "STARTING"
	WorkbenchJobStateRunning   WorkbenchJobState = "RUNNING"
	WorkbenchJobStateClosing   WorkbenchJobState = "CLOSING"
	WorkbenchJobStateSucceeded WorkbenchJobState = "SUCCEEDED"
	WorkbenchJobStateFailed    WorkbenchJobState = "FAILED"
	WorkbenchJobStateCancelled WorkbenchJobState = "CANCELLED"
	WorkbenchJobStateLost      WorkbenchJobState = "LOST"
)

func (s WorkbenchJobState) IsValid() bool {
	switch s {
	case WorkbenchJobStateQueued, WorkbenchJobStateStarting, WorkbenchJobStateRunning, WorkbenchJobStateClosing,
		WorkbenchJobStateSucceeded, WorkbenchJobStateFailed, WorkbenchJobStateCancelled, WorkbenchJobStateLost:
		return true
	default:
		return false
	}
}

func (s WorkbenchJobState) IsTerminal() bool {
	switch s {
	case WorkbenchJobStateSucceeded, WorkbenchJobStateFailed, WorkbenchJobStateCancelled, WorkbenchJobStateLost:
		return true
	default:
		return false
	}
}

type WorkbenchCommandState string

const (
	WorkbenchCommandStateQueued    WorkbenchCommandState = "QUEUED"
	WorkbenchCommandStateRunning   WorkbenchCommandState = "RUNNING"
	WorkbenchCommandStateSucceeded WorkbenchCommandState = "SUCCEEDED"
	WorkbenchCommandStateFailed    WorkbenchCommandState = "FAILED"
	WorkbenchCommandStateCancelled WorkbenchCommandState = "CANCELLED"
	WorkbenchCommandStateLost      WorkbenchCommandState = "LOST"
)

func (s WorkbenchCommandState) IsValid() bool {
	switch s {
	case WorkbenchCommandStateQueued, WorkbenchCommandStateRunning, WorkbenchCommandStateSucceeded,
		WorkbenchCommandStateFailed, WorkbenchCommandStateCancelled, WorkbenchCommandStateLost:
		return true
	default:
		return false
	}
}

func (s WorkbenchCommandState) IsTerminal() bool {
	switch s {
	case WorkbenchCommandStateSucceeded, WorkbenchCommandStateFailed, WorkbenchCommandStateCancelled, WorkbenchCommandStateLost:
		return true
	default:
		return false
	}
}

type WorkbenchAuditOutboxState string

const (
	WorkbenchAuditOutboxStatePending   WorkbenchAuditOutboxState = "PENDING"
	WorkbenchAuditOutboxStatePublished WorkbenchAuditOutboxState = "PUBLISHED"
	WorkbenchAuditOutboxStateFailed    WorkbenchAuditOutboxState = "FAILED"
)

func (s WorkbenchAuditOutboxState) IsValid() bool {
	switch s {
	case WorkbenchAuditOutboxStatePending, WorkbenchAuditOutboxStatePublished, WorkbenchAuditOutboxStateFailed:
		return true
	default:
		return false
	}
}

type WorkbenchAuditAction string

const (
	WorkbenchAuditActionJobStarted       WorkbenchAuditAction = "workbench.job.started"
	WorkbenchAuditActionJobTerminated    WorkbenchAuditAction = "workbench.job.terminated"
	WorkbenchAuditActionCommandQueued    WorkbenchAuditAction = "workbench.command.queued"
	WorkbenchAuditActionCommandSucceeded WorkbenchAuditAction = "workbench.command.succeeded"
	WorkbenchAuditActionCommandFailed    WorkbenchAuditAction = "workbench.command.failed"
	WorkbenchAuditActionSessionLost      WorkbenchAuditAction = "workbench.session.lost"
)

type WorkbenchJob struct {
	ID                     string            `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID               uint64            `json:"tenant_id" gorm:"not null;index"`
	WorkbenchSessionID     string            `json:"workbench_session_id" gorm:"type:varchar(36);not null"`
	ChatSessionID          string            `json:"chat_session_id" gorm:"type:varchar(36);not null"`
	IncarnationID          string            `json:"incarnation_id" gorm:"type:varchar(36);not null"`
	LeaseEpoch             int64             `json:"lease_epoch" gorm:"not null;default:0"`
	StartNonceHash         string            `json:"start_nonce_hash" gorm:"type:varchar(64);not null"`
	BackendType            string            `json:"backend_type" gorm:"type:varchar(32);not null"`
	BackendIdentity        string            `json:"backend_identity" gorm:"type:varchar(128);not null"`
	State                  WorkbenchJobState `json:"state" gorm:"type:varchar(32);not null;default:QUEUED"`
	StateVersion           int64             `json:"state_version" gorm:"not null;default:0"`
	ResourcePolicySnapshot JSONMap           `json:"resource_policy_snapshot" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedBy              string            `json:"created_by" gorm:"type:varchar(36);not null"`
	TerminalReason         string            `json:"terminal_reason,omitempty" gorm:"type:varchar(64)"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
	ClosedAt               *time.Time        `json:"closed_at,omitempty"`
}

func (WorkbenchJob) TableName() string { return "workbench_jobs" }

func (j *WorkbenchJob) BeforeCreate(tx *gorm.DB) error {
	if j.ID == "" {
		j.ID = uuid.New().String()
	}
	if j.State == "" {
		j.State = WorkbenchJobStateQueued
	}
	if j.ResourcePolicySnapshot == nil {
		j.ResourcePolicySnapshot = JSONMap{}
	}
	now := time.Now().UTC()
	if j.CreatedAt.IsZero() {
		j.CreatedAt = now
	}
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = now
	}
	return nil
}

type WorkbenchCommand struct {
	ID                 string                `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64                `json:"tenant_id" gorm:"not null;index"`
	WorkbenchJobID     string                `json:"workbench_job_id" gorm:"type:varchar(36);not null"`
	WorkbenchSessionID string                `json:"workbench_session_id" gorm:"type:varchar(36);not null"`
	Sequence           int64                 `json:"sequence" gorm:"not null"`
	Kind               string                `json:"kind" gorm:"type:varchar(32);not null"`
	Payload            JSONMap               `json:"payload" gorm:"type:jsonb;not null;default:'{}'"`
	State              WorkbenchCommandState `json:"state" gorm:"type:varchar(32);not null;default:QUEUED"`
	StateVersion       int64                 `json:"state_version" gorm:"not null;default:0"`
	CreatedBy          string                `json:"created_by" gorm:"type:varchar(36);not null"`
	TerminalReason     string                `json:"terminal_reason,omitempty" gorm:"type:varchar(64)"`
	CreatedAt          time.Time             `json:"created_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
	ClosedAt           *time.Time            `json:"closed_at,omitempty"`
}

func (WorkbenchCommand) TableName() string { return "workbench_commands" }

func (c *WorkbenchCommand) BeforeCreate(tx *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	if c.State == "" {
		c.State = WorkbenchCommandStateQueued
	}
	if c.Payload == nil {
		c.Payload = JSONMap{}
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	return nil
}

type WorkbenchRunnerEvent struct {
	ID                 string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64    `json:"tenant_id" gorm:"not null;index"`
	WorkbenchJobID     string    `json:"workbench_job_id" gorm:"type:varchar(36);not null"`
	WorkbenchSessionID string    `json:"workbench_session_id" gorm:"type:varchar(36);not null"`
	CommandID          string    `json:"command_id" gorm:"type:varchar(36);not null;default:''"`
	Seq                int64     `json:"seq" gorm:"not null"`
	EventType          string    `json:"event_type" gorm:"type:varchar(32);not null"`
	Payload            JSONMap   `json:"payload" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt          time.Time `json:"created_at"`
}

func (WorkbenchRunnerEvent) TableName() string { return "workbench_runner_events" }

func (e *WorkbenchRunnerEvent) BeforeCreate(tx *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.Payload == nil {
		e.Payload = JSONMap{}
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	return nil
}

type WorkbenchAuditOutbox struct {
	ID                 string                    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64                    `json:"tenant_id" gorm:"not null;index"`
	WorkbenchSessionID string                    `json:"workbench_session_id" gorm:"type:varchar(36);not null"`
	WorkbenchJobID     string                    `json:"workbench_job_id" gorm:"type:varchar(36);not null;default:''"`
	CommandID          string                    `json:"command_id" gorm:"type:varchar(36);not null;default:''"`
	Action             WorkbenchAuditAction      `json:"action" gorm:"type:varchar(64);not null"`
	ActorUserID        string                    `json:"actor_user_id" gorm:"type:varchar(36);not null;default:''"`
	Outcome            string                    `json:"outcome" gorm:"type:varchar(16);not null;default:success"`
	Payload            JSONMap                   `json:"payload" gorm:"type:jsonb;not null;default:'{}'"`
	IdempotencyKey     string                    `json:"idempotency_key" gorm:"type:varchar(128);not null"`
	State              WorkbenchAuditOutboxState `json:"state" gorm:"type:varchar(16);not null;default:PENDING"`
	Attempts           int                       `json:"attempts" gorm:"not null;default:0"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	PublishedAt        *time.Time                `json:"published_at,omitempty"`
}

func (WorkbenchAuditOutbox) TableName() string { return "workbench_audit_outbox" }

func (a *WorkbenchAuditOutbox) BeforeCreate(tx *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	if a.State == "" {
		a.State = WorkbenchAuditOutboxStatePending
	}
	if a.Outcome == "" {
		a.Outcome = "success"
	}
	if a.Payload == nil {
		a.Payload = JSONMap{}
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = now
	}
	return nil
}
