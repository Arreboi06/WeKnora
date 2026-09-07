package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	workbenchrunner "github.com/Tencent/WeKnora/internal/workbench/runner"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	workbenchErrorNotFound             = "workbench_not_found"
	workbenchErrorConflict             = "workbench_conflict"
	workbenchErrorStaleEpoch           = "stale_epoch"
	workbenchErrorStaleVersion         = "stale_version"
	workbenchErrorUnauthorized         = "unauthorized"
	workbenchErrorPathDenied           = "path_denied"
	workbenchErrorPreviewBlocked       = "preview_blocked"
	workbenchErrorRunnerUnavailable    = "runner_unavailable"
	workbenchErrorMigrationUnavailable = "migration_unavailable"
	workbenchErrorQuotaExceeded        = "quota_exceeded"
	workbenchErrorUnsupported          = "unsupported"
	workbenchErrorMalformedRequest     = "malformed_request"
)

type workbenchSessionAuthorizer interface {
	GetOwnedSession(ctx context.Context, id string) (*types.Session, error)
}

type workbenchSessionManager interface {
	CreateOrGet(ctx context.Context, input service.CreateWorkbenchSessionInput) (*types.WorkbenchSession, error)
	CompareAndSwapState(ctx context.Context, input repository.WorkbenchSessionStateCAS) (*types.WorkbenchSession, error)
	GetActiveByChatSession(ctx context.Context, tenantID uint64, chatSessionID string) (*types.WorkbenchSession, error)
}

type workbenchControlManager interface {
	StartJob(ctx context.Context, input service.StartWorkbenchJobInput) (*types.WorkbenchJob, error)
	GetJob(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error)
	GetCommand(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error)
	CreateCommand(ctx context.Context, input service.CreateWorkbenchCommandInput) (*types.WorkbenchCommand, error)
	ListRunnerEvents(ctx context.Context, tenantID uint64, jobID string, afterSeq int64, limit int) ([]*types.WorkbenchRunnerEvent, error)
	ListAuditOutbox(ctx context.Context, tenantID uint64, jobID string, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error)
}

type workbenchRunner interface {
	Start(ctx context.Context, req workbenchrunner.StartRequest) (*types.WorkbenchJob, error)
	RunCommand(ctx context.Context, req workbenchrunner.RunCommandRequest) (*workbenchrunner.CommandResult, error)
}

type workbenchCapabilityProvider interface {
	Snapshot(ctx context.Context, routeRegistered bool) service.WorkbenchCapabilitySnapshot
}

type WorkbenchHandler struct {
	sessions           workbenchSessionAuthorizer
	workbenches        workbenchSessionManager
	control            workbenchControlManager
	runner             workbenchRunner
	capability         workbenchCapabilityProvider
	capabilityRequired bool
	files              workbenchFileManager
	artifacts          workbenchArtifactManager
	presentationSkills workbenchPresentationSkillManager
	tickets            *WorkbenchStreamTicketStore
	upgrader           websocket.Upgrader
}

func NewWorkbenchHandler(
	sessions interfaces.SessionService,
	workbenches *service.WorkbenchSessionService,
	control *service.WorkbenchControlService,
	runner workbenchRunner,
	extras ...any,
) *WorkbenchHandler {
	h := newWorkbenchHandler(sessions, workbenches, control, runner, NewWorkbenchStreamTicketStore(time.Minute), extras...)
	h.capabilityRequired = true
	return h
}

func newWorkbenchHandler(
	sessions workbenchSessionAuthorizer,
	workbenches workbenchSessionManager,
	control workbenchControlManager,
	runner workbenchRunner,
	tickets *WorkbenchStreamTicketStore,
	extras ...any,
) *WorkbenchHandler {
	fileManager := workbenchFileManager(nil)
	artifactManager := workbenchArtifactManager(nil)
	presentationSkillManager := workbenchPresentationSkillManager(nil)
	var capabilityProvider workbenchCapabilityProvider
	for _, extra := range extras {
		switch v := extra.(type) {
		case nil:
		case workbenchFileManager:
			fileManager = v
		case workbenchArtifactManager:
			artifactManager = v
		case workbenchPresentationSkillManager:
			presentationSkillManager = v
		case workbenchCapabilityProvider:
			capabilityProvider = v
		}
	}
	return &WorkbenchHandler{
		sessions:           sessions,
		workbenches:        workbenches,
		control:            control,
		runner:             runner,
		capability:         capabilityProvider,
		files:              fileManager,
		artifacts:          artifactManager,
		presentationSkills: presentationSkillManager,
		tickets:            tickets,
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
			return workbenchStreamOriginAllowed(r)
		}},
	}
}

