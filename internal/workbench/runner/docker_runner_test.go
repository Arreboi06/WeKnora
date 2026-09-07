package runner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L02ProtectedDockerRunnerIsDefaultOff(t *testing.T) {
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	control := newFakeControlRepo()
	r := NewProtectedDockerRunner(Config{}, client, control)

	_, err := r.Start(context.Background(), StartRequest{TenantID: 10, JobID: "job-1"})

	require.ErrorIs(t, err, ErrRunnerDisabled)
	require.Empty(t, client.created)
	require.Empty(t, control.casJobs)
}

func TestT2L02ProtectedDockerRunnerStartsJobWithProtectedEnvelope(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID:                     "job-1",
		TenantID:               10,
		WorkbenchSessionID:     "wb-1",
		ChatSessionID:          "chat-1",
		IncarnationID:          "inc-1",
		LeaseEpoch:             7,
		State:                  types.WorkbenchJobStateQueued,
		StateVersion:           0,
		ResourcePolicySnapshot: types.JSONMap{"network": "none"},
		BackendType:            string(sandbox.SandboxTypeDocker),
		CreatedBy:              "user-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker, nextHandle: &fakeHandle{id: "container-1", provider: sandbox.SandboxTypeDocker}}
	r := NewProtectedDockerRunner(Config{
		Enabled:     true,
		TemplateID:  "weknora/workbench:test",
		IdleTimeout: time.Minute,
	}, client, control)

	started, err := r.Start(context.Background(), StartRequest{TenantID: 10, JobID: "job-1"})

	require.NoError(t, err)
	require.Equal(t, types.WorkbenchJobStateRunning, started.State)
	require.Equal(t, "container-1", started.BackendIdentity)
	require.Len(t, client.created, 1)
	create := client.created[0]
	require.Equal(t, "weknora/workbench:test", create.TemplateID)
	require.Equal(t, sandbox.RemoteTimeoutExplicit, create.Timeout.Mode)
	require.Equal(t, sandbox.RemoteOnTimeoutKill, create.Timeout.Action)
	require.NotNil(t, create.Network.AllowInternetAccess)
	require.False(t, *create.Network.AllowInternetAccess)
	require.NotNil(t, create.Network.AllowPublicTraffic)
	require.False(t, *create.Network.AllowPublicTraffic)
	require.Equal(t, map[string]string{
		"weknora.workbench.tenant_id":   "10",
		"weknora.workbench.job_id":      "job-1",
		"weknora.workbench.session_id":  "wb-1",
		"weknora.workbench.incarnation": "inc-1",
	}, create.Metadata)
	require.Equal(t, []repository.WorkbenchJobStateCAS{
		{
			TenantID:             10,
			ID:                   "job-1",
			ExpectedStateVersion: 0,
			ExpectedState:        types.WorkbenchJobStateQueued,
			NextState:            types.WorkbenchJobStateStarting,
		},
	}, control.casJobs)
	require.Equal(t, []repository.WorkbenchJobBackendBindCAS{
		{
			TenantID:             10,
			ID:                   "job-1",
			ExpectedStateVersion: 1,
			ExpectedState:        types.WorkbenchJobStateStarting,
			BackendIdentity:      "container-1",
		},
	}, control.binds)
	require.Equal(t, []string{"job_started"}, eventTypes(control.events))
}

func TestT2L09ProtectedDockerRunnerStartIsIdempotentForRunningJob(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1", IncarnationID: "inc-1",
		LeaseEpoch: 7, State: types.WorkbenchJobStateRunning, StateVersion: 2,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	started, err := r.Start(context.Background(), StartRequest{TenantID: 10, JobID: "job-1"})

	require.NoError(t, err)
	require.Equal(t, types.WorkbenchJobStateRunning, started.State)
	require.Equal(t, "container-1", started.BackendIdentity)
	require.Empty(t, client.created)
	require.Empty(t, control.casJobs)
}
func TestT2L02ProtectedDockerRunnerStartReturnsCleanupErrorAfterBindFailure(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.bindErr = repository.ErrWorkbenchJobStaleVersion
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1",
		State: types.WorkbenchJobStateQueued, StateVersion: 0,
		BackendType: string(sandbox.SandboxTypeDocker),
	}
	client := &fakeRemoteClient{
		provider:   sandbox.SandboxTypeDocker,
		nextHandle: &fakeHandle{id: "container-1", provider: sandbox.SandboxTypeDocker},
		deleteErr:  errors.New("delete failed"),
	}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	_, err := r.Start(context.Background(), StartRequest{TenantID: 10, JobID: "job-1"})

	require.ErrorIs(t, err, repository.ErrWorkbenchJobStaleVersion)
	require.ErrorContains(t, err, "delete failed")
	require.Equal(t, []string{"container-1"}, client.deleted)
}

