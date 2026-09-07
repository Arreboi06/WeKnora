package handler

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	workbenchrunner "github.com/Tencent/WeKnora/internal/workbench/runner"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestT2L03WorkbenchStartIsDefaultOffAndDoesNotTouchServices(t *testing.T) {
	t.Setenv(sandbox.WorkbenchEnabledEnv, "")

	sessions := newT2L03SessionAuthorizer()
	workbenches := &t2l03WorkbenchSessions{}
	control := &t2l03ControlService{}
	runner := &t2l03Runner{}
	h := newWorkbenchHandler(sessions, workbenches, control, runner, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/start", map[string]any{
		"incarnation_id":    "inc-1",
		"sandbox_config_id": "sandbox-1",
		"backend_type":      "docker",
	}))

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, "unsupported", t2l03ResponseCode(t, w))
	require.Empty(t, sessions.calls)
	require.Empty(t, workbenches.createInputs)
	require.Empty(t, control.startInputs)
	require.Empty(t, runner.startRequests)
}

func TestT2L09ProductionWorkbenchHandlerRejectsMissingCapabilityProvider(t *testing.T) {
	t2l03EnableWorkbench(t)

	h := NewWorkbenchHandler(
		&t2l09SessionService{session: &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}},
		service.NewWorkbenchSessionService(nil, nil),
		service.NewWorkbenchControlService(nil, nil),
		nil,
	)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/start", nil)
	c.Params = gin.Params{{Key: "session_id", Value: "chat-1"}}
	c.Set(types.TenantIDContextKey.String(), uint64(10))
	c.Set(types.UserIDContextKey.String(), "actor-1")

	_, _, _, _, ok := h.authorizeWorkbench(c)

	require.False(t, ok)
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, "unsupported", t2l03ResponseCode(t, w))
}

type t2l09SessionService struct {
	interfaces.SessionService
	session *types.Session
}

func (s *t2l09SessionService) GetOwnedSession(context.Context, string) (*types.Session, error) {
	return s.session, nil
}

type t2l09CapabilityProvider struct {
	snapshot service.WorkbenchCapabilitySnapshot
}

func (p *t2l09CapabilityProvider) Snapshot(context.Context, bool) service.WorkbenchCapabilitySnapshot {
	return p.snapshot
}

func TestT2L09WorkbenchStartRejectsUnlistedBackendConfiguration(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{
		created: &types.WorkbenchSession{
			ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1",
			SandboxConfigID: "forged-backend", BackendType: "docker",
			State: types.WorkbenchStateProvisioning, StateVersion: 0, LeaseEpoch: 0,
			CreatedBy: "actor-1",
		},
		ready: &types.WorkbenchSession{
			ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1",
			SandboxConfigID: "forged-backend", BackendType: "docker",
			State: types.WorkbenchStateReady, StateVersion: 1, LeaseEpoch: 0,
			CreatedBy: "actor-1",
		},
	}
	capability := &t2l09CapabilityProvider{snapshot: service.WorkbenchCapabilitySnapshot{
		Supported: true, EligibleBackends: []string{workbenchrunner.EligibleProtectedDockerBackend},
	}}
	h := newWorkbenchHandler(
		sessions,
		workbenches,
		&t2l03ControlService{},
		nil,
		NewWorkbenchStreamTicketStore(time.Minute),
		capability,
	)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/start", map[string]any{
		"incarnation_id":    "inc-1",
		"sandbox_config_id": "forged-backend",
		"backend_type":      "docker",
	}))

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, "unsupported", t2l03ResponseCode(t, w))
	require.Empty(t, workbenches.createInputs)
}