type startWorkbenchRequest struct {
	IncarnationID      string        `json:"incarnation_id"`
	SandboxConfigID    string        `json:"sandbox_config_id"`
	BackendType        string        `json:"backend_type"`
	CapabilitySnapshot types.JSONMap `json:"capability_snapshot"`
	PolicySnapshot     types.JSONMap `json:"policy_snapshot"`
}

type createWorkbenchJobRequest struct {
	WorkbenchID            string        `json:"workbench_id"`
	ExpectedLeaseEpoch     int64         `json:"expected_lease_epoch"`
	StartNonce             string        `json:"start_nonce"`
	BackendType            string        `json:"backend_type"`
	ResourcePolicySnapshot types.JSONMap `json:"resource_policy_snapshot"`
}

type createWorkbenchCommandRequest struct {
	Sequence                int64             `json:"sequence"`
	ExpectedLeaseEpoch      int64             `json:"expected_lease_epoch"`
	ExpectedJobStateVersion int64             `json:"expected_state_version"`
	Command                 string            `json:"command"`
	WorkDir                 string            `json:"work_dir"`
	Stdin                   string            `json:"stdin"`
	TimeoutMS               float64           `json:"timeout_ms"`
	Env                     map[string]string `json:"env"`
	SkillName               string            `json:"skill_name"`
	SkillOperation          string            `json:"skill_operation"`
}

type signalWorkbenchJobRequest struct {
	Signal string `json:"signal"`
}

type createStreamTicketRequest struct {
	WorkbenchID        string `json:"workbench_id"`
	JobID              string `json:"job_id"`
	ExpectedLeaseEpoch int64  `json:"expected_lease_epoch"`
}

func (h *WorkbenchHandler) Start(c *gin.Context) {
	ctx, tenantID, actorID, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	var req startWorkbenchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	backendType := strings.TrimSpace(req.BackendType)
	sandboxConfigID := strings.TrimSpace(req.SandboxConfigID)
	if backendType != string(sandbox.SandboxTypeDocker) || strings.TrimSpace(req.IncarnationID) == "" || strings.TrimSpace(req.SandboxConfigID) == "" {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid workbench start request")
		return
	}
	capabilitySnapshot := types.JSONMap{"sandbox.workbench": sandbox.WorkbenchEnabled()}
	if h.capability != nil {
		snapshot := h.capability.Snapshot(ctx, true)
		capabilitySnapshot["sandbox.workbench"] = snapshot.Supported
		if snapshot.Reason != "" {
			capabilitySnapshot["reason"] = snapshot.Reason
		}
		resolvedConfigID, configEligible := resolveWorkbenchSandboxConfigID(tenantID, sandboxConfigID, backendType)
		if !snapshot.Supported ||
			!workbenchBackendEligible(snapshot.EligibleBackends, backendType) ||
			!configEligible {
			writeWorkbenchError(c, http.StatusNotFound, workbenchErrorUnsupported, "workbench backend is unavailable")
			return
		}
		sandboxConfigID = resolvedConfigID
		capabilitySnapshot["eligible_backends"] = append([]string(nil), snapshot.EligibleBackends...)
	}
	policySnapshot := service.DefaultWorkbenchPolicySnapshot()
	wb, err := h.workbenches.CreateOrGet(ctx, service.CreateWorkbenchSessionInput{
		TenantID:           tenantID,
		ChatSessionID:      session.ID,
		Purpose:            types.WorkbenchPurpose,
		IncarnationID:      strings.TrimSpace(req.IncarnationID),
		SandboxConfigID:    sandboxConfigID,
		BackendType:        backendType,
		ActorID:            actorID,
		CapabilitySnapshot: capabilitySnapshot,
		PolicySnapshot:     policySnapshot,
	})
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	if wb.State == types.WorkbenchStateProvisioning {
		wb, err = h.workbenches.CompareAndSwapState(ctx, repository.WorkbenchSessionStateCAS{
			TenantID:             tenantID,
			ID:                   wb.ID,
			ExpectedStateVersion: wb.StateVersion,
			ExpectedState:        types.WorkbenchStateProvisioning,
			NextState:            types.WorkbenchStateReady,
		})
		if err != nil {
			h.writeMappedError(c, err)
			return
		}
	}
	if wb.State != types.WorkbenchStateReady {
		writeWorkbenchError(c, http.StatusConflict, workbenchErrorConflict, "workbench is not ready")
		return
	}
	writeWorkbenchData(c, http.StatusOK, workbenchSessionResponse(wb))
}

