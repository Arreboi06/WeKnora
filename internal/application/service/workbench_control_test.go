package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L01WorkbenchControlServiceStartJobHashesNonceAndDelegatesAtomicStart(t *testing.T) {
	ctx := context.Background()
	control := &t2l01FakeWorkbenchControlRepo{}
	svc := NewWorkbenchControlService(control, nil)

	job, err := svc.StartJob(ctx, StartWorkbenchJobInput{
		TenantID:               71,
		WorkbenchSessionID:     "workbench-a",
		ExpectedLeaseEpoch:     9,
		StartNonce:             "plain-start-nonce",
		BackendType:            "docker",
		BackendIdentity:        "docker://local/t2l01",
		ActorID:                "actor-a",
		ResourcePolicySnapshot: types.JSONMap{"cpu_seconds": float64(30)},
	})
	require.NoError(t, err)
	require.Equal(t, "job-created", job.ID)
	require.Equal(t, uint64(71), control.startInput.TenantID)
	require.Equal(t, "workbench-a", control.startInput.WorkbenchSessionID)
	require.Equal(t, int64(9), control.startInput.ExpectedLeaseEpoch)
	require.NotContains(t, control.startInput.StartNonceHash, "plain-start-nonce")

	sum := sha256.Sum256([]byte("plain-start-nonce"))
	require.Equal(t, hex.EncodeToString(sum[:]), control.startInput.StartNonceHash)
	require.Equal(t, "docker", control.startInput.BackendType)
	require.Equal(t, "docker://local/t2l01", control.startInput.BackendIdentity)
	require.Equal(t, "actor-a", control.startInput.CreatedBy)
}

func TestT2L01WorkbenchControlServiceSurfacesAtomicStartErrors(t *testing.T) {
	ctx := context.Background()
	control := &t2l01FakeWorkbenchControlRepo{startErr: repository.ErrWorkbenchStaleEpoch}
	svc := NewWorkbenchControlService(control, nil)

	_, err := svc.StartJob(ctx, StartWorkbenchJobInput{
		TenantID:               71,
		WorkbenchSessionID:     "workbench-a",
		ExpectedLeaseEpoch:     8,
		StartNonce:             "plain-start-nonce",
		BackendType:            "docker",
		BackendIdentity:        "docker://local/t2l01",
		ActorID:                "actor-a",
		ResourcePolicySnapshot: types.JSONMap{"cpu_seconds": float64(30)},
	})
	require.ErrorIs(t, err, repository.ErrWorkbenchStaleEpoch)
	require.Equal(t, uint64(71), control.startInput.TenantID)
}

func TestT2L01WorkbenchControlServiceRejectsInvalidStartInput(t *testing.T) {
	svc := NewWorkbenchControlService(&t2l01FakeWorkbenchControlRepo{}, nil)
	_, err := svc.StartJob(context.Background(), StartWorkbenchJobInput{
		TenantID:               71,
		WorkbenchSessionID:     "workbench-a",
		ExpectedLeaseEpoch:     9,
		StartNonce:             "",
		BackendType:            "docker",
		BackendIdentity:        "docker://local/t2l01",
		ActorID:                "actor-a",
		ResourcePolicySnapshot: types.JSONMap{"cpu_seconds": float64(30)},
	})
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionInvalidArgument)
}

func TestT2L03WorkbenchControlServiceStartsJobBeforeBackendIdentityBind(t *testing.T) {
	ctx := context.Background()
	control := &t2l01FakeWorkbenchControlRepo{}
	svc := NewWorkbenchControlService(control, nil)

	job, err := svc.StartJob(ctx, StartWorkbenchJobInput{
		TenantID:               71,
		WorkbenchSessionID:     "workbench-a",
		ExpectedLeaseEpoch:     9,
		StartNonce:             "plain-start-nonce",
		BackendType:            "docker",
		BackendIdentity:        "",
		ActorID:                "actor-a",
		ResourcePolicySnapshot: types.JSONMap{"cpu_seconds": float64(30)},
	})
	require.NoError(t, err)
	require.Equal(t, "job-created", job.ID)
	require.Equal(t, "", control.startInput.BackendIdentity)
}

