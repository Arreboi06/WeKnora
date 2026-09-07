package runner

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

var (
	ErrRunnerDisabled     = errors.New("workbench runner is disabled")
	ErrUnsupportedBackend = errors.New("workbench runner requires docker backend")
	ErrInvalidRequest     = errors.New("workbench runner invalid request")
	ErrWorkspaceEscape    = errors.New("workbench command work_dir escapes /workspace")
	ErrJobNotRunning      = errors.New("workbench job is not running")
	ErrUnsupportedCommand = errors.New("workbench command kind is unsupported")
	ErrNoBackendIdentity  = errors.New("workbench job has no backend identity")
)

const EligibleProtectedDockerBackend = types.WorkbenchBackendLocalProtectedDocker

type Config struct {
	Enabled               bool
	TemplateID            string
	IdleTimeout           time.Duration
	DefaultCommandTimeout time.Duration
}

type ProtectedDockerRunner struct {
	cfg     Config
	client  sandbox.RemoteSandboxClient
	control repository.WorkbenchControlRepository
}

type StartRequest struct {
	TenantID uint64
	JobID    string
}

type RunCommandRequest struct {
	TenantID  uint64
	CommandID string
}

type CleanupRequest struct {
	TenantID             uint64
	JobID                string
	ExpectedStateVersion int64
	TerminalState        types.WorkbenchJobState
	Reason               string
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	Killed   bool
}

func NewProtectedDockerRunner(
	cfg Config,
	client sandbox.RemoteSandboxClient,
	control repository.WorkbenchControlRepository,
) *ProtectedDockerRunner {
	return &ProtectedDockerRunner{cfg: cfg, client: client, control: control}
}

func (r *ProtectedDockerRunner) EligibleBackendName() (string, bool) {
	if err := r.requireReady(); err != nil || strings.TrimSpace(r.cfg.TemplateID) == "" {
		return "", false
	}
	return EligibleProtectedDockerBackend, true
}

func (r *ProtectedDockerRunner) Start(ctx context.Context, req StartRequest) (*types.WorkbenchJob, error) {
	if err := r.requireReady(); err != nil {
		return nil, err
	}
	if req.TenantID == 0 || strings.TrimSpace(req.JobID) == "" || strings.TrimSpace(r.cfg.TemplateID) == "" {
		return nil, ErrInvalidRequest
	}
	if err := sandbox.EnsureDockerBackendAllowed(sandbox.SandboxTypeDocker); err != nil {
		return nil, err
	}

	job, err := r.control.GetJobByID(ctx, req.TenantID, strings.TrimSpace(req.JobID))
	if err != nil {
		return nil, err
	}
	activeJob, err := r.control.GetActiveJobForSession(ctx, req.TenantID, job.ChatSessionID, job.WorkbenchSessionID, job.ID, job.LeaseEpoch)
	if err != nil {
		return nil, err
	}
	job = activeJob
	if err := requireDockerJob(job); err != nil {
		return nil, err
	}
	if job.ClosedAt != nil {
		return nil, repository.ErrWorkbenchJobConflict
	}
	// Start is safe to retry after another request has won the queue fence.
	// Return the durable in-flight result instead of creating a second backend.
	if job.State == types.WorkbenchJobStateStarting || job.State == types.WorkbenchJobStateRunning {
		return job, nil
	}
	if job.State != types.WorkbenchJobStateQueued {
		return nil, repository.ErrWorkbenchJobConflict
	}

	starting, err := r.control.CompareAndSwapJobState(ctx, repository.WorkbenchJobStateCAS{
		TenantID:             req.TenantID,
		ID:                   job.ID,
		ExpectedStateVersion: job.StateVersion,
		ExpectedState:        types.WorkbenchJobStateQueued,
		NextState:            types.WorkbenchJobStateStarting,
	})
	if err != nil {
		return nil, err
	}

	handle, err := r.client.Create(ctx, protectedCreateRequest(r.cfg, starting))
	if err != nil {
		_, _ = r.control.CompareAndSwapJobState(ctx, repository.WorkbenchJobStateCAS{
			TenantID:             req.TenantID,
			ID:                   job.ID,
			ExpectedStateVersion: starting.StateVersion,
			ExpectedState:        types.WorkbenchJobStateStarting,
			NextState:            types.WorkbenchJobStateLost,
			TerminalReason:       "backend_create_failed",
		})
		return nil, err
	}

	running, err := r.control.BindJobBackendAndMarkRunning(ctx, repository.WorkbenchJobBackendBindCAS{
		TenantID:             req.TenantID,
		ID:                   job.ID,
		ExpectedStateVersion: starting.StateVersion,
		ExpectedState:        types.WorkbenchJobStateStarting,
		BackendIdentity:      handle.ID(),
	})
	if err != nil {
		if deleteErr := r.deleteBackendIdentity(context.WithoutCancel(ctx), handle.ID()); deleteErr != nil {
			return nil, errors.Join(err, deleteErr)
		}
		return nil, err
	}
	if _, err := r.control.AppendRunnerEvent(ctx, &types.WorkbenchRunnerEvent{
		TenantID:           req.TenantID,
		WorkbenchJobID:     running.ID,
		WorkbenchSessionID: running.WorkbenchSessionID,
		Seq:                1,
		EventType:          "job_started",
		Payload: types.JSONMap{
			"backend_type": running.BackendType,
		},
	}); err != nil {
		return nil, err
	}
	return running, nil
}

