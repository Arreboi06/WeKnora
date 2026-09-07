package container

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	workbenchrunner "github.com/Tencent/WeKnora/internal/workbench/runner"
	"github.com/stretchr/testify/require"
)

type t2L09ReadyRemoteClient struct {
	sandbox.RemoteSandboxClient
}

func (t2L09ReadyRemoteClient) Provider() sandbox.RemoteProvider {
	return sandbox.SandboxTypeDocker
}

type t2L09ControlRepository struct {
	repository.WorkbenchControlRepository
}

func TestT2L03WorkbenchRunnerBundleIsDefaultOff(t *testing.T) {
	t.Setenv(sandbox.WorkbenchEnabledEnv, "")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")

	bundle := newWorkbenchRunnerBundle(nil, nil, nil)
	require.Nil(t, bundle.runner)
}

func TestT2L03WorkbenchRunnerBundleRequiresDockerOptIn(t *testing.T) {
	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.DockerBackendEnabledEnv, "")

	bundle := newWorkbenchRunnerBundle(nil, nil, nil)
	require.Nil(t, bundle.runner)
}

func TestT2L09RuntimeDockerReadinessDoesNotCertifyProductionCapability(t *testing.T) {
	runner := workbenchrunner.NewProtectedDockerRunner(workbenchrunner.Config{
		Enabled:    true,
		TemplateID: "local-template",
	}, t2L09ReadyRemoteClient{}, t2L09ControlRepository{})

	name, ready := runner.EligibleBackendName()
	require.True(t, ready)
	require.NotEmpty(t, name)

	capability := newWorkbenchCapabilityService(nil, nil, nil, nil, nil)
	status := capability.Status(context.Background(), true, nil)
	require.Empty(t, status.EligibleBackends, "runtime readiness is not an evidence-bound certification")
}
