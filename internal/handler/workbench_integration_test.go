//go:build t2_l03_real

package handler

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/database"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	workbenchrunner "github.com/Tencent/WeKnora/internal/workbench/runner"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestT2L03RealHTTPPostgresDockerCommandChain(t *testing.T) {
	require.Equal(t, "1", os.Getenv("WEKNORA_T2_L03_REAL"), "WEKNORA_T2_L03_REAL must be set for the real T2-L03 chain")
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "WEKNORA_T2_L03_DOCKER_IMAGE"} {
		require.NotEmpty(t, os.Getenv(key), "%s is required", key)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	previousDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	defer func() { require.NoError(t, os.Chdir(previousDir)) }()

	require.NoError(t, database.RunMigrationsWithOptions(t2l03PostgresURL(os.Getenv("DB_NAME")), database.MigrationOptions{}))
	db, err := gorm.Open(postgres.Open(t2l03GormDSN(os.Getenv("DB_NAME"))), &gorm.Config{})
	require.NoError(t, err)
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	const tenantID uint64 = 7903
	const actorID = "actor-real"
	const chatID = "t2l03-real-chat"
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE tenant_id = ?", tenantID).Error)
	require.NoError(t, db.Exec("INSERT INTO sessions (id, tenant_id, title, user_id) VALUES (?, ?, ?, ?)", chatID, tenantID, "T2L03 real chain", actorID).Error)

	sessionRepo := repository.NewSessionRepository(db)
	workbenchRepo := repository.NewWorkbenchSessionRepository(db)
	controlRepo := repository.NewWorkbenchControlRepository(db)
	workbenchSvc := service.NewWorkbenchSessionService(workbenchRepo, sessionRepo)
	controlSvc := service.NewWorkbenchControlService(controlRepo, workbenchRepo)

	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")

	dockerCfg := sandbox.DefaultConfig()
	dockerCfg.Type = sandbox.SandboxTypeDocker
	dockerCfg.DockerImage = strings.TrimSpace(os.Getenv("WEKNORA_T2_L03_DOCKER_IMAGE"))
	dockerCfg.DockerHost = strings.TrimSpace(os.Getenv("DOCKER_INTEGRATION_HOST"))
	if dockerCfg.DockerHost == "" {
		dockerCfg.DockerHost = strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	}
	dockerCfg.DockerTLSCertPath = strings.TrimSpace(os.Getenv("DOCKER_CERT_PATH"))
	// The shared heavy-test Docker daemon is on a private VM address; this is test-only explicit config.
	dockerCfg.AllowPrivateEndpoints = true
	client, err := sandbox.NewDockerRemoteClient(dockerCfg)
	require.NoError(t, err)
	runner := workbenchrunner.NewProtectedDockerRunner(workbenchrunner.Config{
		Enabled:               true,
		TemplateID:            dockerCfg.DockerImage,
		IdleTimeout:           2 * time.Minute,
		DefaultCommandTimeout: 20 * time.Second,
	}, client, controlRepo)

	h := newWorkbenchHandler(&t2l03RealSessionAuthorizer{db: db}, workbenchSvc, controlSvc, runner, NewWorkbenchStreamTicketStore(time.Minute), t2l10RealCapabilityProvider())
	engine := t2l03RealWorkbenchRouter(h, tenantID, actorID)
	server := httptest.NewServer(engine)
	defer server.Close()
	healthResp, err := http.Get(server.URL + "/health")
	require.NoError(t, err)
	defer healthResp.Body.Close()
	require.Equal(t, http.StatusOK, healthResp.StatusCode)

	var jobID string
	defer func() {
		if jobID == "" {
			return
		}
		job, err := controlRepo.GetJobByID(context.Background(), tenantID, jobID)
		if err == nil && job != nil {
			_ = runner.Cleanup(context.Background(), workbenchrunner.CleanupRequest{
				TenantID:             tenantID,
				JobID:                job.ID,
				ExpectedStateVersion: job.StateVersion,
				TerminalState:        types.WorkbenchJobStateCancelled,
				Reason:               "t2l03_real_test_cleanup",
			})
		}
	}()

	start := httptest.NewRecorder()
	engine.ServeHTTP(start, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/start", map[string]any{
		"incarnation_id":      "inc-real-1",
		"sandbox_config_id":   workbenchrunner.EligibleProtectedDockerBackend,
		"backend_type":        "docker",
		"capability_snapshot": map[string]any{"sandbox.workbench": true},
		"policy_snapshot":     map[string]any{"network": "none"},
	}))
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	workbenchData := t2l03ResponseData(t, start)
	workbenchID, _ := workbenchData["id"].(string)
	leaseEpoch := int64(workbenchData["lease_epoch"].(float64))
	require.NotEmpty(t, workbenchID)

	job := httptest.NewRecorder()
	engine.ServeHTTP(job, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/jobs", map[string]any{
		"workbench_id":             workbenchID,
		"expected_lease_epoch":     leaseEpoch,
		"start_nonce":              "t2l03-real-nonce",
		"backend_type":             "docker",
		"resource_policy_snapshot": map[string]any{"network": "none"},
	}))
	require.Equal(t, http.StatusCreated, job.Code, job.Body.String())
	jobData := t2l03ResponseData(t, job)
	jobID, _ = jobData["id"].(string)
	require.NotEmpty(t, jobID)
	require.Equal(t, "RUNNING", jobData["state"])
	jobStateVersion := int64(jobData["state_version"].(float64))

	command := httptest.NewRecorder()
	engine.ServeHTTP(command, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/jobs/"+jobID+"/commands", map[string]any{
		"sequence":               1,
		"expected_lease_epoch":   leaseEpoch,
		"expected_state_version": jobStateVersion,
		"command":                "printf t2l03-real",
		"work_dir":               "/workspace",
		"timeout_ms":             20000,
	}))
	require.Equal(t, http.StatusCreated, command.Code, command.Body.String())
	commandData := t2l03ResponseData(t, command)
	require.Equal(t, "t2l03-real", commandData["stdout"])
	require.Equal(t, float64(0), commandData["exit_code"])

	events := httptest.NewRecorder()
	engine.ServeHTTP(events, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/events?job_id="+jobID+"&after_seq=0&limit=20", nil))
	require.Equal(t, http.StatusOK, events.Code, events.Body.String())
	require.GreaterOrEqual(t, len(t2l03ResponseData(t, events)["events"].([]any)), 3)

	runningJob, err := controlRepo.GetJobByID(context.Background(), tenantID, jobID)
	require.NoError(t, err)
	require.NoError(t, runner.Cleanup(context.Background(), workbenchrunner.CleanupRequest{
		TenantID:             tenantID,
		JobID:                jobID,
		ExpectedStateVersion: runningJob.StateVersion,
		TerminalState:        types.WorkbenchJobStateLost,
		Reason:               "resource_exceeded",
	}))
	audit := httptest.NewRecorder()
	engine.ServeHTTP(audit, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/audit?job_id="+jobID+"&state=PENDING&limit=20", nil))
	require.Equal(t, http.StatusOK, audit.Code, audit.Body.String())
	require.NotContains(t, audit.Body.String(), runningJob.BackendIdentity)
	require.NotContains(t, audit.Body.String(), "backend_identity")
	auditRows := t2l03ResponseData(t, audit)["audit"].([]any)
	require.NotEmpty(t, auditRows)
	var foundResourceTermination bool
	for _, raw := range auditRows {
		row := raw.(map[string]any)
		if row["action"] != string(types.WorkbenchAuditActionJobTerminated) {
			continue
		}
		payload := row["payload"].(map[string]any)
		require.Equal(t, "failure", row["outcome"])
		require.Equal(t, "resource_exceeded", payload["reason"])
		require.Equal(t, true, payload["resource_exceeded"])
		foundResourceTermination = true
	}
	require.True(t, foundResourceTermination, audit.Body.String())

	ticket := httptest.NewRecorder()
	engine.ServeHTTP(ticket, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/stream-ticket", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch,
	}))
	require.Equal(t, http.StatusCreated, ticket.Code, ticket.Body.String())
	ticketData := t2l03ResponseData(t, ticket)
	token, _ := ticketData["ticket"].(string)
	require.NotEmpty(t, token)

	streamURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/sessions/" + chatID + "/workbench/stream?ticket=" + url.QueryEscape(token) + "&workbench_id=" + url.QueryEscape(workbenchID) + "&job_id=" + url.QueryEscape(jobID) + "&lease_epoch=" + strconv.FormatInt(leaseEpoch, 10)
	conn, _, err := websocket.DefaultDialer.Dial(streamURL, nil)
	require.NoError(t, err)
	defer conn.Close()
	var ready map[string]any
	require.NoError(t, conn.ReadJSON(&ready))
	require.Equal(t, "workbench.stream.ready", ready["type"])
	require.Equal(t, workbenchID, ready["workbench_id"])
}