func TestT2L02ProtectedDockerRunnerRejectsNonDockerAndDisabledDocker(t *testing.T) {
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.DockerBackendEnabledEnv, "")

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1",
		State: types.WorkbenchJobStateQueued, StateVersion: 0,
		BackendType: string(sandbox.SandboxTypeDocker),
	}
	docker := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, docker, control)

	_, err := r.Start(context.Background(), StartRequest{TenantID: 10, JobID: "job-1"})
	require.ErrorIs(t, err, sandbox.ErrDockerBackendDisabled)
	require.Empty(t, docker.created)

	nonDocker := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, &fakeRemoteClient{provider: sandbox.SandboxTypeE2B}, control)
	_, err = nonDocker.Start(context.Background(), StartRequest{TenantID: 10, JobID: "job-1"})
	require.ErrorIs(t, err, ErrUnsupportedBackend)
}

func TestT2L02ProtectedDockerRunnerRunsCommandAsSandboxUserAndAuditsTerminalState(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1",
		State: types.WorkbenchJobStateRunning, StateVersion: 2,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	control.commands["10/cmd-1"] = &types.WorkbenchCommand{
		ID: "cmd-1", TenantID: 10, WorkbenchJobID: "job-1", WorkbenchSessionID: "wb-1",
		Sequence: 3, Kind: "shell", State: types.WorkbenchCommandStateQueued, StateVersion: 0,
		Payload:   types.JSONMap{"command": "printf ok", "work_dir": "/workspace/project", "timeout_ms": float64(1500)},
		CreatedBy: "user-1",
	}
	client := &fakeRemoteClient{
		provider: sandbox.SandboxTypeDocker,
		execResult: &sandbox.RemoteExecResult{
			Stdout: "ok", ExitCode: 0, Duration: 10 * time.Millisecond,
		},
	}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	result, err := r.RunCommand(context.Background(), RunCommandRequest{TenantID: 10, CommandID: "cmd-1"})

	require.NoError(t, err)
	require.Equal(t, "ok", result.Stdout)
	require.Len(t, client.connected, 1)
	require.Equal(t, "container-1", client.connected[0])
	require.Equal(t, []sandbox.RemoteConnectRequest{{SandboxID: "container-1"}}, client.connectReqs)
	require.Len(t, client.execs, 1)
	exec := client.execs[0]
	require.True(t, exec.Shell)
	require.Empty(t, exec.Args)
	require.Equal(t, "printf ok", exec.Command)
	require.Equal(t, "/workspace/project", exec.WorkDir)
	require.Equal(t, sandbox.DefaultSandboxExecUser, exec.User)
	require.Equal(t, 1500*time.Millisecond, exec.Timeout)
	require.Equal(t, []types.WorkbenchCommandState{
		types.WorkbenchCommandStateRunning,
		types.WorkbenchCommandStateSucceeded,
	}, commandNextStates(control.casCommands))
	require.Equal(t, []types.WorkbenchAuditAction{types.WorkbenchAuditActionCommandSucceeded}, auditActions(control.audit))
	require.Equal(t, []string{"command_started", "command_finished"}, eventTypes(control.events))
}