func TestT2L10WorkbenchStartRejectsNamedConfigWithoutConfigBoundRunner(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{
		created: &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1", State: types.WorkbenchStateProvisioning},
		ready:   &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1", State: types.WorkbenchStateReady, StateVersion: 1},
	}
	h := newWorkbenchHandler(
		sessions,
		workbenches,
		&t2l03ControlService{},
		nil,
		NewWorkbenchStreamTicketStore(time.Minute),
		&t2l09CapabilityProvider{snapshot: service.WorkbenchCapabilitySnapshot{
			Supported: true, EligibleBackends: []string{workbenchrunner.EligibleProtectedDockerBackend},
		}},
	)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	wrongBackend := httptest.NewRecorder()
	r.ServeHTTP(wrongBackend, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/start", map[string]any{
		"incarnation_id": "inc-1", "sandbox_config_id": "cfg-1", "backend_type": "remote",
	}))
	require.Equal(t, http.StatusBadRequest, wrongBackend.Code)
	require.Empty(t, workbenches.createInputs)

	named := httptest.NewRecorder()
	r.ServeHTTP(named, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/start", map[string]any{
		"incarnation_id": "inc-1", "sandbox_config_id": "cfg-1", "backend_type": "docker",
	}))
	require.Equal(t, http.StatusNotFound, named.Code, named.Body.String())
	require.Equal(t, workbenchErrorUnsupported, t2l03ResponseCode(t, named))
	require.Empty(t, workbenches.createInputs)
}

func TestT2L10WorkbenchStartRequiresExplicitBackendType(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{
		created: &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1", State: types.WorkbenchStateProvisioning},
		ready:   &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1", State: types.WorkbenchStateReady, StateVersion: 1},
	}
	h := newWorkbenchHandler(
		sessions,
		workbenches,
		&t2l03ControlService{},
		nil,
		NewWorkbenchStreamTicketStore(time.Minute),
		&t2l09CapabilityProvider{snapshot: service.WorkbenchCapabilitySnapshot{
			Supported: true, EligibleBackends: []string{workbenchrunner.EligibleProtectedDockerBackend},
		}},
	)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/start", map[string]any{
		"incarnation_id": "inc-1", "sandbox_config_id": types.WorkbenchBackendLocalProtectedDocker,
	}))

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, workbenchErrorMalformedRequest, t2l03ResponseCode(t, w))
	require.Empty(t, workbenches.createInputs)
}
func TestT2L10WorkbenchLocalProfilePersistsTenantScopedConfigIdentity(t *testing.T) {
	t2l03EnableWorkbench(t)

	startForTenant := func(tenantID uint64) string {
		chatID := "chat-" + strconv.FormatUint(tenantID, 10)
		actorID := "actor-" + strconv.FormatUint(tenantID, 10)
		sessions := newT2L03SessionAuthorizer()
		sessions.rows[strconv.FormatUint(tenantID, 10)+"/"+chatID] = &types.Session{
			ID: chatID, TenantID: tenantID, UserID: actorID,
		}
		workbenches := &t2l03WorkbenchSessions{
			created: &types.WorkbenchSession{ID: "wb-1", TenantID: tenantID, ChatSessionID: chatID, IncarnationID: "inc-1", State: types.WorkbenchStateProvisioning},
			ready:   &types.WorkbenchSession{ID: "wb-1", TenantID: tenantID, ChatSessionID: chatID, IncarnationID: "inc-1", State: types.WorkbenchStateReady, StateVersion: 1},
		}
		h := newWorkbenchHandler(
			sessions,
			workbenches,
			&t2l03ControlService{},
			nil,
			NewWorkbenchStreamTicketStore(time.Minute),
			&t2l09CapabilityProvider{snapshot: service.WorkbenchCapabilitySnapshot{
				Supported: true, EligibleBackends: []string{workbenchrunner.EligibleProtectedDockerBackend},
			}},
		)
		r := t2l03WorkbenchRouter(h, tenantID, actorID)

		response := httptest.NewRecorder()
		r.ServeHTTP(response, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/start", map[string]any{
			"incarnation_id":    "inc-1",
			"sandbox_config_id": types.WorkbenchBackendLocalProtectedDocker,
			"backend_type":      "docker",
		}))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Len(t, workbenches.createInputs, 1)
		return workbenches.createInputs[0].SandboxConfigID
	}

	tenantAConfigID := startForTenant(10)
	tenantBConfigID := startForTenant(11)
	require.Len(t, tenantAConfigID, 36)
	require.Len(t, tenantBConfigID, 36)
	require.NotEqual(t, types.WorkbenchBackendLocalProtectedDocker, tenantAConfigID)
	require.NotEqual(t, tenantAConfigID, tenantBConfigID)
}