func (r *ProtectedDockerRunner) RunCommand(ctx context.Context, req RunCommandRequest) (*CommandResult, error) {
	if err := r.requireReady(); err != nil {
		return nil, err
	}
	if req.TenantID == 0 || strings.TrimSpace(req.CommandID) == "" {
		return nil, ErrInvalidRequest
	}
	if err := sandbox.EnsureDockerBackendAllowed(sandbox.SandboxTypeDocker); err != nil {
		return nil, err
	}

	command, err := r.control.GetCommandByID(ctx, req.TenantID, strings.TrimSpace(req.CommandID))
	if err != nil {
		return nil, err
	}
	if command.Kind != "shell" {
		return nil, ErrUnsupportedCommand
	}
	job, err := r.control.GetJobByID(ctx, req.TenantID, command.WorkbenchJobID)
	if err != nil {
		return nil, err
	}
	activeJob, err := r.control.GetActiveJobForSession(ctx, req.TenantID, job.ChatSessionID, job.WorkbenchSessionID, job.ID, job.LeaseEpoch)
	if err != nil {
		return nil, err
	}
	job = activeJob
	if err := requireDockerJob(job); err != nil {
		return nil, err
	}
	if job.State != types.WorkbenchJobStateRunning || job.ClosedAt != nil {
		return nil, ErrJobNotRunning
	}
	if strings.TrimSpace(job.BackendIdentity) == "" {
		return nil, ErrNoBackendIdentity
	}

	execReq, err := commandExecRequest(command, r.cfg.DefaultCommandTimeout)
	if err != nil {
		return nil, err
	}
	running, err := r.control.CompareAndSwapCommandState(ctx, repository.WorkbenchCommandStateCAS{
		TenantID:             req.TenantID,
		ID:                   command.ID,
		ExpectedStateVersion: command.StateVersion,
		ExpectedState:        types.WorkbenchCommandStateQueued,
		NextState:            types.WorkbenchCommandStateRunning,
	})
	if err != nil {
		return nil, err
	}
	if _, err := r.control.AppendRunnerEvent(ctx, commandEvent(command, "command_started", command.Sequence*10+1, nil)); err != nil {
		return nil, err
	}
	handle, err := r.client.Connect(ctx, sandbox.RemoteConnectRequest{
		SandboxID: strings.TrimSpace(job.BackendIdentity),
	})
	if err != nil {
		_, casErr := r.control.CompareAndSwapCommandState(ctx, repository.WorkbenchCommandStateCAS{
			TenantID:             req.TenantID,
			ID:                   command.ID,
			ExpectedStateVersion: running.StateVersion,
			ExpectedState:        types.WorkbenchCommandStateRunning,
			NextState:            types.WorkbenchCommandStateLost,
			TerminalReason:       "backend_connect_failed",
		})
		if casErr != nil {
			return nil, errors.Join(err, casErr)
		}
		_, eventErr := r.control.AppendRunnerEvent(ctx, commandEvent(command, "command_finished", command.Sequence*10+2, types.JSONMap{"error": err.Error()}))
		if eventErr != nil {
			return nil, errors.Join(err, eventErr)
		}
		return nil, err
	}

	execResult, err := r.client.Exec(ctx, handle, execReq)
	if err != nil {
		_, casErr := r.control.CompareAndSwapCommandState(ctx, repository.WorkbenchCommandStateCAS{
			TenantID:             req.TenantID,
			ID:                   command.ID,
			ExpectedStateVersion: running.StateVersion,
			ExpectedState:        types.WorkbenchCommandStateRunning,
			NextState:            types.WorkbenchCommandStateLost,
			TerminalReason:       "backend_exec_failed",
		})
		if casErr != nil {
			return nil, errors.Join(err, casErr)
		}
		_, eventErr := r.control.AppendRunnerEvent(ctx, commandEvent(command, "command_finished", command.Sequence*10+2, types.JSONMap{"error": err.Error()}))
		if eventErr != nil {
			return nil, errors.Join(err, eventErr)
		}
		return nil, err
	}
	if execResult == nil {
		execResult = &sandbox.RemoteExecResult{ExitCode: -1, Stderr: "empty runner result"}
	}

	nextState := types.WorkbenchCommandStateSucceeded
	reason := ""
	action := types.WorkbenchAuditActionCommandSucceeded
	outcome := "success"
	if execResult.ExitCode != 0 || execResult.Killed {
		nextState = types.WorkbenchCommandStateFailed
		action = types.WorkbenchAuditActionCommandFailed
		outcome = "failure"
		if execResult.Killed {
			reason = "killed"
		} else {
			reason = "nonzero_exit"
		}
	}
	terminal, err := r.control.CompareAndSwapCommandState(ctx, repository.WorkbenchCommandStateCAS{
		TenantID:             req.TenantID,
		ID:                   command.ID,
		ExpectedStateVersion: running.StateVersion,
		ExpectedState:        types.WorkbenchCommandStateRunning,
		NextState:            nextState,
		TerminalReason:       reason,
	})
	if err != nil {
		return nil, err
	}
	if _, err := r.control.AppendRunnerEvent(ctx, commandEvent(command, "command_finished", command.Sequence*10+2, types.JSONMap{
		"exit_code": float64(execResult.ExitCode),
		"killed":    execResult.Killed,
		"state":     string(terminal.State),
	})); err != nil {
		return nil, err
	}
	if _, err := r.control.EnqueueAuditOutbox(ctx, &types.WorkbenchAuditOutbox{
		TenantID:           req.TenantID,
		WorkbenchSessionID: command.WorkbenchSessionID,
		WorkbenchJobID:     command.WorkbenchJobID,
		CommandID:          command.ID,
		Action:             action,
		ActorUserID:        command.CreatedBy,
		Outcome:            outcome,
		Payload: types.JSONMap{
			"exit_code": float64(execResult.ExitCode),
			"killed":    execResult.Killed,
		},
		IdempotencyKey: "workbench.command.terminal:" + command.ID,
		State:          types.WorkbenchAuditOutboxStatePending,
	}); err != nil {
		return nil, err
	}

	return &CommandResult{
		Stdout:   execResult.Stdout,
		Stderr:   execResult.Stderr,
		ExitCode: execResult.ExitCode,
		Duration: execResult.Duration,
		Killed:   execResult.Killed,
	}, nil
}