func (h *WorkbenchHandler) Get(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	wb, err := h.workbenches.GetActiveByChatSession(ctx, tenantID, session.ID)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusOK, workbenchSessionResponse(wb))
}

func (h *WorkbenchHandler) CreateJob(c *gin.Context) {
	ctx, tenantID, actorID, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if h.runner == nil {
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, "workbench runner is unavailable")
		return
	}
	var req createWorkbenchJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	wb, ok := h.requireActiveWorkbench(c, ctx, tenantID, session.ID, req.WorkbenchID, req.ExpectedLeaseEpoch)
	if !ok {
		return
	}
	backendType := strings.TrimSpace(req.BackendType)
	if backendType != string(sandbox.SandboxTypeDocker) || strings.TrimSpace(req.StartNonce) == "" {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid workbench job request")
		return
	}
	resourcePolicySnapshot := service.DefaultWorkbenchResourcePolicySnapshot()
	job, err := h.control.StartJob(ctx, service.StartWorkbenchJobInput{
		TenantID:               tenantID,
		WorkbenchSessionID:     wb.ID,
		ExpectedLeaseEpoch:     req.ExpectedLeaseEpoch,
		StartNonce:             strings.TrimSpace(req.StartNonce),
		BackendType:            backendType,
		ActorID:                actorID,
		ResourcePolicySnapshot: resourcePolicySnapshot,
	})
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	// A nonce retry is idempotent through the HTTP boundary. Only a queued job
	// with no backend identity still needs the runner start transition; once a
	// prior request has reached STARTING, RUNNING, or a terminal state, return
	// its durable result instead of attempting a second start.
	if job.State != types.WorkbenchJobStateQueued || job.ClosedAt != nil || strings.TrimSpace(job.BackendIdentity) != "" {
		writeWorkbenchData(c, http.StatusCreated, workbenchJobResponse(job))
		return
	}
	running, err := h.runner.Start(ctx, workbenchrunner.StartRequest{TenantID: tenantID, JobID: job.ID})
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusCreated, workbenchJobResponse(running))
}

func (h *WorkbenchHandler) GetJob(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	job, ok := h.requireSessionJob(c, ctx, tenantID, session.ID, c.Param("job_id"))
	if !ok {
		return
	}
	writeWorkbenchData(c, http.StatusOK, workbenchJobResponse(job))
}

