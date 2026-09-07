package container

import (
	"context"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sandbox"
	workbenchrunner "github.com/Tencent/WeKnora/internal/workbench/runner"
	"gorm.io/gorm"
)

type workbenchRunnerBundle struct {
	runner             *workbenchrunner.ProtectedDockerRunner
	files              *service.WorkbenchFileService
	artifacts          *service.WorkbenchArtifactService
	presentationSkills *service.WorkbenchPresentationSkillService
}

// newWorkbenchCapabilityService deliberately has no runtime-runner input.
// A configured Docker adapter is not an evidence-bound backend certification;
// production eligibility must stay empty until an independently verified
// backend record is explicitly supplied.
func newWorkbenchCapabilityService(
	db *gorm.DB,
	sessionRepo repository.WorkbenchSessionRepository,
	controlRepo repository.WorkbenchControlRepository,
	artifactRepo repository.WorkbenchArtifactRepository,
	skillRepo repository.WorkbenchSkillRunRepository,
) *service.WorkbenchCapabilityService {
	probes := []service.WorkbenchSchemaProbe{sessionRepo, controlRepo, artifactRepo, skillRepo}
	return service.NewWorkbenchCapabilityServiceWithSchemaProbes(db, sessionRepo, probes, nil)
}

func newWorkbenchRunnerBundle(control repository.WorkbenchControlRepository, artifacts repository.WorkbenchArtifactRepository, skillRuns repository.WorkbenchSkillRunRepository) workbenchRunnerBundle {
	if control == nil || !sandbox.WorkbenchEnabled() || !sandbox.DockerBackendEnabled() {
		return workbenchRunnerBundle{}
	}
	cfg := buildGlobalSandboxConfig()
	cfg.Type = sandbox.SandboxTypeDocker
	client, err := sandbox.NewDockerRemoteClient(cfg)
	if err != nil {
		logger.Warnf(context.Background(), "Workbench docker runner disabled: %v", err)
		return workbenchRunnerBundle{}
	}
	runner := workbenchrunner.NewProtectedDockerRunner(workbenchrunner.Config{
		Enabled:               true,
		TemplateID:            cfg.DockerImage,
		IdleTimeout:           cfg.DockerIdleTTL,
		DefaultCommandTimeout: cfg.DefaultTimeout,
	}, client, control)
	files := service.NewWorkbenchFileService(control, client)
	artifactSvc := service.NewWorkbenchArtifactService(artifacts, files, control)
	return workbenchRunnerBundle{runner: runner, files: files, artifacts: artifactSvc, presentationSkills: service.NewWorkbenchPresentationSkillService(skillRuns, control, artifactSvc)}
}
