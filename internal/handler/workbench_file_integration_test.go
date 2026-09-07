//go:build t2_l03_real && t2_l04_real

package handler

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	workbenchrunner "github.com/Tencent/WeKnora/internal/workbench/runner"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestT2L04RealHTTPPostgresDockerFileOps(t *testing.T) {
	require.Equal(t, "1", os.Getenv("WEKNORA_T2_L04_REAL"), "WEKNORA_T2_L04_REAL must be set for the real T2-L04 chain")
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "WEKNORA_T2_L04_DOCKER_IMAGE"} {
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

	const tenantID uint64 = 7904
	const actorID = "actor-file-real"
	const chatID = "t2l04-real-chat"
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE tenant_id = ?", tenantID).Error)
	require.NoError(t, db.Exec("INSERT INTO sessions (id, tenant_id, title, user_id) VALUES (?, ?, ?, ?)", chatID, tenantID, "T2L04 real file chain", actorID).Error)

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
	dockerCfg.DockerImage = strings.TrimSpace(os.Getenv("WEKNORA_T2_L04_DOCKER_IMAGE"))
	dockerCfg.DockerHost = strings.TrimSpace(os.Getenv("DOCKER_INTEGRATION_HOST"))
	if dockerCfg.DockerHost == "" {
		dockerCfg.DockerHost = strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	}
	dockerCfg.DockerTLSCertPath = strings.TrimSpace(os.Getenv("DOCKER_CERT_PATH"))
	dockerCfg.AllowPrivateEndpoints = true
	client, err := sandbox.NewDockerRemoteClient(dockerCfg)
	require.NoError(t, err)
	runner := workbenchrunner.NewProtectedDockerRunner(workbenchrunner.Config{
		Enabled:               true,
		TemplateID:            dockerCfg.DockerImage,
		IdleTimeout:           2 * time.Minute,
		DefaultCommandTimeout: 20 * time.Second,
	}, client, controlRepo)
	fileSvc := service.NewWorkbenchFileService(controlRepo, client)

	h := newWorkbenchHandler(&t2l03RealSessionAuthorizer{db: db}, workbenchSvc, controlSvc, runner, NewWorkbenchStreamTicketStore(time.Minute), fileSvc, t2l10RealCapabilityProvider())
	engine := t2l03RealWorkbenchRouter(h, tenantID, actorID)

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
				Reason:               "t2l04_real_test_cleanup",
			})
		}
	}()

	start := httptest.NewRecorder()
	engine.ServeHTTP(start, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/start", map[string]any{
		"incarnation_id":      "inc-file-real-1",
		"sandbox_config_id":   workbenchrunner.EligibleProtectedDockerBackend,
		"backend_type":        "docker",
		"capability_snapshot": map[string]any{"sandbox.workbench": true},
		"policy_snapshot":     map[string]any{"network": "none"},
	}))
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	workbenchData := t2l03ResponseData(t, start)
	workbenchID, _ := workbenchData["id"].(string)
	leaseEpoch := int64(workbenchData["lease_epoch"].(float64))

	job := httptest.NewRecorder()
	engine.ServeHTTP(job, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/jobs", map[string]any{
		"workbench_id":             workbenchID,
		"expected_lease_epoch":     leaseEpoch,
		"start_nonce":              "t2l04-real-nonce",
		"backend_type":             "docker",
		"resource_policy_snapshot": map[string]any{"network": "none"},
	}))
	require.Equal(t, http.StatusCreated, job.Code, job.Body.String())
	jobData := t2l03ResponseData(t, job)
	jobID, _ = jobData["id"].(string)
	require.NotContains(t, job.Body.String(), "backend_identity")

	fileRef := map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"notes", "real.txt"}}
	upload := httptest.NewRecorder()
	engine.ServeHTTP(upload, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/files/upload", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch,
		"ref": fileRef, "content_b64": base64.StdEncoding.EncodeToString([]byte("hello-l04")),
	}))
	require.Equal(t, http.StatusCreated, upload.Code, upload.Body.String())
	require.NotContains(t, upload.Body.String(), "container")

	browse := httptest.NewRecorder()
	engine.ServeHTTP(browse, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/files/browse", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch,
		"ref": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"notes"}},
	}))
	require.Equal(t, http.StatusOK, browse.Code, browse.Body.String())
	require.Contains(t, browse.Body.String(), "real.txt")
	require.NotContains(t, browse.Body.String(), "backend_identity")

	download := httptest.NewRecorder()
	engine.ServeHTTP(download, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/files/download", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch, "ref": fileRef,
	}))
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("hello-l04")), t2l03ResponseData(t, download)["content_b64"])

	rename := httptest.NewRecorder()
	renamedRef := map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"notes", "renamed.txt"}}
	engine.ServeHTTP(rename, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/files/rename", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch,
		"source": fileRef, "target": renamedRef,
	}))
	require.Equal(t, http.StatusOK, rename.Code, rename.Body.String())

	deleteReq := httptest.NewRecorder()
	engine.ServeHTTP(deleteReq, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/files/delete", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch, "ref": renamedRef,
	}))
	require.Equal(t, http.StatusOK, deleteReq.Code, deleteReq.Body.String())

	missing := httptest.NewRecorder()
	engine.ServeHTTP(missing, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/files/download", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch, "ref": renamedRef,
	}))
	require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())
}