func (h *WorkbenchHandler) CreateCommand(c *gin.Context) {
	ctx, tenantID, actorID, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if h.runner == nil {
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, "workbench runner is unavailable")
		return
	}
	var req createWorkbenchCommandRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	job, ok := h.requireSessionJob(c, ctx, tenantID, session.ID, c.Param("job_id"))
	if !ok {
		return
	}
	if req.ExpectedLeaseEpoch != job.LeaseEpoch {
		writeWorkbenchError(c, http.StatusConflict, workbenchErrorStaleEpoch, "stale lease epoch")
		return
	}
	if req.ExpectedJobStateVersion != job.StateVersion {
		writeWorkbenchError(c, http.StatusConflict, workbenchErrorStaleVersion, "stale job state version")
		return
	}
	payload := types.JSONMap{"command": strings.TrimSpace(req.Command)}
	if payload["command"] == "" || req.Sequence <= 0 {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid workbench command request")
		return
	}
	if strings.TrimSpace(req.WorkDir) != "" {
		payload["work_dir"] = strings.TrimSpace(req.WorkDir)
	}
	if req.Stdin != "" {
		payload["stdin"] = req.Stdin
	}
	if req.TimeoutMS > 0 {
		payload["timeout_ms"] = req.TimeoutMS
	}
	if len(req.Env) > 0 {
		payload["env"] = req.Env
	}
	skillName := strings.ToLower(strings.TrimSpace(req.SkillName))
	skillOperation := strings.TrimSpace(req.SkillOperation)
	if skillName != "" || skillOperation != "" {
		if skillName != service.WorkbenchPresentationSkillName || skillOperation != service.WorkbenchPresentationSkillOperation {
			writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "unsupported workbench skill metadata")
			return
		}
		payload["skill_name"] = service.WorkbenchPresentationSkillName
		payload["skill_operation"] = service.WorkbenchPresentationSkillOperation
	}
	command, err := h.control.CreateCommand(ctx, service.CreateWorkbenchCommandInput{
		TenantID:                tenantID,
		WorkbenchJobID:          job.ID,
		ExpectedLeaseEpoch:      req.ExpectedLeaseEpoch,
		ExpectedJobStateVersion: req.ExpectedJobStateVersion,
		Sequence:                req.Sequence,
		Kind:                    "shell",
		Payload:                 payload,
		ActorID:                 actorID,
	})
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	result, err := h.runner.RunCommand(ctx, workbenchrunner.RunCommandRequest{TenantID: tenantID, CommandID: command.ID})
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	terminalCommand, err := h.control.GetCommand(ctx, tenantID, command.ID)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	if terminalCommand == nil ||
		terminalCommand.TenantID != tenantID ||
		terminalCommand.ID != command.ID ||
		terminalCommand.WorkbenchJobID != command.WorkbenchJobID ||
		terminalCommand.WorkbenchSessionID != command.WorkbenchSessionID ||
		!terminalCommand.State.IsTerminal() {
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, "workbench command terminal state is unavailable")
		return
	}
	writeWorkbenchData(c, http.StatusCreated, gin.H{
		"command_id": command.ID,
		"state":      terminalCommand.State,
		"stdout":     result.Stdout,
		"stderr":     result.Stderr,
		"exit_code":  result.ExitCode,
		"killed":     result.Killed,
	})
}

func (h *WorkbenchHandler) SignalJob(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	var req signalWorkbenchJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Signal) == "" {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "missing workbench signal")
		return
	}
	if _, ok := h.requireSessionJob(c, ctx, tenantID, session.ID, c.Param("job_id")); !ok {
		return
	}
	writeWorkbenchError(c, http.StatusNotImplemented, workbenchErrorUnsupported, "workbench job signals are not wired in this slice")
}

func (h *WorkbenchHandler) ListEvents(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(c.Query("job_id"))
	if jobID == "" {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "missing workbench job id")
		return
	}
	if _, ok := h.requireSessionJob(c, ctx, tenantID, session.ID, jobID); !ok {
		return
	}
	afterSeq := int64(0)
	if raw := strings.TrimSpace(c.Query("after_seq")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid after_seq")
			return
		}
		afterSeq = parsed
	}
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	events, err := h.control.ListRunnerEvents(ctx, tenantID, jobID, afterSeq, limit)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusOK, gin.H{"events": workbenchRunnerEventResponses(events)})
}

func (h *WorkbenchHandler) ListAuditOutbox(c *gin.Context) {
	ctx, tenantID, _, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(c.Query("job_id"))
	if jobID == "" {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "missing workbench job id")
		return
	}
	if _, ok := h.requireSessionJob(c, ctx, tenantID, session.ID, jobID); !ok {
		return
	}
	state := types.WorkbenchAuditOutboxState(strings.TrimSpace(c.Query("state")))
	limit := 100
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	rows, err := h.control.ListAuditOutbox(ctx, tenantID, jobID, state, limit)
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusOK, gin.H{"audit": workbenchAuditOutboxResponses(rows)})
}
func (h *WorkbenchHandler) CreateStreamTicket(c *gin.Context) {
	ctx, tenantID, actorID, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	var req createStreamTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	wb, ok := h.requireActiveWorkbench(c, ctx, tenantID, session.ID, req.WorkbenchID, req.ExpectedLeaseEpoch)
	if !ok {
		return
	}
	if strings.TrimSpace(req.JobID) != "" {
		job, ok := h.requireSessionJob(c, ctx, tenantID, session.ID, req.JobID)
		if !ok {
			return
		}
		if job.WorkbenchSessionID != wb.ID {
			writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench job not found")
			return
		}
		if job.LeaseEpoch != req.ExpectedLeaseEpoch {
			writeWorkbenchError(c, http.StatusConflict, workbenchErrorStaleEpoch, "stale lease epoch")
			return
		}
	}
	ticket, err := h.tickets.Issue(WorkbenchStreamTicketScope{
		TenantID:      tenantID,
		ChatSessionID: session.ID,
		WorkbenchID:   wb.ID,
		JobID:         strings.TrimSpace(req.JobID),
		LeaseEpoch:    req.ExpectedLeaseEpoch,
		ActorID:       actorID,
	})
	if err != nil {
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, "stream ticket unavailable")
		return
	}
	writeWorkbenchData(c, http.StatusCreated, gin.H{
		"ticket":     ticket.Token,
		"expires_at": ticket.ExpiresAt,
		"grants":     []string{"stream"},
	})
}