func TestT2L03WorkbenchControlServiceCreateCommandChecksLeaseVersionAndDelegates(t *testing.T) {
	ctx := context.Background()
	control := &t2l01FakeWorkbenchControlRepo{job: &types.WorkbenchJob{
		ID: "job-running", TenantID: 71, WorkbenchSessionID: "workbench-a", LeaseEpoch: 5,
		State: types.WorkbenchJobStateRunning, StateVersion: 2,
	}}
	svc := NewWorkbenchControlService(control, nil)

	command, err := svc.CreateCommand(ctx, CreateWorkbenchCommandInput{
		TenantID:                71,
		WorkbenchJobID:          "job-running",
		ExpectedLeaseEpoch:      5,
		ExpectedJobStateVersion: 2,
		Sequence:                1,
		Kind:                    "shell",
		Payload:                 types.JSONMap{"command": "printf ok"},
		ActorID:                 "actor-a",
	})
	require.NoError(t, err)
	require.Equal(t, "command-created", command.ID)
	require.Equal(t, uint64(71), control.commandInput.TenantID)
	require.Equal(t, "job-running", control.commandInput.WorkbenchJobID)
	require.Equal(t, "workbench-a", control.commandInput.WorkbenchSessionID)
	require.Equal(t, int64(1), control.commandInput.Sequence)
	require.Equal(t, types.WorkbenchCommandStateQueued, control.commandInput.State)
	require.Equal(t, "actor-a", control.commandInput.CreatedBy)
	require.Equal(t, 1, control.atomicCommandCalls)

	_, err = svc.CreateCommand(ctx, CreateWorkbenchCommandInput{
		TenantID:                71,
		WorkbenchJobID:          "job-running",
		ExpectedLeaseEpoch:      6,
		ExpectedJobStateVersion: 2,
		Sequence:                2,
		Kind:                    "shell",
		Payload:                 types.JSONMap{"command": "printf stale"},
		ActorID:                 "actor-a",
	})
	require.ErrorIs(t, err, repository.ErrWorkbenchStaleEpoch)
}

func TestT2L07WorkbenchControlServiceListsAuditOutboxByJob(t *testing.T) {
	ctx := context.Background()
	control := &t2l01FakeWorkbenchControlRepo{auditRows: []*types.WorkbenchAuditOutbox{{
		ID: "audit-1", TenantID: 71, WorkbenchSessionID: "workbench-a", WorkbenchJobID: "job-1", CommandID: "cmd-1",
		Action: types.WorkbenchAuditActionCommandFailed, Outcome: "failure", Payload: types.JSONMap{"killed": true}, State: types.WorkbenchAuditOutboxStatePending,
	}}}
	svc := NewWorkbenchControlService(control, nil)

	rows, err := svc.ListAuditOutbox(ctx, 71, "job-1", types.WorkbenchAuditOutboxStatePending, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "audit-1", rows[0].ID)
	require.Equal(t, t2l07AuditListCall{tenantID: 71, jobID: "job-1", state: types.WorkbenchAuditOutboxStatePending, limit: 10}, control.auditListCall)

	_, err = svc.ListAuditOutbox(ctx, 0, "job-1", types.WorkbenchAuditOutboxStatePending, 10)
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionInvalidArgument)
	_, err = svc.ListAuditOutbox(ctx, 71, "", types.WorkbenchAuditOutboxStatePending, 10)
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionInvalidArgument)
}

type t2l07AuditListCall struct {
	tenantID uint64
	jobID    string
	state    types.WorkbenchAuditOutboxState
	limit    int
}

type t2l01FakeWorkbenchControlRepo struct {
	startInput         repository.WorkbenchJobStartInput
	startErr           error
	job                *types.WorkbenchJob
	commandInput       *types.WorkbenchCommand
	atomicCommandCalls int
	auditRows          []*types.WorkbenchAuditOutbox
	auditListCall      t2l07AuditListCall
}

func (f *t2l01FakeWorkbenchControlRepo) ProbeSchema(context.Context) error { return nil }