func TestT2L03WorkbenchStartRequiresOwnedSessionAndTransitionsReady(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{
		created: &types.WorkbenchSession{
			ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1",
			SandboxConfigID: "sandbox-1", BackendType: "docker", State: types.WorkbenchStateProvisioning,
			StateVersion: 0, LeaseEpoch: 0, CreatedBy: "actor-1",
		},
		ready: &types.WorkbenchSession{
			ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1",
			SandboxConfigID: "sandbox-1", BackendType: "docker", State: types.WorkbenchStateReady,
			StateVersion: 1, LeaseEpoch: 0, CreatedBy: "actor-1",
		},
	}
	h := newWorkbenchHandler(sessions, workbenches, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/start", map[string]any{
		"incarnation_id":      "inc-1",
		"sandbox_config_id":   "sandbox-1",
		"backend_type":        "docker",
		"capability_snapshot": map[string]any{"sandbox.workbench": false, "forged": true},
		"policy_snapshot":     map[string]any{"network": "host", "max_seconds": 999999},
	}))

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "wb-1", t2l03ResponseData(t, w)["id"])
	require.Equal(t, "READY", t2l03ResponseData(t, w)["state"])
	require.Len(t, sessions.calls, 1)
	require.Equal(t, service.CreateWorkbenchSessionInput{
		TenantID:           10,
		ChatSessionID:      "chat-1",
		Purpose:            types.WorkbenchPurpose,
		IncarnationID:      "inc-1",
		SandboxConfigID:    "sandbox-1",
		BackendType:        "docker",
		ActorID:            "actor-1",
		CapabilitySnapshot: types.JSONMap{"sandbox.workbench": true},
		PolicySnapshot:     types.JSONMap{"network": "none", "max_seconds": float64(60), "cpu_seconds": float64(30)},
	}, workbenches.createInputs[0])
	require.Equal(t, repository.WorkbenchSessionStateCAS{
		TenantID:             10,
		ID:                   "wb-1",
		ExpectedStateVersion: 0,
		ExpectedState:        types.WorkbenchStateProvisioning,
		NextState:            types.WorkbenchStateReady,
	}, workbenches.casInputs[0])

	missing := httptest.NewRecorder()
	r.ServeHTTP(missing, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-missing/workbench/start", map[string]any{
		"incarnation_id": "inc-2", "sandbox_config_id": "sandbox-1", "backend_type": "docker",
	}))
	require.Equal(t, http.StatusNotFound, missing.Code)
	require.Equal(t, "workbench_not_found", t2l03ResponseCode(t, missing))
}

func TestT2L07WorkbenchCommandRejectsJobFromReplacedIncarnation(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{
		ID: "wb-current", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-current",
		State: types.WorkbenchStateReady, LeaseEpoch: 8,
	}}
	control := &t2l03ControlService{job: &types.WorkbenchJob{
		ID: "job-old", TenantID: 10, WorkbenchSessionID: "wb-old", ChatSessionID: "chat-1",
		IncarnationID: "inc-old", LeaseEpoch: 7, State: types.WorkbenchJobStateRunning,
		StateVersion: 3, BackendType: "docker", BackendIdentity: "container-old",
	}}
	h := newWorkbenchHandler(sessions, workbenches, control, &t2l03Runner{}, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/jobs/job-old/commands", map[string]any{
		"sequence": 1, "expected_lease_epoch": 7, "expected_state_version": 3, "command": "printf old",
	}))
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Empty(t, control.commandInputs)
}
func TestT2L03WorkbenchJobRouteConsumesStartNonceAndRunsDockerRunner(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{
		ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1",
		BackendType: "docker", State: types.WorkbenchStateReady, StateVersion: 1, LeaseEpoch: 4,
	}}
	control := &t2l03ControlService{startedJob: &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		IncarnationID: "inc-1", LeaseEpoch: 4, BackendType: "docker", State: types.WorkbenchJobStateQueued,
	}}
	runner := &t2l03Runner{startedJob: &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		IncarnationID: "inc-1", LeaseEpoch: 4, BackendType: "docker", BackendIdentity: "container-1", State: types.WorkbenchJobStateRunning,
	}}
	h := newWorkbenchHandler(sessions, workbenches, control, runner, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/jobs", map[string]any{
		"workbench_id":             "wb-1",
		"expected_lease_epoch":     4,
		"start_nonce":              "nonce-1",
		"backend_type":             "docker",
		"resource_policy_snapshot": map[string]any{"network": "none"},
	}))

	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, "RUNNING", t2l03ResponseData(t, w)["state"])
	require.Equal(t, service.StartWorkbenchJobInput{
		TenantID: 10, WorkbenchSessionID: "wb-1", ExpectedLeaseEpoch: 4, StartNonce: "nonce-1",
		BackendType: "docker", BackendIdentity: "", ActorID: "actor-1",
		ResourcePolicySnapshot: types.JSONMap{"network": "none", "max_seconds": float64(60), "cpu_seconds": float64(30)},
	}, control.startInputs[0])
	require.Equal(t, []workbenchrunner.StartRequest{{TenantID: 10, JobID: "job-1"}}, runner.startRequests)
	require.NotContains(t, w.Body.String(), "nonce-1")
}