func (h *WorkbenchHandler) Stream(c *gin.Context) {
	ctx, tenantID, actorID, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !workbenchStreamOriginAllowed(c.Request) {
		writeWorkbenchError(c, http.StatusForbidden, workbenchErrorUnauthorized, "origin is not allowed")
		return
	}
	token := strings.TrimSpace(c.Query("ticket"))
	if token == "" {
		writeWorkbenchError(c, http.StatusUnauthorized, workbenchErrorUnauthorized, "missing stream ticket")
		return
	}
	workbenchID := strings.TrimSpace(c.Query("workbench_id"))
	jobID := strings.TrimSpace(c.Query("job_id"))
	leaseEpoch, err := strconv.ParseInt(strings.TrimSpace(c.Query("lease_epoch")), 10, 64)
	if err != nil || leaseEpoch < 0 {
		writeWorkbenchError(c, http.StatusUnauthorized, workbenchErrorUnauthorized, "invalid stream ticket scope")
		return
	}
	scope := WorkbenchStreamTicketScope{
		TenantID:      tenantID,
		ChatSessionID: session.ID,
		WorkbenchID:   workbenchID,
		JobID:         jobID,
		LeaseEpoch:    leaseEpoch,
		ActorID:       actorID,
	}
	if !h.streamScopeIsCurrent(ctx, scope) {
		writeWorkbenchError(c, http.StatusUnauthorized, workbenchErrorUnauthorized, "stream ticket is stale")
		return
	}
	ticket, redeemed := h.tickets.Redeem(token, scope)
	if !redeemed {
		writeWorkbenchError(c, http.StatusUnauthorized, workbenchErrorUnauthorized, "invalid stream ticket")
		return
	}
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.WriteJSON(gin.H{"type": "workbench.stream.ready", "workbench_id": ticket.Scope.WorkbenchID, "job_id": ticket.Scope.JobID})
	if ticket.Scope.JobID != "" && h.control != nil {
		events, err := h.control.ListRunnerEvents(ctx, tenantID, ticket.Scope.JobID, 0, 100)
		if err == nil {
			for _, event := range workbenchRunnerEventResponses(events) {
				_ = conn.WriteJSON(event)
			}
		}
	}
}