func TestT2L02ProtectedDockerRunnerRejectsCrossTenantAndWorkspaceEscape(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.commands["10/cmd-1"] = &types.WorkbenchCommand{
		ID: "cmd-1", TenantID: 10, WorkbenchJobID: "job-1", WorkbenchSessionID: "wb-1",
		Sequence: 1, Kind: "shell", State: types.WorkbenchCommandStateQueued, StateVersion: 0,
		Payload:   types.JSONMap{"command": "pwd", "work_dir": "/etc"},
		CreatedBy: "user-1",
	}
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1",
		State: types.WorkbenchJobStateRunning, StateVersion: 1,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	_, err := r.RunCommand(context.Background(), RunCommandRequest{TenantID: 11, CommandID: "cmd-1"})
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)

	_, err = r.RunCommand(context.Background(), RunCommandRequest{TenantID: 10, CommandID: "cmd-1"})
	require.ErrorIs(t, err, ErrWorkspaceEscape)
	require.Empty(t, client.execs)
}

func TestT2L02ProtectedDockerRunnerCleanupDeletesSandboxAndClosesJob(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1",
		State: types.WorkbenchJobStateRunning, StateVersion: 4,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	err := r.Cleanup(context.Background(), CleanupRequest{
		TenantID:             10,
		JobID:                "job-1",
		ExpectedStateVersion: 4,
		TerminalState:        types.WorkbenchJobStateCancelled,
		Reason:               "user_cancelled",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"container-1"}, client.deleted)
	require.Equal(t, types.WorkbenchJobStateCancelled, control.casJobs[len(control.casJobs)-1].NextState)
	require.Equal(t, []string{"job_finished"}, eventTypes(control.events))
}

func TestT2L07ProtectedDockerRunnerCleanupAuditsResourceKill(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		State: types.WorkbenchJobStateRunning, StateVersion: 4,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	err := r.Cleanup(context.Background(), CleanupRequest{
		TenantID:             10,
		JobID:                "job-1",
		ExpectedStateVersion: 4,
		TerminalState:        types.WorkbenchJobStateLost,
		Reason:               "resource_exceeded",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"container-1"}, client.deleted)
	require.Len(t, control.audit, 1)
	require.Equal(t, types.WorkbenchAuditActionJobTerminated, control.audit[0].Action)
	require.Equal(t, "failure", control.audit[0].Outcome)
	require.Equal(t, "resource_exceeded", control.audit[0].Payload["reason"])
	require.Equal(t, true, control.audit[0].Payload["resource_exceeded"])
	require.NotContains(t, fmt.Sprint(control.audit[0].Payload), "container-1")
}
func TestT2L07ProtectedDockerRunnerCleanupRetriesAfterPartialFailure(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		State: types.WorkbenchJobStateRunning, StateVersion: 4,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker, deleteErrs: []error{errors.New("delete failed"), nil}}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	err := r.Cleanup(context.Background(), CleanupRequest{
		TenantID:             10,
		JobID:                "job-1",
		ExpectedStateVersion: 4,
		TerminalState:        types.WorkbenchJobStateLost,
		Reason:               "resource_exceeded",
	})
	require.ErrorContains(t, err, "delete failed")
	require.Equal(t, types.WorkbenchJobStateLost, control.jobs["10/job-1"].State)
	require.Equal(t, int64(5), control.jobs["10/job-1"].StateVersion)
	require.Empty(t, control.audit)

	err = r.Cleanup(context.Background(), CleanupRequest{
		TenantID:             10,
		JobID:                "job-1",
		ExpectedStateVersion: 4,
		TerminalState:        types.WorkbenchJobStateLost,
		Reason:               "resource_exceeded",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"container-1", "container-1"}, client.deleted)
	require.Len(t, control.audit, 1)
	require.Equal(t, types.WorkbenchAuditActionJobTerminated, control.audit[0].Action)
	require.Equal(t, "resource_exceeded", control.audit[0].Payload["reason"])
}

func TestT2L02ProtectedDockerRunnerCleanupCanRetryBackendDeleteForTerminalJob(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	closedAt := time.Now().UTC()
	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1",
		State: types.WorkbenchJobStateCancelled, StateVersion: 5, ClosedAt: &closedAt,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	err := r.Cleanup(context.Background(), CleanupRequest{
		TenantID:             10,
		JobID:                "job-1",
		ExpectedStateVersion: 5,
		TerminalState:        types.WorkbenchJobStateCancelled,
		Reason:               "retry_cleanup",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"container-1"}, client.deleted)
	require.Empty(t, control.casJobs)
	require.Empty(t, control.events)
}