func (f *t2l01FakeWorkbenchControlRepo) StartJobWithAudit(_ context.Context, input repository.WorkbenchJobStartInput) (*types.WorkbenchJob, error) {
	f.startInput = input
	if f.startErr != nil {
		return nil, f.startErr
	}
	return &types.WorkbenchJob{
		ID:                     "job-created",
		TenantID:               input.TenantID,
		WorkbenchSessionID:     input.WorkbenchSessionID,
		LeaseEpoch:             input.ExpectedLeaseEpoch,
		StartNonceHash:         input.StartNonceHash,
		BackendType:            input.BackendType,
		BackendIdentity:        input.BackendIdentity,
		State:                  types.WorkbenchJobStateQueued,
		ResourcePolicySnapshot: input.ResourcePolicySnapshot,
		CreatedBy:              input.CreatedBy,
	}, nil
}

func (f *t2l01FakeWorkbenchControlRepo) GetJobByID(_ context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error) {
	if f.job == nil || f.job.TenantID != tenantID || f.job.ID != id {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	copy := *f.job
	return &copy, nil
}

func (f *t2l01FakeWorkbenchControlRepo) CompareAndSwapJobState(context.Context, repository.WorkbenchJobStateCAS) (*types.WorkbenchJob, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *t2l01FakeWorkbenchControlRepo) BindJobBackendAndMarkRunning(context.Context, repository.WorkbenchJobBackendBindCAS) (*types.WorkbenchJob, error) {
	return nil, errors.New("not implemented in fake")
}
func (f *t2l01FakeWorkbenchControlRepo) CreateCommand(_ context.Context, command *types.WorkbenchCommand) (*types.WorkbenchCommand, error) {
	copy := *command
	f.commandInput = &copy
	copy.ID = "command-created"
	return &copy, nil
}

func (f *t2l01FakeWorkbenchControlRepo) CreateCommandIfJobRunning(ctx context.Context, input repository.WorkbenchCommandCreateInput) (*types.WorkbenchCommand, error) {
	f.atomicCommandCalls++
	if f.job == nil || f.job.State != types.WorkbenchJobStateRunning || f.job.ClosedAt != nil ||
		f.job.LeaseEpoch != input.ExpectedLeaseEpoch || f.job.StateVersion != input.ExpectedJobStateVersion {
		return nil, repository.ErrWorkbenchJobConflict
	}
	return f.CreateCommand(ctx, input.Command)
}

func (f *t2l01FakeWorkbenchControlRepo) GetActiveJobForSession(_ context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error) {
	if f.job == nil || f.job.TenantID != tenantID || f.job.ChatSessionID != chatSessionID ||
		f.job.WorkbenchSessionID != workbenchSessionID || f.job.ID != jobID ||
		f.job.LeaseEpoch != expectedLeaseEpoch || f.job.State == types.WorkbenchJobStateSucceeded ||
		f.job.State == types.WorkbenchJobStateFailed || f.job.State == types.WorkbenchJobStateCancelled ||
		f.job.State == types.WorkbenchJobStateLost || f.job.ClosedAt != nil {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	copy := *f.job
	return &copy, nil
}
func (f *t2l01FakeWorkbenchControlRepo) GetCommandByID(context.Context, uint64, string) (*types.WorkbenchCommand, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *t2l01FakeWorkbenchControlRepo) CompareAndSwapCommandState(context.Context, repository.WorkbenchCommandStateCAS) (*types.WorkbenchCommand, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *t2l01FakeWorkbenchControlRepo) AppendRunnerEvent(context.Context, *types.WorkbenchRunnerEvent) (*types.WorkbenchRunnerEvent, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *t2l01FakeWorkbenchControlRepo) ListRunnerEvents(context.Context, uint64, string, int64, int) ([]*types.WorkbenchRunnerEvent, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *t2l01FakeWorkbenchControlRepo) EnqueueAuditOutbox(context.Context, *types.WorkbenchAuditOutbox) (*types.WorkbenchAuditOutbox, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *t2l01FakeWorkbenchControlRepo) ListAuditOutbox(context.Context, uint64, types.WorkbenchAuditOutboxState, int) ([]*types.WorkbenchAuditOutbox, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *t2l01FakeWorkbenchControlRepo) ListAuditOutboxForJob(_ context.Context, tenantID uint64, jobID string, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error) {
	f.auditListCall = t2l07AuditListCall{tenantID: tenantID, jobID: jobID, state: state, limit: limit}
	return f.auditRows, nil
}