func (h *WorkbenchHandler) streamScopeIsCurrent(ctx context.Context, scope WorkbenchStreamTicketScope) bool {
	if h == nil || h.workbenches == nil || h.control == nil || scope.TenantID == 0 ||
		strings.TrimSpace(scope.ChatSessionID) == "" || strings.TrimSpace(scope.WorkbenchID) == "" || scope.LeaseEpoch < 0 {
		return false
	}
	active, err := h.workbenches.GetActiveByChatSession(ctx, scope.TenantID, strings.TrimSpace(scope.ChatSessionID))
	if err != nil || active == nil || active.ID != strings.TrimSpace(scope.WorkbenchID) || active.LeaseEpoch != scope.LeaseEpoch {
		return false
	}
	if strings.TrimSpace(scope.JobID) == "" {
		return true
	}
	job, err := h.control.GetJob(ctx, scope.TenantID, strings.TrimSpace(scope.JobID))
	if err != nil || job == nil {
		return false
	}
	return job.TenantID == scope.TenantID && job.ChatSessionID == scope.ChatSessionID &&
		job.WorkbenchSessionID == active.ID && job.IncarnationID == active.IncarnationID &&
		job.LeaseEpoch == active.LeaseEpoch
}
func (h *WorkbenchHandler) authorizeWorkbench(c *gin.Context) (context.Context, uint64, string, *types.Session, bool) {
	if h == nil || h.sessions == nil || h.workbenches == nil || h.control == nil || h.tickets == nil || !sandbox.WorkbenchEnabled() {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorUnsupported, "workbench is disabled")
		return nil, 0, "", nil, false
	}
	if h.capabilityRequired && h.capability == nil {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorUnsupported, "workbench capability is unavailable")
		return nil, 0, "", nil, false
	}
	tenantID := c.GetUint64(types.TenantIDContextKey.String())
	actorID := strings.TrimSpace(c.GetString(types.UserIDContextKey.String()))
	ctx := c.Request.Context()
	if tenantID == 0 || actorID == "" {
		writeWorkbenchError(c, http.StatusUnauthorized, workbenchErrorUnauthorized, "missing authenticated actor")
		return nil, 0, "", nil, false
	}
	sessionID := c.Param("session_id")
	if sessionID == "" {
		sessionID = c.Param("id")
	}
	if strings.TrimSpace(sessionID) == "" {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, "missing session id")
		return nil, 0, "", nil, false
	}
	session, err := h.sessions.GetOwnedSession(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		h.writeMappedError(c, err)
		return nil, 0, "", nil, false
	}
	if session == nil || session.TenantID != tenantID || session.ID != strings.TrimSpace(sessionID) {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench session not found")
		return nil, 0, "", nil, false
	}
	if h.capability != nil {
		snapshot := h.capability.Snapshot(ctx, true)
		if !snapshot.Supported {
			writeWorkbenchError(c, http.StatusNotFound, workbenchErrorUnsupported, "workbench is unavailable")
			return nil, 0, "", nil, false
		}
	}
	return ctx, tenantID, actorID, session, true
}

func workbenchBackendEligible(backends []string, backendType string) bool {
	if strings.TrimSpace(backendType) != string(sandbox.SandboxTypeDocker) {
		return false
	}
	for _, backend := range backends {
		if strings.TrimSpace(backend) == types.WorkbenchBackendLocalProtectedDocker {
			return true
		}
	}
	return false
}

// resolveWorkbenchSandboxConfigID accepts only the evidence-bound local
// protected-Docker profile. A named tenant config may select another daemon,
// image, TLS identity, or network policy; until Runner, file, and artifact
// services are all constructed from that exact config, accepting it would be a
// silent backend substitution. The local profile has no tenant config row, so
// its persisted stable UUID is derived from the server-owned tenant scope.
func resolveWorkbenchSandboxConfigID(tenantID uint64, configID, backendType string) (string, bool) {
	configID = strings.TrimSpace(configID)
	backendType = strings.TrimSpace(backendType)
	if tenantID == 0 || configID != types.WorkbenchBackendLocalProtectedDocker || backendType != string(sandbox.SandboxTypeDocker) {
		return "", false
	}
	name := "weknora/workbench/config/" + strconv.FormatUint(tenantID, 10) + "/" + configID
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String(), true
}

func (h *WorkbenchHandler) requireActiveWorkbench(c *gin.Context, ctx context.Context, tenantID uint64, sessionID, workbenchID string, expectedLeaseEpoch int64) (*types.WorkbenchSession, bool) {
	wb, err := h.workbenches.GetActiveByChatSession(ctx, tenantID, sessionID)
	if err != nil {
		h.writeMappedError(c, err)
		return nil, false
	}
	if strings.TrimSpace(workbenchID) == "" || wb.ID != strings.TrimSpace(workbenchID) {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench not found")
		return nil, false
	}
	if wb.LeaseEpoch != expectedLeaseEpoch {
		writeWorkbenchError(c, http.StatusConflict, workbenchErrorStaleEpoch, "stale lease epoch")
		return nil, false
	}
	return wb, true
}

func (h *WorkbenchHandler) requireSessionJob(c *gin.Context, ctx context.Context, tenantID uint64, sessionID, jobID string) (*types.WorkbenchJob, bool) {
	job, err := h.control.GetJob(ctx, tenantID, strings.TrimSpace(jobID))
	if err != nil {
		h.writeMappedError(c, err)
		return nil, false
	}
	active, err := h.workbenches.GetActiveByChatSession(ctx, tenantID, sessionID)
	if err != nil {
		h.writeMappedError(c, err)
		return nil, false
	}
	if job == nil || active == nil || active.State != types.WorkbenchStateReady || active.ClosedAt != nil ||
		job.TenantID != tenantID || job.ChatSessionID != sessionID ||
		job.WorkbenchSessionID != active.ID || job.IncarnationID != active.IncarnationID || job.LeaseEpoch != active.LeaseEpoch {
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench job not found")
		return nil, false
	}
	return job, true
}

