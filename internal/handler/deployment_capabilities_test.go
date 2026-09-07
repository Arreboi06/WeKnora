package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/sandbox"
)

func TestDeploymentCapabilityKeysMatchFrontend(t *testing.T) {
	frontendKeys, err := readFrontendDeploymentCapabilityKeys()
	if err != nil {
		t.Fatalf("read frontend capability keys: %v", err)
	}

	if !slices.Equal(DeploymentCapabilityKeys, frontendKeys) {
		t.Fatalf("backend keys = %#v, frontend keys = %#v", DeploymentCapabilityKeys, frontendKeys)
	}
}

func TestBuildDeploymentCapabilitiesIncludesAllKeys(t *testing.T) {
	result := BuildDeploymentCapabilities("standard", DeploymentFeatureAvailability{
		Organizations: true,
		Agents:        true,
		IM:            true,
		Embed:         true,
		API:           true,
		MCP:           true,
		WebSearch:     true,
		VectorStore:   true,
		Storage:       true,
		Sandbox:       true,
	})

	for _, key := range DeploymentCapabilityKeys {
		if _, ok := result.Capabilities[key]; !ok {
			t.Fatalf("missing capability key %q", key)
		}
	}
}

func TestT2M01AWorkbenchCapabilityIsAdvertisedFailClosed(t *testing.T) {
	result := BuildDeploymentCapabilities("standard", DeploymentFeatureAvailability{})
	capability, ok := result.Capabilities["sandbox.workbench"]
	if !ok {
		t.Fatal("missing sandbox.workbench capability")
	}
	if capability.Supported {
		t.Fatal("sandbox.workbench must be unsupported by default")
	}
	if capability.Reason != "feature_disabled" {
		t.Fatalf("reason = %q, want feature_disabled", capability.Reason)
	}
	if !slices.Contains(DeploymentCapabilityKeys, "sandbox.workbench") {
		t.Fatal("DeploymentCapabilityKeys must include sandbox.workbench")
	}
}
func TestOverlayLiveDockerSandboxCapabilityIgnoresStartupSnapshot(t *testing.T) {
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.DockerBackendEnabledEnv, "")

	snapshot := BuildDeploymentCapabilities("standard", DeploymentFeatureAvailability{
		Sandbox:       true,
		SandboxDocker: true,
		Workbench: service.WorkbenchCapabilityStatus{
			Known:            true,
			FeatureEnabled:   true,
			DatabaseDialect:  "postgres",
			SchemaReady:      true,
			RouteRegistered:  true,
			EligibleBackends: []string{"local-protected-docker"},
		},
	})
	live := overlayLiveDockerSandboxCapability(snapshot)
	docker := live.Capabilities["settings.sandbox.docker"]
	if docker.Supported {
		t.Fatal("live env off must hide docker even if the startup snapshot was on")
	}
	if docker.Reason != "docker_backend_disabled" {
		t.Fatalf("reason = %q, want docker_backend_disabled", docker.Reason)
	}
	workbench := live.Capabilities["sandbox.workbench"]
	if workbench.Supported {
		t.Fatal("live docker off must hide workbench even if startup snapshot was fully supported")
	}
	if workbench.Reason != service.WorkbenchCapabilityReasonNoEligibleBackend {
		t.Fatalf("workbench reason = %q, want no_eligible_backend", workbench.Reason)
	}
	if len(workbench.EligibleBackends) != 0 {
		t.Fatalf("workbench eligible_backends = %#v, want empty", workbench.EligibleBackends)
	}

	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	enabled := overlayLiveDockerSandboxCapability(snapshot)
	if !enabled.Capabilities["settings.sandbox.docker"].Supported {
		t.Fatal("live env true must expose docker")
	}
	if !enabled.Capabilities["sandbox.workbench"].Supported {
		t.Fatalf("live docker true must preserve supported workbench snapshot: %#v", enabled.Capabilities["sandbox.workbench"])
	}
}

func readFrontendDeploymentCapabilityKeys() ([]string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, os.ErrInvalid
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	frontendPath := filepath.Join(repoRoot, "frontend", "src", "config", "deploymentCapabilities.ts")
	content, err := os.ReadFile(frontendPath)
	if err != nil {
		return nil, err
	}

	re := regexp.MustCompile(`(?s)export const DEPLOYMENT_CAPABILITY_KEYS = \[(.*?)\]`)
	match := re.FindSubmatch(content)
	if len(match) < 2 {
		return nil, os.ErrInvalid
	}

	var keys []string
	for _, line := range strings.Split(string(match[1]), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, ","))
		if line == "" {
			continue
		}
		line = strings.Trim(line, `'`)
		keys = append(keys, line)
	}
	return keys, nil
}

func TestT2M01AWorkbenchCapabilityReasonPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		status service.WorkbenchCapabilityStatus
		reason string
	}{
		{name: "flag off", status: service.WorkbenchCapabilityStatus{Known: true}, reason: service.WorkbenchCapabilityReasonFeatureDisabled},
		{name: "sqlite", status: service.WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "sqlite"}, reason: service.WorkbenchCapabilityReasonUnsupportedDatabase},
		{name: "missing migration", status: service.WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "postgres"}, reason: service.WorkbenchCapabilityReasonMigrationUnavailable},
		{name: "no route", status: service.WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "postgres", SchemaReady: true, EligibleBackends: []string{"local-protected-docker"}}, reason: service.WorkbenchCapabilityReasonRouteNotRegistered},
		{name: "no backend", status: service.WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "postgres", SchemaReady: true, RouteRegistered: true}, reason: service.WorkbenchCapabilityReasonNoEligibleBackend},
		{name: "unknown last", status: service.WorkbenchCapabilityStatus{FeatureEnabled: true, DatabaseDialect: "postgres", SchemaReady: true, RouteRegistered: true, EligibleBackends: []string{"local-protected-docker"}}, reason: service.WorkbenchCapabilityReasonUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capability := BuildDeploymentCapabilities("standard", DeploymentFeatureAvailability{Workbench: tc.status}).Capabilities["sandbox.workbench"]
			if capability.Supported {
				t.Fatalf("sandbox.workbench unexpectedly supported for %s", tc.name)
			}
			if capability.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", capability.Reason, tc.reason)
			}
		})
	}

	capability := BuildDeploymentCapabilities("standard", DeploymentFeatureAvailability{Workbench: service.WorkbenchCapabilityStatus{
		Known:            true,
		FeatureEnabled:   true,
		DatabaseDialect:  "postgres",
		SchemaReady:      true,
		RouteRegistered:  true,
		EligibleBackends: []string{"local-protected-docker"},
	}}).Capabilities["sandbox.workbench"]
	if !capability.Supported {
		t.Fatalf("sandbox.workbench should be supported only when every prerequisite is true: %#v", capability)
	}
	if got := strings.Join(capability.EligibleBackends, ","); got != "local-protected-docker" {
		t.Fatalf("eligible_backends = %q, want local-protected-docker", got)
	}
}