func (r *ProtectedDockerRunner) Cleanup(ctx context.Context, req CleanupRequest) error {
	if err := r.requireReady(); err != nil {
		return err
	}
	if req.TenantID == 0 || strings.TrimSpace(req.JobID) == "" ||
		req.ExpectedStateVersion < 0 || !req.TerminalState.IsTerminal() {
		return ErrInvalidRequest
	}
	if err := sandbox.EnsureDockerBackendAllowed(sandbox.SandboxTypeDocker); err != nil {
		return err
	}
	job, err := r.control.GetJobByID(ctx, req.TenantID, strings.TrimSpace(req.JobID))
	if err != nil {
		return err
	}
	if err := requireDockerJob(job); err != nil {
		return err
	}
	if job.State.IsTerminal() || job.ClosedAt != nil {
		if req.TerminalState != job.State || !workbenchTerminalCleanupRetryVersion(req.ExpectedStateVersion, job.StateVersion) {
			return repository.ErrWorkbenchJobStaleVersion
		}
		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			reason = strings.TrimSpace(job.TerminalReason)
		}
		if err := r.deleteBackendIdentity(ctx, job.BackendIdentity); err != nil {
			return err
		}
		return r.enqueueJobTerminalAudit(ctx, job, job.State, reason)
	}
	closed, err := r.control.CompareAndSwapJobState(ctx, repository.WorkbenchJobStateCAS{
		TenantID:             req.TenantID,
		ID:                   job.ID,
		ExpectedStateVersion: req.ExpectedStateVersion,
		ExpectedState:        job.State,
		NextState:            req.TerminalState,
		TerminalReason:       strings.TrimSpace(req.Reason),
	})
	if err != nil {
		return err
	}
	if err := r.deleteBackendIdentity(ctx, job.BackendIdentity); err != nil {
		return err
	}
	if _, err := r.control.AppendRunnerEvent(ctx, &types.WorkbenchRunnerEvent{
		TenantID:           req.TenantID,
		WorkbenchJobID:     job.ID,
		WorkbenchSessionID: job.WorkbenchSessionID,
		Seq:                9_000_000 + closed.StateVersion,
		EventType:          "job_finished",
		Payload: types.JSONMap{
			"state":  string(closed.State),
			"reason": strings.TrimSpace(req.Reason),
		},
	}); err != nil {
		return err
	}
	return r.enqueueJobTerminalAudit(ctx, closed, closed.State, strings.TrimSpace(req.Reason))
}