func TestT2L10WorkbenchJobRequiresExplicitBackendType(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{
		ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1",
		BackendType: "docker", State: types.WorkbenchStateReady, StateVersion: 1, LeaseEpoch: 4,
	}}
	control := &t2l03ControlService{startedJob: &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		IncarnationID: "inc-1", LeaseEpoch: 4, BackendType: "docker", State: types.WorkbenchJobStateQueued,
	}}
	runner := &t2l03Runner{startedJob: &types.WorkbenchJob{ID: "job-1", TenantID: 10}}
	h := newWorkbenchHandler(sessions, workbenches, control, runner, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/jobs", map[string]any{
		"workbench_id": "wb-1", "expected_lease_epoch": 4, "start_nonce": "nonce-1",
	}))

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, workbenchErrorMalformedRequest, t2l03ResponseCode(t, w))
	require.Empty(t, control.startInputs)
	require.Empty(t, runner.startRequests)
}

func TestT2L09WorkbenchJobNonceRetryDoesNotRestartRunningJob(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{
		ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1",
		State: types.WorkbenchStateReady, LeaseEpoch: 4,
	}}
	control := &t2l03ControlService{startedJob: &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		IncarnationID: "inc-1", LeaseEpoch: 4, BackendType: "docker", BackendIdentity: "container-1",
		State: types.WorkbenchJobStateRunning, StateVersion: 2,
	}}
	runner := &t2l03Runner{}
	h := newWorkbenchHandler(sessions, workbenches, control, runner, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/jobs", map[string]any{
		"workbench_id": "wb-1", "expected_lease_epoch": 4, "start_nonce": "nonce-1", "backend_type": "docker",
	}))

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	require.Equal(t, "RUNNING", t2l03ResponseData(t, w)["state"])
	require.Empty(t, runner.startRequests, "an idempotent nonce retry must not restart an already-running job")
}
func TestT2L03WorkbenchCommandRouteRejectsStaleEpochAndRunsThroughRunner(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", State: types.WorkbenchStateReady, LeaseEpoch: 7}}
	control := &t2l03ControlService{job: &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		LeaseEpoch: 7, State: types.WorkbenchJobStateRunning, StateVersion: 3, BackendType: "docker", BackendIdentity: "container-1",
	}, createdCommand: &types.WorkbenchCommand{
		ID: "cmd-1", TenantID: 10, WorkbenchJobID: "job-1", WorkbenchSessionID: "wb-1",
		Sequence: 1, Kind: "shell", State: types.WorkbenchCommandStateQueued,
	}}
	runner := &t2l03Runner{
		commandResult: &workbenchrunner.CommandResult{Stdout: "ok", ExitCode: 0},
		onRun: func() {
			control.createdCommand.State = types.WorkbenchCommandStateSucceeded
		},
	}
	h := newWorkbenchHandler(sessions, workbenches, control, runner, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	stale := httptest.NewRecorder()
	r.ServeHTTP(stale, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/jobs/job-1/commands", map[string]any{
		"sequence": 1, "expected_lease_epoch": 9, "expected_state_version": 3, "command": "printf ok",
	}))
	require.Equal(t, http.StatusConflict, stale.Code)
	require.Equal(t, "stale_epoch", t2l03ResponseCode(t, stale))
	require.Empty(t, control.commandInputs)
	require.Empty(t, runner.commandRequests)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/jobs/job-1/commands", map[string]any{
		"sequence": 1, "expected_lease_epoch": 7, "expected_state_version": 3,
		"command": "printf ok", "work_dir": "/workspace/project", "timeout_ms": 1500,
	}))

	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, "cmd-1", t2l03ResponseData(t, w)["command_id"])
	require.Equal(t, "SUCCEEDED", t2l03ResponseData(t, w)["state"])
	require.Equal(t, "ok", t2l03ResponseData(t, w)["stdout"])
	require.Equal(t, []workbenchrunner.RunCommandRequest{{TenantID: 10, CommandID: "cmd-1"}}, runner.commandRequests)
	require.Equal(t, service.CreateWorkbenchCommandInput{
		TenantID: 10, WorkbenchJobID: "job-1", ExpectedLeaseEpoch: 7, ExpectedJobStateVersion: 3,
		Sequence: 1, Kind: "shell", ActorID: "actor-1",
		Payload: types.JSONMap{"command": "printf ok", "work_dir": "/workspace/project", "timeout_ms": float64(1500)},
	}, control.commandInputs[0])
}