func (h *WorkbenchHandler) writeMappedError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, apperrors.ErrSessionNotFound), errors.Is(err, repository.ErrWorkbenchSessionNotFound), errors.Is(err, repository.ErrWorkbenchJobNotFound), errors.Is(err, repository.ErrWorkbenchCommandNotFound), errors.Is(err, repository.ErrWorkbenchArtifactNotFound), errors.Is(err, repository.ErrWorkbenchSkillRunNotFound), errors.Is(err, service.ErrWorkbenchFileNotFound):
		writeWorkbenchError(c, http.StatusNotFound, workbenchErrorNotFound, "workbench resource not found")
	case errors.Is(err, repository.ErrWorkbenchSessionConflict), errors.Is(err, repository.ErrWorkbenchJobConflict), errors.Is(err, repository.ErrWorkbenchStartNonceConsumed), errors.Is(err, repository.ErrWorkbenchCommandSequenceConflict), errors.Is(err, repository.ErrWorkbenchArtifactConflict), errors.Is(err, repository.ErrWorkbenchSkillRunConflict):
		writeWorkbenchError(c, http.StatusConflict, workbenchErrorConflict, err.Error())
	case errors.Is(err, repository.ErrWorkbenchStaleEpoch):
		writeWorkbenchError(c, http.StatusConflict, workbenchErrorStaleEpoch, err.Error())
	case errors.Is(err, repository.ErrWorkbenchSessionStaleVersion), errors.Is(err, repository.ErrWorkbenchJobStaleVersion), errors.Is(err, repository.ErrWorkbenchCommandStaleVersion), errors.Is(err, repository.ErrWorkbenchSkillRunStaleVersion):
		writeWorkbenchError(c, http.StatusConflict, workbenchErrorStaleVersion, err.Error())
	case errors.Is(err, repository.ErrWorkbenchSessionInvalidArgument), errors.Is(err, workbenchrunner.ErrInvalidRequest), errors.Is(err, service.ErrWorkbenchFileInvalidArgument):
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
	case errors.Is(err, workbenchrunner.ErrWorkspaceEscape), errors.Is(err, service.ErrWorkbenchFilePathDenied):
		writeWorkbenchError(c, http.StatusForbidden, workbenchErrorPathDenied, err.Error())
	case errors.Is(err, service.ErrWorkbenchFileQuotaExceeded):
		writeWorkbenchError(c, http.StatusRequestEntityTooLarge, workbenchErrorQuotaExceeded, err.Error())
	case errors.Is(err, service.ErrWorkbenchPreviewBlocked):
		writeWorkbenchError(c, http.StatusForbidden, workbenchErrorPreviewBlocked, err.Error())
	case errors.Is(err, repository.ErrWorkbenchUnsupportedDatabase):
		writeWorkbenchError(c, http.StatusNotImplemented, workbenchErrorUnsupported, "workbench requires PostgreSQL")
	case errors.Is(err, repository.ErrWorkbenchMigrationUnavailable):
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorMigrationUnavailable, "workbench migration is unavailable")
	case errors.Is(err, service.ErrWorkbenchFileUnsupported):
		writeWorkbenchError(c, http.StatusNotImplemented, workbenchErrorUnsupported, err.Error())
	case errors.Is(err, workbenchrunner.ErrRunnerDisabled), errors.Is(err, workbenchrunner.ErrUnsupportedBackend), errors.Is(err, workbenchrunner.ErrJobNotRunning), errors.Is(err, workbenchrunner.ErrNoBackendIdentity):
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, err.Error())
	default:
		writeWorkbenchError(c, http.StatusInternalServerError, "lost", err.Error())
	}
}

func writeWorkbenchData(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"success": true, "data": data})
}

func writeWorkbenchError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"success": false, "code": code, "message": message})
}

