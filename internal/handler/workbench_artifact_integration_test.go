//go:build t2_l03_real && t2_l04_real && t2_l05_real

package handler

import (
	"archive/zip"
	"bytes"
	"context"
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

func TestT2L05RealHTTPPostgresDockerArtifactPreview(t *testing.T) {
	require.Equal(t, "1", os.Getenv("WEKNORA_T2_L05_REAL"), "WEKNORA_T2_L05_REAL must be set for the real T2-L05 chain")
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "WEKNORA_T2_L05_DOCKER_IMAGE"} {
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

	const tenantID uint64 = 7905
	const otherTenantID uint64 = 7906
	const actorID = "actor-artifact-real"
	const chatID = "t2l05-real-chat"
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE tenant_id IN (?, ?)", tenantID, otherTenantID).Error)
	require.NoError(t, db.Exec("INSERT INTO sessions (id, tenant_id, title, user_id) VALUES (?, ?, ?, ?)", chatID, tenantID, "T2L05 real artifact chain", actorID).Error)

	sessionRepo := repository.NewSessionRepository(db)
	workbenchRepo := repository.NewWorkbenchSessionRepository(db)
	controlRepo := repository.NewWorkbenchControlRepository(db)
	artifactRepo := repository.NewWorkbenchArtifactRepository(db)
	workbenchSvc := service.NewWorkbenchSessionService(workbenchRepo, sessionRepo)
	controlSvc := service.NewWorkbenchControlService(controlRepo, workbenchRepo)

	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")

	dockerCfg := sandbox.DefaultConfig()
	dockerCfg.Type = sandbox.SandboxTypeDocker
	dockerCfg.DockerImage = strings.TrimSpace(os.Getenv("WEKNORA_T2_L05_DOCKER_IMAGE"))
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
	artifactSvc := service.NewWorkbenchArtifactService(artifactRepo, fileSvc, controlRepo)

	h := newWorkbenchHandler(&t2l03RealSessionAuthorizer{db: db}, workbenchSvc, controlSvc, runner, NewWorkbenchStreamTicketStore(time.Minute), fileSvc, artifactSvc, t2l10RealCapabilityProvider())
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
				Reason:               "t2l05_real_test_cleanup",
			})
		}
	}()

	start := httptest.NewRecorder()
	engine.ServeHTTP(start, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/start", map[string]any{
		"incarnation_id":      "inc-artifact-real-1",
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

	jobReq := httptest.NewRecorder()
	engine.ServeHTTP(jobReq, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/jobs", map[string]any{
		"workbench_id":             workbenchID,
		"expected_lease_epoch":     leaseEpoch,
		"start_nonce":              "t2l05-real-nonce",
		"backend_type":             "docker",
		"resource_policy_snapshot": map[string]any{"network": "none"},
	}))
	require.Equal(t, http.StatusCreated, jobReq.Code, jobReq.Body.String())
	jobData := t2l03ResponseData(t, jobReq)
	jobID, _ = jobData["id"].(string)
	jobStateVersion := int64(jobData["state_version"].(float64))
	require.NotEmpty(t, jobID)
	require.NotContains(t, jobReq.Body.String(), "backend_identity")

	csvCommandID := t2l05RunCommand(t, engine, chatID, jobID, leaseEpoch, jobStateVersion, 1, "printf '%s\n' 'name,value' 'alpha,<script>alert(1)</script>' > /workspace/output/report.csv")
	htmlCommandID := t2l05RunCommand(t, engine, chatID, jobID, leaseEpoch, jobStateVersion, 2, "printf '%s' '<html><script>document.cookie=\"x=1\"</script><body>active</body></html>' > /workspace/output/index.html")
	pptxCommandID := t2l05RunCommand(t, engine, chatID, jobID, leaseEpoch, jobStateVersion, 3, "printf '%s' 'pptx seeded' > /workspace/output/pptx.marker")

	jobRow, err := controlRepo.GetJobByID(context.Background(), tenantID, jobID)
	require.NoError(t, err)
	require.NotEmpty(t, jobRow.BackendIdentity)
	handle, err := client.Connect(context.Background(), sandbox.RemoteConnectRequest{SandboxID: jobRow.BackendIdentity})
	require.NoError(t, err)
	require.NoError(t, client.WriteFile(context.Background(), handle, "/workspace/output/slides.pptx", t2l05HandlerMinimalPPTX(t)))

	csvData := t2l05PublishArtifact(t, engine, chatID, workbenchID, jobID, leaseEpoch, csvCommandID, t2l05Ref(types.WorkbenchFileRootOutput, "report.csv"))
	require.Equal(t, "table_csv", csvData["preview_class"])
	require.Equal(t, csvCommandID, csvData["command_id"])
	require.Empty(t, csvData["message_id"])
	require.Empty(t, csvData["skill_run_id"])
	csvArtifactID, _ := csvData["artifact_id"].(string)
	require.NotEmpty(t, csvArtifactID)
	require.NotContains(t, csvData, "backend_identity")

	csvPreview := httptest.NewRecorder()
	engine.ServeHTTP(csvPreview, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/artifacts/"+csvArtifactID+"/versions/1/preview", nil))
	require.Equal(t, http.StatusOK, csvPreview.Code, csvPreview.Body.String())
	require.Contains(t, csvPreview.Header().Get("Content-Security-Policy"), "script-src 'none'")
	require.Empty(t, csvPreview.Header().Values("Set-Cookie"))
	require.Contains(t, csvPreview.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;")
	require.NotContains(t, csvPreview.Body.String(), "<script>alert")

	csvDownload := httptest.NewRecorder()
	engine.ServeHTTP(csvDownload, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/artifacts/"+csvArtifactID+"/versions/1/download", nil))
	require.Equal(t, http.StatusOK, csvDownload.Code, csvDownload.Body.String())
	require.Contains(t, csvDownload.Header().Get("Content-Disposition"), "attachment")
	require.Equal(t, "name,value\nalpha,<script>alert(1)</script>\n", csvDownload.Body.String())

	htmlData := t2l05PublishArtifact(t, engine, chatID, workbenchID, jobID, leaseEpoch, htmlCommandID, t2l05Ref(types.WorkbenchFileRootOutput, "index.html"))
	require.Equal(t, "html_active", htmlData["preview_class"])
	htmlArtifactID, _ := htmlData["artifact_id"].(string)
	htmlPreview := httptest.NewRecorder()
	engine.ServeHTTP(htmlPreview, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/artifacts/"+htmlArtifactID+"/versions/1/preview", nil))
	require.Equal(t, http.StatusOK, htmlPreview.Code, htmlPreview.Body.String())
	require.Contains(t, htmlPreview.Header().Get("Content-Security-Policy"), "sandbox allow-scripts")
	require.Contains(t, htmlPreview.Header().Get("Content-Security-Policy"), "connect-src 'none'")
	require.Empty(t, htmlPreview.Header().Values("Set-Cookie"))
	require.Contains(t, htmlPreview.Body.String(), "document.cookie")
	require.NotContains(t, htmlPreview.Body.String(), jobRow.BackendIdentity)

	pptxData := t2l05PublishArtifact(t, engine, chatID, workbenchID, jobID, leaseEpoch, pptxCommandID, t2l05Ref(types.WorkbenchFileRootOutput, "slides.pptx"))
	require.Equal(t, "presentation_page", pptxData["preview_class"])
	pptxArtifactID, _ := pptxData["artifact_id"].(string)
	pptxPreview := httptest.NewRecorder()
	engine.ServeHTTP(pptxPreview, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/artifacts/"+pptxArtifactID+"/versions/1/preview", nil))
	require.Equal(t, http.StatusOK, pptxPreview.Code, pptxPreview.Body.String())
	require.Contains(t, pptxPreview.Header().Get("Content-Security-Policy"), "script-src 'none'")
	require.Contains(t, pptxPreview.Body.String(), "Slides: 1")

	aliasDenied := httptest.NewRecorder()
	engine.ServeHTTP(aliasDenied, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/artifacts", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch,
		"command_id": csvCommandID, "source_ref": t2l05Ref(types.WorkbenchFileRootWorkspace, "output", "report.csv"),
	}))
	require.Equal(t, http.StatusForbidden, aliasDenied.Code, aliasDenied.Body.String())
	require.Equal(t, "path_denied", t2l03ResponseCode(t, aliasDenied))

	otherEngine := t2l03RealWorkbenchRouter(h, otherTenantID, actorID)
	crossTenant := httptest.NewRecorder()
	otherEngine.ServeHTTP(crossTenant, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+chatID+"/workbench/artifacts/"+csvArtifactID+"/versions/1", nil))
	require.Equal(t, http.StatusNotFound, crossTenant.Code, crossTenant.Body.String())
	require.NotContains(t, crossTenant.Body.String(), csvArtifactID)
}

func t2l05RunCommand(t *testing.T, engine http.Handler, chatID, jobID string, leaseEpoch, jobStateVersion, sequence int64, command string) string {
	t.Helper()
	req := httptest.NewRecorder()
	engine.ServeHTTP(req, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/jobs/"+jobID+"/commands", map[string]any{
		"sequence":               sequence,
		"expected_lease_epoch":   leaseEpoch,
		"expected_state_version": jobStateVersion,
		"command":                command,
		"work_dir":               "/workspace",
		"timeout_ms":             20000,
	}))
	require.Equal(t, http.StatusCreated, req.Code, req.Body.String())
	data := t2l03ResponseData(t, req)
	commandID, _ := data["command_id"].(string)
	require.NotEmpty(t, commandID)
	require.Equal(t, float64(0), data["exit_code"])
	require.NotContains(t, req.Body.String(), "backend_identity")
	return commandID
}