func TestT2L03WorkbenchEventsAndSignalsStayScoped(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", State: types.WorkbenchStateReady, LeaseEpoch: 7}}
	control := &t2l03ControlService{job: &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		LeaseEpoch: 7, State: types.WorkbenchJobStateRunning, StateVersion: 3, BackendType: "docker", BackendIdentity: "container-1",
	}, events: []*types.WorkbenchRunnerEvent{{
		ID: "evt-2", TenantID: 10, WorkbenchJobID: "job-1", WorkbenchSessionID: "wb-1", Seq: 2, EventType: "stdout", Payload: types.JSONMap{"chunk": "ok", "backend_identity": "container-secret"},
	}}}
	h := newWorkbenchHandler(sessions, workbenches, control, nil, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	events := httptest.NewRecorder()
	r.ServeHTTP(events, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/events?job_id=job-1&after_seq=1&limit=10", nil))
	require.Equal(t, http.StatusOK, events.Code)
	require.Len(t, t2l03ResponseData(t, events)["events"], 1)
	require.NotContains(t, events.Body.String(), "container-secret")
	require.NotContains(t, events.Body.String(), "backend_identity")
	require.Equal(t, []t2l03ListEventsCall{{tenantID: 10, jobID: "job-1", afterSeq: 1, limit: 10}}, control.eventCalls)

	signal := httptest.NewRecorder()
	r.ServeHTTP(signal, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/jobs/job-1/signals", map[string]any{"signal": "TERM"}))
	require.Equal(t, http.StatusNotImplemented, signal.Code)
	require.Equal(t, "unsupported", t2l03ResponseCode(t, signal))
}
func TestT2L07WorkbenchAuditOutboxRouteStaysScopedAndSanitized(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", State: types.WorkbenchStateReady, LeaseEpoch: 7}}
	control := &t2l03ControlService{job: &types.WorkbenchJob{ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1", LeaseEpoch: 7, State: types.WorkbenchJobStateRunning, StateVersion: 3}, auditRows: []*types.WorkbenchAuditOutbox{{
		ID: "audit-1", TenantID: 10, WorkbenchSessionID: "wb-1", WorkbenchJobID: "job-1", CommandID: "cmd-1",
		Action: types.WorkbenchAuditActionCommandFailed, ActorUserID: "actor-1", Outcome: "failure",
		Payload: types.JSONMap{"killed": true, "backend_identity": "container-secret", "reason": "resource_exceeded"}, State: types.WorkbenchAuditOutboxStatePending,
	}}}
	h := newWorkbenchHandler(sessions, workbenches, control, nil, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/audit?job_id=job-1&state=PENDING&limit=5", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	data := t2l03ResponseData(t, w)
	auditRows, ok := data["audit"].([]any)
	require.True(t, ok, "body=%s", w.Body.String())
	require.Len(t, auditRows, 1)
	require.NotContains(t, w.Body.String(), "container-secret")
	require.NotContains(t, w.Body.String(), "backend_identity")
	require.Equal(t, []t2l07AuditListCall{{tenantID: 10, jobID: "job-1", state: types.WorkbenchAuditOutboxStatePending, limit: 5}}, control.auditCalls)

	cross := httptest.NewRecorder()
	control.job.ChatSessionID = "other-chat"
	r.ServeHTTP(cross, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/audit?job_id=job-1", nil))
	require.Equal(t, http.StatusNotFound, cross.Code)
	require.Equal(t, "workbench_not_found", t2l03ResponseCode(t, cross))
}
func TestT2L03WorkbenchStreamTicketRejectsJobOutsideActiveWorkbench(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{ID: "wb-current", TenantID: 10, ChatSessionID: "chat-1", State: types.WorkbenchStateReady, LeaseEpoch: 7}}
	control := &t2l03ControlService{job: &types.WorkbenchJob{
		ID: "job-old", TenantID: 10, WorkbenchSessionID: "wb-old", ChatSessionID: "chat-1",
		LeaseEpoch: 7, State: types.WorkbenchJobStateRunning, StateVersion: 3, BackendType: "docker", BackendIdentity: "container-1",
	}}
	h := newWorkbenchHandler(sessions, workbenches, control, nil, NewWorkbenchStreamTicketStore(time.Minute))
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/stream-ticket", map[string]any{
		"workbench_id": "wb-current", "job_id": "job-old", "expected_lease_epoch": 7,
	}))

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Equal(t, "workbench_not_found", t2l03ResponseCode(t, w))
}
func TestT2L09WorkbenchStreamTicketRejectsReplacedIncarnation(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{
		ID: "wb-current", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-current",
		State: types.WorkbenchStateReady, LeaseEpoch: 8,
	}}
	control := &t2l03ControlService{job: &types.WorkbenchJob{
		ID: "job-old", TenantID: 10, WorkbenchSessionID: "wb-old", ChatSessionID: "chat-1",
		IncarnationID: "inc-old", LeaseEpoch: 7, State: types.WorkbenchJobStateRunning,
	}}
	store := NewWorkbenchStreamTicketStore(time.Minute)
	ticket, err := store.Issue(WorkbenchStreamTicketScope{
		TenantID: 10, ChatSessionID: "chat-1", WorkbenchID: "wb-old", JobID: "job-old", LeaseEpoch: 7, ActorID: "actor-1",
	})
	require.NoError(t, err)
	h := newWorkbenchHandler(sessions, workbenches, control, nil, store)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/stream?ticket="+ticket.Token+"&workbench_id=wb-old&job_id=job-old&lease_epoch=7", nil)
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Equal(t, "unauthorized", t2l03ResponseCode(t, w))
}
func TestT2L03WorkbenchStreamTicketIsSingleUseActorAndExpiryBound(t *testing.T) {
	store := NewWorkbenchStreamTicketStore(20 * time.Millisecond)
	scope := WorkbenchStreamTicketScope{TenantID: 10, ChatSessionID: "chat-1", WorkbenchID: "wb-1", JobID: "job-1", LeaseEpoch: 3, ActorID: "actor-1"}
	ticket, err := store.Issue(scope)
	require.NoError(t, err)
	require.NotEmpty(t, ticket.Token)
	require.True(t, ticket.ExpiresAt.After(time.Now()))

	_, ok := store.Redeem(ticket.Token, WorkbenchStreamTicketScope{TenantID: 10, ChatSessionID: "chat-1", WorkbenchID: "wb-1", JobID: "job-1", LeaseEpoch: 3, ActorID: "other"})
	require.False(t, ok)

	redeemed, ok := store.Redeem(ticket.Token, scope)
	require.True(t, ok)
	require.Equal(t, scope, redeemed.Scope)

	_, ok = store.Redeem(ticket.Token, scope)
	require.False(t, ok)

	expiring, err := store.Issue(scope)
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond)
	_, ok = store.Redeem(expiring.Token, scope)
	require.False(t, ok)
}