type t2l10EvidenceCapability struct{}

func (t2l10EvidenceCapability) Snapshot(_ context.Context, routeRegistered bool) service.WorkbenchCapabilitySnapshot {
	if !routeRegistered {
		return service.WorkbenchCapabilitySnapshot{Supported: false, Reason: service.WorkbenchCapabilityReasonRouteNotRegistered}
	}
	return service.WorkbenchCapabilitySnapshot{
		Supported:        true,
		EligibleBackends: []string{workbenchrunner.EligibleProtectedDockerBackend},
	}
}

func t2l10RealCapabilityProvider() workbenchCapabilityProvider {
	return t2l10EvidenceCapability{}
}

type t2l03RealSessionAuthorizer struct {
	db *gorm.DB
}

func (a *t2l03RealSessionAuthorizer) GetOwnedSession(ctx context.Context, id string) (*types.Session, error) {
	tenantID, _ := ctx.Value(types.TenantIDContextKey).(uint64)
	actorID, _ := ctx.Value(types.UserIDContextKey).(string)
	var session types.Session
	err := a.db.WithContext(ctx).Where("tenant_id = ? AND id = ? AND user_id = ?", tenantID, id, actorID).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperrors.ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func t2l03RealWorkbenchRouter(h *WorkbenchHandler, tenantID uint64, actorID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
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

func t2l03GormDSN(dbName string) string {
	return "host=" + os.Getenv("DB_HOST") + " port=" + os.Getenv("DB_PORT") + " user=" + os.Getenv("DB_USER") + " password=" + os.Getenv("DB_PASSWORD") + " dbname=" + dbName + " sslmode=disable TimeZone=UTC"
}

func t2l03PostgresURL(dbName string) string {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(os.Getenv("DB_HOST"), os.Getenv("DB_PORT")), Path: "/" + dbName}
	u.User = url.UserPassword(os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"))
	q := u.Query()
	q.Set("sslmode", "disable")
	q.Set("options", "-c app.skip_embedding=true")
	u.RawQuery = q.Encode()
	return u.String()
}