func t2l05PublishArtifact(t *testing.T, engine http.Handler, chatID, workbenchID, jobID string, leaseEpoch int64, commandID string, sourceRef map[string]any) map[string]any {
	t.Helper()
	publish := httptest.NewRecorder()
	engine.ServeHTTP(publish, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/"+chatID+"/workbench/artifacts", map[string]any{
		"workbench_id": workbenchID, "job_id": jobID, "expected_lease_epoch": leaseEpoch,
		"command_id": commandID, "message_id": "spoofed-message", "skill_run_id": "spoofed-skill", "source_ref": sourceRef,
	}))
	require.Equal(t, http.StatusCreated, publish.Code, publish.Body.String())
	require.NotContains(t, publish.Body.String(), "backend_identity")
	require.NotContains(t, publish.Body.String(), "container")
	return t2l03ResponseData(t, publish)
}

func t2l05Ref(root types.WorkbenchFileRoot, segments ...string) map[string]any {
	return map[string]any{"file_ref_version": 1, "root": string(root), "segments": segments}
}

func t2l05HandlerMinimalPPTX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	contentTypes, err := zw.Create("[Content_Types].xml")
	require.NoError(t, err)
	_, err = contentTypes.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`))
	require.NoError(t, err)
	slide, err := zw.Create("ppt/slides/slide1.xml")
	require.NoError(t, err)
	_, err = slide.Write([]byte(`<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:sld>`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