func TestT2L07WorkbenchStreamTicketRefusesRandomSourceFailure(t *testing.T) {
	oldReader := workbenchRandomReader
	workbenchRandomReader = t2l07FailingRandomReader{}
	t.Cleanup(func() { workbenchRandomReader = oldReader })

	_, err := NewWorkbenchStreamTicketStore(time.Minute).Issue(WorkbenchStreamTicketScope{TenantID: 10, ChatSessionID: "chat-1", WorkbenchID: "wb-1", ActorID: "actor-1"})
	require.Error(t, err)
}

type t2l07FailingRandomReader struct{}

func (t2l07FailingRandomReader) Read([]byte) (int, error) {
	return 0, stderrors.New("random source failed")
}
func t2l03EnableWorkbench(t *testing.T) {
	t.Helper()
	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
}

func t2l03WorkbenchRouter(h *WorkbenchHandler, tenantID uint64, actorID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), tenantID)
		c.Set(types.UserIDContextKey.String(), actorID)
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, tenantID)
		ctx = context.WithValue(ctx, types.UserIDContextKey, actorID)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	g := r.Group("/api/v1/sessions")
	g.POST("/:session_id/workbench/start", h.Start)
	g.GET("/:id/workbench", h.Get)
	g.POST("/:session_id/workbench/jobs", h.CreateJob)
	g.GET("/:id/workbench/jobs/:job_id", h.GetJob)
	g.POST("/:session_id/workbench/jobs/:job_id/commands", h.CreateCommand)
	g.POST("/:session_id/workbench/jobs/:job_id/signals", h.SignalJob)
	g.GET("/:id/workbench/events", h.ListEvents)
	g.GET("/:id/workbench/audit", h.ListAuditOutbox)
	g.POST("/:session_id/workbench/stream-ticket", h.CreateStreamTicket)
	g.GET("/:id/workbench/stream", h.Stream)
	g.POST("/:session_id/workbench/files/browse", h.BrowseFiles)
	g.POST("/:session_id/workbench/files/upload", h.UploadFile)
	g.POST("/:session_id/workbench/files/download", h.DownloadFile)
	g.POST("/:session_id/workbench/files/rename", h.RenameFile)
	g.POST("/:session_id/workbench/files/delete", h.DeleteFile)
	g.POST("/:session_id/workbench/artifacts", h.PublishArtifact)
	g.GET("/:id/workbench/artifacts/:artifact_id/versions/:version", h.GetArtifact)
	g.GET("/:id/workbench/artifacts/:artifact_id/versions/:version/download", h.DownloadArtifact)
	g.GET("/:id/workbench/artifacts/:artifact_id/versions/:version/preview", h.PreviewArtifact)
	g.POST("/:session_id/workbench/skill-runs/presentation", h.PublishPresentationSkillCandidate)
	return r
}