func workbenchSessionResponse(wb *types.WorkbenchSession) gin.H {
	return gin.H{
		"id":                 wb.ID,
		"chat_session_id":    wb.ChatSessionID,
		"incarnation_id":     wb.IncarnationID,
		"backend_type":       wb.BackendType,
		"state":              wb.State,
		"state_version":      wb.StateVersion,
		"lease_epoch":        wb.LeaseEpoch,
		"stream_ticket_only": true,
	}
}

func workbenchJobResponse(job *types.WorkbenchJob) gin.H {
	return gin.H{
		"id":              job.ID,
		"workbench_id":    job.WorkbenchSessionID,
		"chat_session_id": job.ChatSessionID,
		"backend_type":    job.BackendType,
		"state":           job.State,
		"state_version":   job.StateVersion,
		"lease_epoch":     job.LeaseEpoch,
		"created_at":      job.CreatedAt,
		"resource_policy": job.ResourcePolicySnapshot,
	}
}

func workbenchRunnerEventResponses(events []*types.WorkbenchRunnerEvent) []gin.H {
	responses := make([]gin.H, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		responses = append(responses, gin.H{
			"id":         event.ID,
			"job_id":     event.WorkbenchJobID,
			"command_id": event.CommandID,
			"seq":        event.Seq,
			"event_type": event.EventType,
			"payload":    workbenchRunnerEventPayloadResponse(event.Payload),
			"created_at": event.CreatedAt,
		})
	}
	return responses
}

func workbenchAuditOutboxResponses(rows []*types.WorkbenchAuditOutbox) []gin.H {
	responses := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		responses = append(responses, gin.H{
			"id":            row.ID,
			"job_id":        row.WorkbenchJobID,
			"command_id":    row.CommandID,
			"action":        row.Action,
			"actor_user_id": row.ActorUserID,
			"outcome":       row.Outcome,
			"payload":       workbenchRunnerEventPayloadResponse(row.Payload),
			"state":         row.State,
			"attempts":      row.Attempts,
			"created_at":    row.CreatedAt,
			"updated_at":    row.UpdatedAt,
			"published_at":  row.PublishedAt,
		})
	}
	return responses
}

func workbenchRunnerEventPayloadResponse(payload types.JSONMap) types.JSONMap {
	if payload == nil {
		return types.JSONMap{}
	}
	copy := make(types.JSONMap, len(payload))
	for key, value := range payload {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "backend_identity", "backendidentity", "container_id", "containerid":
			continue
		default:
			copy[key] = value
		}
	}
	return copy
}

type WorkbenchStreamTicketScope struct {
	TenantID      uint64
	ChatSessionID string
	WorkbenchID   string
	JobID         string
	LeaseEpoch    int64
	ActorID       string
}

type WorkbenchStreamTicket struct {
	Token     string
	Scope     WorkbenchStreamTicketScope
	ExpiresAt time.Time
}

type WorkbenchStreamTicketStore struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	tickets map[string]WorkbenchStreamTicket
}

func NewWorkbenchStreamTicketStore(ttl time.Duration) *WorkbenchStreamTicketStore {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &WorkbenchStreamTicketStore{ttl: ttl, now: time.Now, tickets: make(map[string]WorkbenchStreamTicket)}
}

func (s *WorkbenchStreamTicketStore) Issue(scope WorkbenchStreamTicketScope) (WorkbenchStreamTicket, error) {
	if s == nil {
		return WorkbenchStreamTicket{}, errors.New("nil stream ticket store")
	}
	token, err := randomWorkbenchTicket()
	if err != nil {
		return WorkbenchStreamTicket{}, err
	}
	ticket := WorkbenchStreamTicket{Token: token, Scope: scope, ExpiresAt: s.now().Add(s.ttl)}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[token] = ticket
	return ticket, nil
}

func (s *WorkbenchStreamTicketStore) Redeem(token string, expected WorkbenchStreamTicketScope) (WorkbenchStreamTicket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[token]
	if !ok || s.now().After(ticket.ExpiresAt) || ticket.Scope != expected {
		return WorkbenchStreamTicket{}, false
	}
	delete(s.tickets, token)
	return ticket, true
}

var workbenchRandomReader io.Reader = rand.Reader

func randomWorkbenchTicket() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(workbenchRandomReader, buf); err != nil {
		return "", errors.New("secure random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func workbenchStreamOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
