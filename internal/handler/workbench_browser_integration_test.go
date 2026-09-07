//go:build t2_l03_real && t2_l04_real && t2_l05_real && t2_l06_real && t2_l10_real

package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/database"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	workbenchrunner "github.com/Tencent/WeKnora/internal/workbench/runner"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type t2l10BrowserCompletion struct {
	Status  string
	Details map[string]any
}

func TestT2L10RealBrowserWorkbenchJourney(t *testing.T) {
	require.Equal(t, "1", os.Getenv("WEKNORA_T2_L10_REAL"), "WEKNORA_T2_L10_REAL must be set")
	for _, key := range []string{"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "WEKNORA_T2_L10_DOCKER_IMAGE", "WEKNORA_T2_L10_SERVER_ADDR"} {
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

	const tenantID uint64 = 7910
	const actorID = "actor-browser-real"
	const chatID = "t2l10-browser-chat"
	require.NoError(t, db.Exec("DELETE FROM sessions WHERE tenant_id = ?", tenantID).Error)
	require.NoError(t, db.Exec("INSERT INTO sessions (id, tenant_id, title, user_id) VALUES (?, ?, ?, ?)", chatID, tenantID, "T2L10 browser journey", actorID).Error)

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
	dockerCfg.DockerImage = strings.TrimSpace(os.Getenv("WEKNORA_T2_L10_DOCKER_IMAGE"))
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
		IdleTimeout:           3 * time.Minute,
		DefaultCommandTimeout: 30 * time.Second,
	}, client, controlRepo)
	fileSvc := service.NewWorkbenchFileService(controlRepo, client)
	artifactSvc := service.NewWorkbenchArtifactService(artifactRepo, fileSvc, controlRepo)
	skillSvc := service.NewWorkbenchPresentationSkillService(skillRunRepo, controlRepo, artifactSvc)
	handler := newWorkbenchHandler(
		&t2l03RealSessionAuthorizer{db: db},
		workbenchSvc,
		controlSvc,
		runner,
		NewWorkbenchStreamTicketStore(time.Minute),
		fileSvc,
		artifactSvc,
		skillSvc,
		t2l10RealCapabilityProvider(),
	)
	engine := t2l03RealWorkbenchRouter(handler, tenantID, actorID)

	pptxB64 := base64.StdEncoding.EncodeToString(t2l05HandlerMinimalPPTX(t))
	completed := make(chan t2l10BrowserCompletion, 1)
	var networkProbes atomic.Int32
	engine.GET("/api/v1/t2-e2e/bootstrap", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"data": gin.H{
				"session_id":        chatID,
				"eligible_backends": []string{workbenchrunner.EligibleProtectedDockerBackend},
				"pptx_b64":          pptxB64,
			},
		})
	})
	engine.Any("/api/v1/t2-e2e/network-probe", func(c *gin.Context) {
		networkProbes.Add(1)
		c.Status(http.StatusNoContent)
	})
	engine.POST("/api/v1/t2-e2e/complete", func(c *gin.Context) {
		var result t2l10BrowserCompletion
		if err := c.ShouldBindJSON(&result); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false})
			return
		}
		select {
		case completed <- result:
		default:
		}
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	listener, err := net.Listen("tcp", strings.TrimSpace(os.Getenv("WEKNORA_T2_L10_SERVER_ADDR")))
	require.NoError(t, err)
	server := &http.Server{Handler: engine, ReadHeaderTimeout: 5 * time.Second}
	serveErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	fmt.Printf("T2_L10_SERVER_URL=http://%s\n", listener.Addr().String())

	var result t2l10BrowserCompletion
	select {
	case result = <-completed:
	case err := <-serveErrors:
		require.NoError(t, err)
	case <-time.After(4 * time.Minute):
		t.Fatal("browser journey timed out")
	}
	require.Equal(t, "pass", result.Status, "browser details=%v", result.Details)
	require.Zero(t, networkProbes.Load(), "active HTML preview escaped CSP network isolation")

	var artifactCount int64
	require.NoError(t, db.Model(&types.WorkbenchArtifactVersion{}).Where("tenant_id = ? AND chat_session_id = ?", tenantID, chatID).Count(&artifactCount).Error)
	require.GreaterOrEqual(t, artifactCount, int64(3))
	var skillCount int64
	require.NoError(t, db.Model(&types.WorkbenchSkillRun{}).Where("tenant_id = ? AND chat_session_id = ? AND state = ?", tenantID, chatID, types.WorkbenchSkillRunStateSucceeded).Count(&skillCount).Error)
	require.Equal(t, int64(1), skillCount)
	var job types.WorkbenchJob
	require.NoError(t, db.Where("tenant_id = ? AND chat_session_id = ?", tenantID, chatID).Order("created_at DESC").First(&job).Error)
	require.NotEmpty(t, job.BackendIdentity)
	require.NoError(t, runner.Cleanup(context.Background(), workbenchrunner.CleanupRequest{
		TenantID:             tenantID,
		JobID:                job.ID,
		ExpectedStateVersion: job.StateVersion,
		TerminalState:        types.WorkbenchJobStateCancelled,
		Reason:               "t2l10_browser_evidence_cleanup",
	}))
	_, getErr := client.Get(context.Background(), job.BackendIdentity)
	require.Error(t, getErr, "cleanup must remove the real Docker backend identity")
	require.True(t, sandbox.IsRemoteNotFound(getErr), "cleanup get error=%v", getErr)
	var auditRows []types.WorkbenchAuditOutbox
	require.NoError(t, db.Where("tenant_id = ? AND workbench_job_id = ?", tenantID, job.ID).Order("created_at ASC").Find(&auditRows).Error)
	require.GreaterOrEqual(t, len(auditRows), 3)
	actions := make(map[types.WorkbenchAuditAction]bool, len(auditRows))
	for _, row := range auditRows {
		require.Equal(t, tenantID, row.TenantID)
		require.Equal(t, job.ID, row.WorkbenchJobID)
		actions[row.Action] = true
	}
	require.True(t, actions[types.WorkbenchAuditActionJobStarted])
	require.True(t, actions[types.WorkbenchAuditActionCommandSucceeded])
	require.True(t, actions[types.WorkbenchAuditActionJobTerminated])
}