func (r *ProtectedDockerRunner) enqueueJobTerminalAudit(ctx context.Context, job *types.WorkbenchJob, state types.WorkbenchJobState, reason string) error {
	if job == nil {
		return repository.ErrWorkbenchJobNotFound
	}
	payload := types.JSONMap{
		"terminal_state":    string(state),
		"resource_exceeded": workbenchResourceExceeded(reason),
	}
	if trimmed := strings.TrimSpace(reason); trimmed != "" {
		payload["reason"] = trimmed
	}
	_, err := r.control.EnqueueAuditOutbox(ctx, &types.WorkbenchAuditOutbox{
		TenantID:           job.TenantID,
		WorkbenchSessionID: job.WorkbenchSessionID,
		WorkbenchJobID:     job.ID,
		Action:             types.WorkbenchAuditActionJobTerminated,
		ActorUserID:        strings.TrimSpace(job.CreatedBy),
		Outcome:            workbenchJobTerminalOutcome(state),
		Payload:            payload,
		IdempotencyKey:     "workbench.job.terminal:" + job.ID,
		State:              types.WorkbenchAuditOutboxStatePending,
	})
	return err
}

func workbenchTerminalCleanupRetryVersion(expected, current int64) bool {
	return expected == current || expected+1 == current
}

func workbenchJobTerminalOutcome(state types.WorkbenchJobState) string {
	if state == types.WorkbenchJobStateSucceeded {
		return "success"
	}
	if state == types.WorkbenchJobStateCancelled {
		return "cancelled"
	}
	return "failure"
}

func workbenchResourceExceeded(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "resource_exceeded")
}

func (r *ProtectedDockerRunner) deleteBackendIdentity(ctx context.Context, backendIdentity string) error {
	trimmed := strings.TrimSpace(backendIdentity)
	if trimmed == "" {
		return nil
	}
	if err := r.client.Delete(ctx, trimmed); err != nil && !sandbox.IsRemoteNotFound(err) {
		return err
	}
	return nil
}

func (r *ProtectedDockerRunner) requireReady() error {
	if r == nil || !r.cfg.Enabled {
		return ErrRunnerDisabled
	}
	if r.client == nil || r.control == nil || r.client.Provider() != sandbox.SandboxTypeDocker {
		return ErrUnsupportedBackend
	}
	return nil
}

func requireDockerJob(job *types.WorkbenchJob) error {
	if job == nil {
		return repository.ErrWorkbenchJobNotFound
	}
	if strings.TrimSpace(job.BackendType) != string(sandbox.SandboxTypeDocker) {
		return ErrUnsupportedBackend
	}
	return nil
}