func TestT2L02ProtectedDockerRunnerCleanupStaleVersionDoesNotDeleteSandbox(t *testing.T) {
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	control := newFakeControlRepo()
	control.jobs["10/job-1"] = &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1",
		State: types.WorkbenchJobStateRunning, StateVersion: 4,
		BackendType: string(sandbox.SandboxTypeDocker), BackendIdentity: "container-1",
	}
	client := &fakeRemoteClient{provider: sandbox.SandboxTypeDocker}
	r := NewProtectedDockerRunner(Config{Enabled: true, TemplateID: "tpl"}, client, control)

	err := r.Cleanup(context.Background(), CleanupRequest{
		TenantID:             10,
		JobID:                "job-1",
		ExpectedStateVersion: 99,
		TerminalState:        types.WorkbenchJobStateCancelled,
		Reason:               "stale",
	})

	require.ErrorIs(t, err, repository.ErrWorkbenchJobStaleVersion)
	require.Empty(t, client.deleted)
	require.Empty(t, control.events)
}

type fakeControlRepo struct {
	jobs        map[string]*types.WorkbenchJob
	commands    map[string]*types.WorkbenchCommand
	casJobs     []repository.WorkbenchJobStateCAS
	binds       []repository.WorkbenchJobBackendBindCAS
	casCommands []repository.WorkbenchCommandStateCAS
	events      []*types.WorkbenchRunnerEvent
	audit       []*types.WorkbenchAuditOutbox
	bindErr     error
	eventErrs   []error
	auditErrs   []error
}

func newFakeControlRepo() *fakeControlRepo {
	return &fakeControlRepo{
		jobs:     make(map[string]*types.WorkbenchJob),
		commands: make(map[string]*types.WorkbenchCommand),
	}
}

func (f *fakeControlRepo) ProbeSchema(context.Context) error { return nil }

func (f *fakeControlRepo) StartJobWithAudit(context.Context, repository.WorkbenchJobStartInput) (*types.WorkbenchJob, error) {
	return nil, errors.New("not used")
}