func t2l03JSONRequest(method, target string, body map[string]any) *http.Request {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func t2l03ResponseCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	code, _ := body["code"].(string)
	return code
}

func t2l03ResponseData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "body=%s", w.Body.String())
	return data
}

type t2l03SessionAuthorizer struct {
	rows  map[string]*types.Session
	calls []string
}

func newT2L03SessionAuthorizer() *t2l03SessionAuthorizer {
	return &t2l03SessionAuthorizer{rows: make(map[string]*types.Session)}
}

func (s *t2l03SessionAuthorizer) GetOwnedSession(ctx context.Context, id string) (*types.Session, error) {
	tenantID, _ := ctx.Value(types.TenantIDContextKey).(uint64)
	s.calls = append(s.calls, id)
	row, ok := s.rows[controlKey(tenantID, id)]
	if !ok {
		return nil, apperrors.ErrSessionNotFound
	}
	copy := *row
	return &copy, nil
}

type t2l03WorkbenchSessions struct {
	created      *types.WorkbenchSession
	ready        *types.WorkbenchSession
	active       *types.WorkbenchSession
	createInputs []service.CreateWorkbenchSessionInput
	casInputs    []repository.WorkbenchSessionStateCAS
}

func (w *t2l03WorkbenchSessions) CreateOrGet(_ context.Context, input service.CreateWorkbenchSessionInput) (*types.WorkbenchSession, error) {
	w.createInputs = append(w.createInputs, input)
	if w.created == nil {
		return nil, repository.ErrWorkbenchSessionNotFound
	}
	copy := *w.created
	return &copy, nil
}

func (w *t2l03WorkbenchSessions) CompareAndSwapState(_ context.Context, input repository.WorkbenchSessionStateCAS) (*types.WorkbenchSession, error) {
	w.casInputs = append(w.casInputs, input)
	if w.ready == nil {
		return nil, repository.ErrWorkbenchSessionStaleVersion
	}
	copy := *w.ready
	return &copy, nil
}