func protectedCreateRequest(cfg Config, job *types.WorkbenchJob) sandbox.RemoteCreateRequest {
	no := false
	return sandbox.RemoteCreateRequest{
		TemplateID: strings.TrimSpace(cfg.TemplateID),
		Timeout: sandbox.RemoteTimeoutPolicy{
			Mode:   sandbox.RemoteTimeoutExplicit,
			Value:  effectiveIdleTimeout(cfg.IdleTimeout),
			Action: sandbox.RemoteOnTimeoutKill,
		},
		Metadata: map[string]string{
			"weknora.workbench.tenant_id":   strconv.FormatUint(job.TenantID, 10),
			"weknora.workbench.job_id":      job.ID,
			"weknora.workbench.session_id":  job.WorkbenchSessionID,
			"weknora.workbench.incarnation": job.IncarnationID,
		},
		Network: sandbox.RemoteNetworkPolicy{
			AllowInternetAccess: &no,
			AllowPublicTraffic:  &no,
		},
	}
}

func commandExecRequest(command *types.WorkbenchCommand, defaultTimeout time.Duration) (sandbox.RemoteExecRequest, error) {
	raw, _ := command.Payload["command"].(string)
	if strings.TrimSpace(raw) == "" {
		return sandbox.RemoteExecRequest{}, ErrInvalidRequest
	}
	workDir, err := cleanWorkbenchWorkDir(valueString(command.Payload["work_dir"]))
	if err != nil {
		return sandbox.RemoteExecRequest{}, err
	}
	timeout := durationFromMillis(command.Payload["timeout_ms"])
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return sandbox.RemoteExecRequest{
		Command: raw,
		Shell:   true,
		Stdin:   valueString(command.Payload["stdin"]),
		Env:     stringMapValue(command.Payload["env"]),
		WorkDir: workDir,
		User:    sandbox.DefaultSandboxExecUser,
		Timeout: timeout,
	}, nil
}

func cleanWorkbenchWorkDir(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return sandbox.SessionWorkspaceRoot, nil
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = path.Join(sandbox.SessionWorkspaceRoot, trimmed)
	}
	clean := path.Clean(trimmed)
	if clean != sandbox.SessionWorkspaceRoot && !strings.HasPrefix(clean, sandbox.SessionWorkspaceRoot+"/") {
		return "", ErrWorkspaceEscape
	}
	return clean, nil
}

func commandEvent(command *types.WorkbenchCommand, eventType string, seq int64, payload types.JSONMap) *types.WorkbenchRunnerEvent {
	if payload == nil {
		payload = types.JSONMap{}
	}
	return &types.WorkbenchRunnerEvent{
		TenantID:           command.TenantID,
		WorkbenchJobID:     command.WorkbenchJobID,
		WorkbenchSessionID: command.WorkbenchSessionID,
		CommandID:          command.ID,
		Seq:                seq,
		EventType:          eventType,
		Payload:            payload,
	}
}

func effectiveIdleTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return 30 * time.Minute
}

func durationFromMillis(value any) time.Duration {
	switch v := value.(type) {
	case int:
		return time.Duration(v) * time.Millisecond
	case int64:
		return time.Duration(v) * time.Millisecond
	case float64:
		return time.Duration(v) * time.Millisecond
	case json.Number:
		n, err := strconv.ParseInt(v.String(), 10, 64)
		if err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 0
}

func valueString(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func stringMapValue(value any) map[string]string {
	switch raw := value.(type) {
	case map[string]string:
		result := make(map[string]string, len(raw))
		for key, val := range raw {
			if strings.TrimSpace(key) != "" {
				result[key] = val
			}
		}
		return result
	case map[string]any:
		result := make(map[string]string, len(raw))
		for key, val := range raw {
			if strings.TrimSpace(key) == "" {
				continue
			}
			if text, ok := val.(string); ok {
				result[key] = text
			}
		}
		return result
	case types.JSONMap:
		result := make(map[string]string, len(raw))
		for key, val := range raw {
			if strings.TrimSpace(key) == "" {
				continue
			}
			if text, ok := val.(string); ok {
				result[key] = text
			}
		}
		return result
	}
	return nil
}
