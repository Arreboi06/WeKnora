package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

type WorkbenchControlService struct {
	control repository.WorkbenchControlRepository
}

type StartWorkbenchJobInput struct {
	TenantID               uint64
	WorkbenchSessionID     string
	ExpectedLeaseEpoch     int64
	StartNonce             string
	BackendType            string
	BackendIdentity        string
	ActorID                string
	ResourcePolicySnapshot types.JSONMap
}

type CreateWorkbenchCommandInput struct {
	TenantID                uint64
	WorkbenchJobID          string
	ExpectedLeaseEpoch      int64
	ExpectedJobStateVersion int64
	Sequence                int64
	Kind                    string
	Payload                 types.JSONMap
	ActorID                 string
}

func NewWorkbenchControlService(control repository.WorkbenchControlRepository, _ repository.WorkbenchSessionRepository) *WorkbenchControlService {
	return &WorkbenchControlService{control: control}
}

func (s *WorkbenchControlService) StartJob(ctx context.Context, input StartWorkbenchJobInput) (*types.WorkbenchJob, error) {
	if s == nil || s.control == nil || input.TenantID == 0 ||
		strings.TrimSpace(input.WorkbenchSessionID) == "" || input.ExpectedLeaseEpoch < 0 ||
		strings.TrimSpace(input.StartNonce) == "" || strings.TrimSpace(input.BackendType) == "" ||
		strings.TrimSpace(input.ActorID) == "" || input.ResourcePolicySnapshot == nil {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}

	return s.control.StartJobWithAudit(ctx, repository.WorkbenchJobStartInput{
		TenantID:               input.TenantID,
		WorkbenchSessionID:     strings.TrimSpace(input.WorkbenchSessionID),
		ExpectedLeaseEpoch:     input.ExpectedLeaseEpoch,
		StartNonceHash:         hashWorkbenchStartNonce(strings.TrimSpace(input.StartNonce)),
		BackendType:            strings.TrimSpace(input.BackendType),
		BackendIdentity:        strings.TrimSpace(input.BackendIdentity),
		ResourcePolicySnapshot: input.ResourcePolicySnapshot,
		CreatedBy:              strings.TrimSpace(input.ActorID),
	})
}

func (s *WorkbenchControlService) GetJob(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error) {
	if s == nil || s.control == nil || tenantID == 0 || strings.TrimSpace(id) == "" {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	return s.control.GetJobByID(ctx, tenantID, strings.TrimSpace(id))
}

func (s *WorkbenchControlService) GetCommand(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error) {
	if s == nil || s.control == nil || tenantID == 0 || strings.TrimSpace(id) == "" {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	return s.control.GetCommandByID(ctx, tenantID, strings.TrimSpace(id))
}

func (s *WorkbenchControlService) CreateCommand(ctx context.Context, input CreateWorkbenchCommandInput) (*types.WorkbenchCommand, error) {
	if s == nil || s.control == nil || input.TenantID == 0 || strings.TrimSpace(input.WorkbenchJobID) == "" ||
		input.ExpectedLeaseEpoch < 0 || input.ExpectedJobStateVersion < 0 || input.Sequence <= 0 ||
		strings.TrimSpace(input.Kind) == "" || input.Payload == nil || strings.TrimSpace(input.ActorID) == "" {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	job, err := s.control.GetJobByID(ctx, input.TenantID, strings.TrimSpace(input.WorkbenchJobID))
	if err != nil {
		return nil, err
	}
	if job.LeaseEpoch != input.ExpectedLeaseEpoch {
		return nil, repository.ErrWorkbenchStaleEpoch
	}
	if job.StateVersion != input.ExpectedJobStateVersion {
		return nil, repository.ErrWorkbenchJobStaleVersion
	}
	if job.State != types.WorkbenchJobStateRunning || job.ClosedAt != nil {
		return nil, repository.ErrWorkbenchJobConflict
	}
	return s.control.CreateCommandIfJobRunning(ctx, repository.WorkbenchCommandCreateInput{
		Command: &types.WorkbenchCommand{
			TenantID:           input.TenantID,
			WorkbenchJobID:     job.ID,
			WorkbenchSessionID: job.WorkbenchSessionID,
			Sequence:           input.Sequence,
			Kind:               strings.TrimSpace(input.Kind),
			Payload:            input.Payload,
			State:              types.WorkbenchCommandStateQueued,
			StateVersion:       0,
			CreatedBy:          strings.TrimSpace(input.ActorID),
		},
		ExpectedLeaseEpoch:      input.ExpectedLeaseEpoch,
		ExpectedJobStateVersion: input.ExpectedJobStateVersion,
	})
}

func (s *WorkbenchControlService) ListAuditOutbox(ctx context.Context, tenantID uint64, jobID string, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error) {
	if s == nil || s.control == nil || tenantID == 0 || strings.TrimSpace(jobID) == "" || (state != "" && !state.IsValid()) {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	return s.control.ListAuditOutboxForJob(ctx, tenantID, strings.TrimSpace(jobID), state, limit)
}
func (s *WorkbenchControlService) ListRunnerEvents(ctx context.Context, tenantID uint64, jobID string, afterSeq int64, limit int) ([]*types.WorkbenchRunnerEvent, error) {
	if s == nil || s.control == nil || tenantID == 0 || strings.TrimSpace(jobID) == "" || afterSeq < 0 {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	return s.control.ListRunnerEvents(ctx, tenantID, strings.TrimSpace(jobID), afterSeq, limit)
}

func hashWorkbenchStartNonce(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}