func (f *fakeControlRepo) GetJobByID(_ context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error) {
	job, ok := f.jobs[controlKey(tenantID, id)]
	if !ok {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	copy := *job
	return &copy, nil
}

func (f *fakeControlRepo) CompareAndSwapJobState(_ context.Context, input repository.WorkbenchJobStateCAS) (*types.WorkbenchJob, error) {
	f.casJobs = append(f.casJobs, input)
	job, ok := f.jobs[controlKey(input.TenantID, input.ID)]
	if !ok || job.StateVersion != input.ExpectedStateVersion || job.State != input.ExpectedState || job.ClosedAt != nil {
		return nil, repository.ErrWorkbenchJobStaleVersion
	}
	job.State = input.NextState
	job.StateVersion++
	job.TerminalReason = input.TerminalReason
	if input.NextState.IsTerminal() {
		now := time.Now().UTC()
		job.ClosedAt = &now
	}
	copy := *job
	return &copy, nil
}

func (f *fakeControlRepo) BindJobBackendAndMarkRunning(_ context.Context, input repository.WorkbenchJobBackendBindCAS) (*types.WorkbenchJob, error) {
	f.binds = append(f.binds, input)
	if f.bindErr != nil {
		return nil, f.bindErr
	}
	job, ok := f.jobs[controlKey(input.TenantID, input.ID)]
	if !ok || job.StateVersion != input.ExpectedStateVersion || job.State != input.ExpectedState || job.ClosedAt != nil {
		return nil, repository.ErrWorkbenchJobStaleVersion
	}
	job.BackendIdentity = input.BackendIdentity
	job.State = types.WorkbenchJobStateRunning
	job.StateVersion++
	copy := *job
	return &copy, nil
}
func (f *fakeControlRepo) CreateCommand(_ context.Context, command *types.WorkbenchCommand) (*types.WorkbenchCommand, error) {
	f.commands[controlKey(command.TenantID, command.ID)] = command
	return command, nil
}

func (f *fakeControlRepo) CreateCommandIfJobRunning(_ context.Context, input repository.WorkbenchCommandCreateInput) (*types.WorkbenchCommand, error) {
	job, ok := f.jobs[controlKey(input.Command.TenantID, input.Command.WorkbenchJobID)]
	if !ok || job.State != types.WorkbenchJobStateRunning || job.ClosedAt != nil ||
		job.LeaseEpoch != input.ExpectedLeaseEpoch || job.StateVersion != input.ExpectedJobStateVersion {
		return nil, repository.ErrWorkbenchJobConflict
	}
	return f.CreateCommand(context.Background(), input.Command)
}

func (f *fakeControlRepo) GetActiveJobForSession(_ context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error) {
	job, ok := f.jobs[controlKey(tenantID, jobID)]
	if !ok || job.ChatSessionID != chatSessionID || job.WorkbenchSessionID != workbenchSessionID ||
		job.LeaseEpoch != expectedLeaseEpoch || job.ClosedAt != nil {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	copy := *job
	return &copy, nil
}
func (f *fakeControlRepo) GetCommandByID(_ context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error) {
	command, ok := f.commands[controlKey(tenantID, id)]
	if !ok {
		return nil, repository.ErrWorkbenchCommandNotFound
	}
	copy := *command
	return &copy, nil
}

func (f *fakeControlRepo) CompareAndSwapCommandState(_ context.Context, input repository.WorkbenchCommandStateCAS) (*types.WorkbenchCommand, error) {
	f.casCommands = append(f.casCommands, input)
	command, ok := f.commands[controlKey(input.TenantID, input.ID)]
	if !ok || command.StateVersion != input.ExpectedStateVersion || command.State != input.ExpectedState || command.ClosedAt != nil {
		return nil, repository.ErrWorkbenchCommandStaleVersion
	}
	command.State = input.NextState
	command.StateVersion++
	command.TerminalReason = input.TerminalReason
	if input.NextState.IsTerminal() {
		now := time.Now().UTC()
		command.ClosedAt = &now
	}
	copy := *command
	return &copy, nil
}

func (f *fakeControlRepo) AppendRunnerEvent(_ context.Context, event *types.WorkbenchRunnerEvent) (*types.WorkbenchRunnerEvent, error) {
	if len(f.eventErrs) > 0 {
		err := f.eventErrs[0]
		f.eventErrs = f.eventErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	f.events = append(f.events, event)
	return event, nil
}

func (f *fakeControlRepo) ListRunnerEvents(context.Context, uint64, string, int64, int) ([]*types.WorkbenchRunnerEvent, error) {
	return nil, nil
}

func (f *fakeControlRepo) EnqueueAuditOutbox(_ context.Context, row *types.WorkbenchAuditOutbox) (*types.WorkbenchAuditOutbox, error) {
	if len(f.auditErrs) > 0 {
		err := f.auditErrs[0]
		f.auditErrs = f.auditErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	f.audit = append(f.audit, row)
	return row, nil
}

func (f *fakeControlRepo) ListAuditOutbox(context.Context, uint64, types.WorkbenchAuditOutboxState, int) ([]*types.WorkbenchAuditOutbox, error) {
	return nil, nil
}

func (f *fakeControlRepo) ListAuditOutboxForJob(context.Context, uint64, string, types.WorkbenchAuditOutboxState, int) ([]*types.WorkbenchAuditOutbox, error) {
	return nil, nil
}

type fakeRemoteClient struct {
	provider   sandbox.RemoteProvider
	nextHandle sandbox.RemoteSandboxHandle
	execResult *sandbox.RemoteExecResult
	execErr    error
	createErr  error
	deleteErr  error
	deleteErrs []error

	created     []sandbox.RemoteCreateRequest
	connected   []string
	connectReqs []sandbox.RemoteConnectRequest
	execs       []sandbox.RemoteExecRequest
	deleted     []string
}

func (f *fakeRemoteClient) Provider() sandbox.RemoteProvider { return f.provider }
func (f *fakeRemoteClient) Capabilities() sandbox.RemoteSandboxCapabilities {
	return sandbox.RemoteSandboxCapabilities{SupportsReconnect: true, SupportsMetadata: true, SupportsListSandboxes: true}
}
func (f *fakeRemoteClient) Health(context.Context) error { return nil }
func (f *fakeRemoteClient) Create(_ context.Context, req sandbox.RemoteCreateRequest) (sandbox.RemoteSandboxHandle, error) {
	f.created = append(f.created, req)
	if f.createErr != nil {
		return nil, f.createErr
	}
	if f.nextHandle != nil {
		return f.nextHandle, nil
	}
	return &fakeHandle{id: "sandbox-1", provider: f.provider}, nil
}
func (f *fakeRemoteClient) Connect(_ context.Context, request sandbox.RemoteConnectRequest) (sandbox.RemoteSandboxHandle, error) {
	f.connectReqs = append(f.connectReqs, request)
	f.connected = append(f.connected, request.SandboxID)
	return &fakeHandle{id: request.SandboxID, provider: f.provider}, nil
}
func (f *fakeRemoteClient) Get(context.Context, string) (*sandbox.RemoteSandboxSummary, error) {
	return nil, nil
}
func (f *fakeRemoteClient) List(context.Context, sandbox.RemoteListFilter) ([]sandbox.RemoteSandboxSummary, error) {
	return nil, nil
}
func (f *fakeRemoteClient) Delete(_ context.Context, sandboxID string) error {
	f.deleted = append(f.deleted, sandboxID)
	if len(f.deleteErrs) > 0 {
		err := f.deleteErrs[0]
		f.deleteErrs = f.deleteErrs[1:]
		if err != nil {
			return err
		}
	}
	return f.deleteErr
}
func (f *fakeRemoteClient) Exec(_ context.Context, _ sandbox.RemoteSandboxHandle, req sandbox.RemoteExecRequest) (*sandbox.RemoteExecResult, error) {
	f.execs = append(f.execs, req)
	if f.execErr != nil {
		return nil, f.execErr
	}
	if f.execResult != nil {
		return f.execResult, nil
	}
	return &sandbox.RemoteExecResult{ExitCode: 0}, nil
}
func (f *fakeRemoteClient) WriteFile(context.Context, sandbox.RemoteSandboxHandle, string, []byte) error {
	return nil
}
func (f *fakeRemoteClient) ReadFile(context.Context, sandbox.RemoteSandboxHandle, string) ([]byte, error) {
	return nil, nil
}
func (f *fakeRemoteClient) ListDir(context.Context, sandbox.RemoteSandboxHandle, string) ([]sandbox.RemoteDirEntry, error) {
	return nil, nil
}
func (f *fakeRemoteClient) MakeDir(context.Context, sandbox.RemoteSandboxHandle, string) error {
	return nil
}
func (f *fakeRemoteClient) Remove(context.Context, sandbox.RemoteSandboxHandle, string) error {
	return nil
}
func (f *fakeRemoteClient) Stat(context.Context, sandbox.RemoteSandboxHandle, string) (*sandbox.RemoteStatEntry, error) {
	return nil, nil
}

type fakeHandle struct {
	id       string
	provider sandbox.RemoteProvider
}

func (h *fakeHandle) ID() string                       { return h.id }
func (h *fakeHandle) Provider() sandbox.RemoteProvider { return h.provider }
func (h *fakeHandle) Metadata() map[string]string      { return nil }
func controlKey(tenantID uint64, id string) string {
	return strconv.FormatUint(tenantID, 10) + "/" + id
}
func eventTypes(events []*types.WorkbenchRunnerEvent) []string {
	result := make([]string, 0, len(events))
	for _, event := range events {
		result = append(result, event.EventType)
	}
	return result
}
func commandNextStates(cases []repository.WorkbenchCommandStateCAS) []types.WorkbenchCommandState {
	result := make([]types.WorkbenchCommandState, 0, len(cases))
	for _, c := range cases {
		result = append(result, c.NextState)
	}
	return result
}
func auditActions(rows []*types.WorkbenchAuditOutbox) []types.WorkbenchAuditAction {
	result := make([]types.WorkbenchAuditAction, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.Action)
	}
	return result
}