func (w *t2l03WorkbenchSessions) GetActiveByChatSession(_ context.Context, tenantID uint64, chatSessionID string) (*types.WorkbenchSession, error) {
	if w.active == nil || w.active.TenantID != tenantID || w.active.ChatSessionID != chatSessionID {
		return nil, repository.ErrWorkbenchSessionNotFound
	}
	copy := *w.active
	return &copy, nil
}

type t2l03ListEventsCall struct {
	tenantID uint64
	jobID    string
	afterSeq int64
	limit    int
}

type t2l07AuditListCall struct {
	tenantID uint64
	jobID    string
	state    types.WorkbenchAuditOutboxState
	limit    int
}

type t2l03ControlService struct {
	startedJob     *types.WorkbenchJob
	job            *types.WorkbenchJob
	createdCommand *types.WorkbenchCommand
	events         []*types.WorkbenchRunnerEvent
	auditRows      []*types.WorkbenchAuditOutbox
	startInputs    []service.StartWorkbenchJobInput
	commandInputs  []service.CreateWorkbenchCommandInput
	eventCalls     []t2l03ListEventsCall
	auditCalls     []t2l07AuditListCall
}

func (c *t2l03ControlService) StartJob(_ context.Context, input service.StartWorkbenchJobInput) (*types.WorkbenchJob, error) {
	c.startInputs = append(c.startInputs, input)
	if c.startedJob == nil {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	copy := *c.startedJob
	return &copy, nil
}

func (c *t2l03ControlService) GetJob(_ context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error) {
	if c.job == nil || c.job.TenantID != tenantID || c.job.ID != id {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	copy := *c.job
	return &copy, nil
}

func (c *t2l03ControlService) GetCommand(_ context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error) {
	if c.createdCommand == nil || c.createdCommand.TenantID != tenantID || c.createdCommand.ID != id {
		return nil, repository.ErrWorkbenchCommandNotFound
	}
	copy := *c.createdCommand
	return &copy, nil
}

func (c *t2l03ControlService) CreateCommand(_ context.Context, input service.CreateWorkbenchCommandInput) (*types.WorkbenchCommand, error) {
	c.commandInputs = append(c.commandInputs, input)
	if c.createdCommand == nil {
		return nil, repository.ErrWorkbenchCommandNotFound
	}
	copy := *c.createdCommand
	return &copy, nil
}

func (c *t2l03ControlService) ListRunnerEvents(_ context.Context, tenantID uint64, jobID string, afterSeq int64, limit int) ([]*types.WorkbenchRunnerEvent, error) {
	c.eventCalls = append(c.eventCalls, t2l03ListEventsCall{tenantID: tenantID, jobID: jobID, afterSeq: afterSeq, limit: limit})
	return c.events, nil
}

func (c *t2l03ControlService) ListAuditOutbox(_ context.Context, tenantID uint64, jobID string, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error) {
	c.auditCalls = append(c.auditCalls, t2l07AuditListCall{tenantID: tenantID, jobID: jobID, state: state, limit: limit})
	return c.auditRows, nil
}

type t2l03Runner struct {
	startedJob      *types.WorkbenchJob
	commandResult   *workbenchrunner.CommandResult
	onRun           func()
	startRequests   []workbenchrunner.StartRequest
	commandRequests []workbenchrunner.RunCommandRequest
}

func (r *t2l03Runner) Start(_ context.Context, req workbenchrunner.StartRequest) (*types.WorkbenchJob, error) {
	r.startRequests = append(r.startRequests, req)
	if r.startedJob == nil {
		return nil, workbenchrunner.ErrRunnerDisabled
	}
	copy := *r.startedJob
	return &copy, nil
}

func (r *t2l03Runner) RunCommand(_ context.Context, req workbenchrunner.RunCommandRequest) (*workbenchrunner.CommandResult, error) {
	r.commandRequests = append(r.commandRequests, req)
	if r.onRun != nil {
		r.onRun()
	}
	if r.commandResult == nil {
		return nil, stderrors.New("missing result")
	}
	copy := *r.commandResult
	return &copy, nil
}

func controlKey(tenantID uint64, id string) string {
	return strconv.FormatUint(tenantID, 10) + "/" + id
}
