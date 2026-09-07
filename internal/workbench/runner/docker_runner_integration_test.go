//go:build t2_l02_docker

package runner

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L02ProtectedDockerRunnerRealDocker(t *testing.T) {
	if os.Getenv("WEKNORA_T2_L02_DOCKER") != "1" {
		t.Fatal("WEKNORA_T2_L02_DOCKER=1 is required for the real Docker runner proof")
	}
	image := strings.TrimSpace(os.Getenv("WEKNORA_T2_L02_DOCKER_IMAGE"))
	if image == "" {
		t.Fatal("WEKNORA_T2_L02_DOCKER_IMAGE is required")
	}

	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	cfg := sandbox.DefaultConfig()
	cfg.Type = sandbox.SandboxTypeDocker
	cfg.DockerImage = image
	cfg.DockerHost = strings.TrimSpace(os.Getenv("DOCKER_INTEGRATION_HOST"))
	cfg.DockerTLSCertPath = strings.TrimSpace(os.Getenv("DOCKER_CERT_PATH"))
	cfg.AllowPrivateEndpoints = true
	cfg.DockerNetworkMode = "none"
	cfg.DockerIdleTTL = 3 * time.Minute
	client, err := sandbox.NewDockerRemoteClient(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	require.NoError(t, client.Health(ctx))

	control := newFakeControlRepo()
	control.jobs["10/job-real"] = &types.WorkbenchJob{
		ID:                     "job-real",
		TenantID:               10,
		WorkbenchSessionID:     "wb-real",
		ChatSessionID:          "chat-real",
		IncarnationID:          "inc-real",
		LeaseEpoch:             1,
		State:                  types.WorkbenchJobStateQueued,
		StateVersion:           0,
		ResourcePolicySnapshot: types.JSONMap{"network": "none"},
		BackendType:            string(sandbox.SandboxTypeDocker),
		CreatedBy:              "user-real",
	}

	r := NewProtectedDockerRunner(Config{
		Enabled:               true,
		TemplateID:            image,
		IdleTimeout:           3 * time.Minute,
		DefaultCommandTimeout: 15 * time.Second,
	}, client, control)
	job, err := r.Start(ctx, StartRequest{TenantID: 10, JobID: "job-real"})
	require.NoError(t, err)
	require.NotEmpty(t, job.BackendIdentity)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_ = client.Delete(cleanupCtx, job.BackendIdentity)
	})

	control.commands["10/cmd-real"] = &types.WorkbenchCommand{
		ID:                 "cmd-real",
		TenantID:           10,
		WorkbenchJobID:     job.ID,
		WorkbenchSessionID: job.WorkbenchSessionID,
		Sequence:           1,
		Kind:               "shell",
		Payload: types.JSONMap{
			"command":    "printf 'uid='; id -u; printf '\\nuser='; whoami; printf '\\npwd='; pwd",
			"work_dir":   "/workspace",
			"timeout_ms": float64(15000),
		},
		State:        types.WorkbenchCommandStateQueued,
		StateVersion: 0,
		CreatedBy:    "user-real",
	}
	result, err := r.RunCommand(ctx, RunCommandRequest{TenantID: 10, CommandID: "cmd-real"})
	require.NoError(t, err)
	require.Equal(t, 0, result.ExitCode)
	require.NotContains(t, result.Stdout, "uid=0")
	require.Contains(t, result.Stdout, "user=user")
	require.Contains(t, result.Stdout, "pwd=/workspace")
	require.Equal(t, []types.WorkbenchAuditAction{types.WorkbenchAuditActionCommandSucceeded}, auditActions(control.audit))

	err = r.Cleanup(ctx, CleanupRequest{
		TenantID:             10,
		JobID:                job.ID,
		ExpectedStateVersion: control.jobs["10/job-real"].StateVersion,
		TerminalState:        types.WorkbenchJobStateSucceeded,
		Reason:               "integration_done",
	})
	require.NoError(t, err)
	_, err = client.Get(ctx, job.BackendIdentity)
	require.True(t, sandbox.IsRemoteNotFound(err), "container should be gone after cleanup, got %v", err)
}
