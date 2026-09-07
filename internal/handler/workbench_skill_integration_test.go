//go:build t2_l03_real && t2_l04_real && t2_l05_real && t2_l06_real

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

func TestT2L06RealHTTPPostgresDockerPresentationSkillCandidate(t *testing.T) {
	require.Equal(t, "1", os.Getenv("WEKNORA_T2_L06_REAL"), "WEKNORA_T2_L06_REAL must be set for the real T2-L06 chain")
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "WEKNORA_T2_L06_DOCKER_IMAGE"} {
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

	const tenantID uint64 = 7907
	const actorID = "actor-skill-real"
	const chatID = "t2l06-real-chat"
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE tenant_id = ?", tenantID).Error)
	require.NoError(t, db.Exec("INSERT INTO sessions (id, tenant_id, title, user_id) VALUES (?, ?, ?, ?)", chatID, tenantID, "T2L06 real skill chain", actorID).Error)

	sessionRepo := repository.NewSessionRepository(db)
	workbenchRepo := repository.NewWorkbenchSessionRepository(db)
	controlRepo := repository.NewWorkbenchControlRepository(db)
	artifactRepo := repository.NewWorkbenchArtifactRepository(db)
	skillRunRepo := repository.NewWorkbenchSkillRunRepository(db)
	workbenchSvc := service.NewWorkbenchSessionService(workbenchRepo, sessionRepo)
	controlSvc := service.NewWorkbenchControlService(controlRepo, workbenchRepo)

	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")

	dockerCfg := sandbox.DefaultConfig()
	dockerCfg.Type = sandbox.SandboxTypeDocker
	dockerCfg.DockerImage = strings.TrimSpace(os.Getenv("WEKNORA_T2_L06_DOCKER_IMAGE"))
	dockerCfg.DockerHost = strings.TrimSpace(os.Getenv("DOCKER_INTEGRATION_HOST"))
	if dockerCfg.DockerHost == "" {
		dockerCfg.DockerHost = strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	}
	dockerCfg.DockerTLSCertPath = strings.TrimSpace(os.Getenv("DOCKER_CERT_PATH"))
	dockerCfg.AllowPrivateEndpoints = true
	client, err := sandbox.NewDockerRemoteClient(dockerCfg)
	require.NoError(t, err)
	runner := workbenchrunner.NewProtectedDockerRunner(workbenchrunner.Config{Enabled: true, TemplateID: dockerCfg.DockerImage, IdleTimeout: 2 * time.Minute, DefaultCommandTimeout: 20 * time.Second}, client, controlRepo)
	fileSvc := service.NewWorkbenchFileService(controlRepo, client)
	artifactSvc := service.NewWorkbenchArtifactService(artifactRepo, fileSvc, controlRepo)
	skillSvc := service.NewWorkbenchPresentationSkillService(skillRunRepo, controlRepo, artifactSvc)

	h := newWorkbenchHandler(&t2l03RealSessionAuthorizer{db: db}, workbenchSvc, controlSvc, runner, NewWorkbenchStreamTicketStore(time.Minute), fileSvc, artifactSvc, skillSvc, t2l10RealCapabilityProvider())
	engine := t2l03RealWorkbenchRouter(h, tenantID, actorID)

	var jobID string
	defer func() {
		if jobID == "" {
			return
		}
		job, err := controlRepo.GetJobByID(context.Background(), tenantID, jobID)
		if err == nil && job != nil {
			_ = runner.Cleanup(context.Background(), workbenchrunner.CleanupRequest{TenantID: tenantID, JobID: job.ID, ExpectedStateVersion: job.StateVersion, TerminalState: types.WorkbenchJobStateCancelled, Reason: "t2l06_real_test_cleanup"})
		}
	}()

	start := httptest.NewRecorder()
	engine.ServeHTTP(start, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/start", map[string]any{
		"incarnation_id": "inc-skill-real-1", "sandbox_config_id": workbenchrunner.EligibleProtectedDockerBackend, "backend_type": "docker",
		"capability_snapshot": map[string]any{"sandbox.workbench": true}, "policy_snapshot": map[string]any{"network": "none"},
	}))
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	workbenchData := t2l03ResponseData(t, start)
	workbenchID, _ := workbenchData["id"].(string)
	leaseEpoch := int64(workbenchData["lease_epoch"].(float64))

	jobReq := httptest.NewRecorder()
	engine.ServeHTTP(jobReq, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/jobs", map[string]any{
		"workbench_id": workbenchID, "expected_lease_epoch": leaseEpoch, "start_nonce": "t2l06-real-nonce", "backend_type": "docker", "resource_policy_snapshot": map[string]any{"network": "none"},
	}))
	require.Equal(t, http.StatusCreated, jobReq.Code, jobReq.Body.String())
	jobData := t2l03ResponseData(t, jobReq)
	jobID, _ = jobData["id"].(string)
	jobStateVersion := int64(jobData["state_version"].(float64))

	pptxBytes := t2l05HandlerMinimalPPTX(t)
	encoded := base64.StdEncoding.EncodeToString(pptxBytes)
	command := "printf '%s' '" + encoded + "' | base64 -d > /workspace/output/slides.pptx"
	cmdReq := httptest.NewRecorder()
	engine.ServeHTTP(cmdReq, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/jobs/"+jobID+"/commands", map[string]any{
		"sequence": 1, "expected_lease_epoch": leaseEpoch, "expected_state_version": jobStateVersion,
		"command": command, "work_dir": "/workspace", "timeout_ms": 20000,
		"skill_name": "presentations", "skill_operation": "create_presentation",
	}))
	require.Equal(t, http.StatusCreated, cmdReq.Code, cmdReq.Body.String())
	cmdData := t2l03ResponseData(t, cmdReq)
	commandID, _ := cmdData["command_id"].(string)
	require.NotEmpty(t, commandID)
	require.Equal(t, float64(0), cmdData["exit_code"])
	require.NotContains(t, cmdReq.Body.String(), "backend_identity")

	publish := httptest.NewRecorder()
	engine.ServeHTTP(publish, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/skill-runs/presentation", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch, "command_id": commandID,
		"source_ref": map[string]any{"file_ref_version": 1, "root": "output", "segments": []string{"slides.pptx"}},
	}))
	require.Equal(t, http.StatusCreated, publish.Code, publish.Body.String())
	publishData := t2l03ResponseData(t, publish)
	skillRunID, _ := publishData["skill_run_id"].(string)
	require.NotEmpty(t, skillRunID)
	require.Equal(t, "presentations", publishData["skill_name"])
	artifactData, ok := publishData["artifact"].(map[string]any)
	require.True(t, ok, "body=%s", publish.Body.String())
	require.Equal(t, skillRunID, artifactData["skill_run_id"])
	require.Equal(t, "presentation_page", artifactData["preview_class"])
	require.NotContains(t, publish.Body.String(), "backend_identity")
	require.NotContains(t, publish.Body.String(), "container")

	artifactID, _ := artifactData["artifact_id"].(string)
	preview := httptest.NewRecorder()
	engine.ServeHTTP(preview, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/artifacts/"+artifactID+"/versions/1/preview", nil))
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Contains(t, preview.Header().Get("Content-Security-Policy"), "script-src 'none'")
	require.Contains(t, preview.Body.String(), "Slides: 1")

	download := httptest.NewRecorder()
	engine.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/artifacts/"+artifactID+"/versions/1/download", nil))
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	require.Equal(t, pptxBytes, download.Body.Bytes())

	var count int64
	require.NoError(t, db.Model(&types.WorkbenchSkillRun{}).Where("tenant_id = ? AND id = ? AND output_artifact_id = ?", tenantID, skillRunID, artifactID).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
