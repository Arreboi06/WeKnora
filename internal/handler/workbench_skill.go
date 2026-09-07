package handler

import (
	"context"
	"net/http"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

type workbenchPresentationSkillManager interface {
	PublishCandidate(ctx context.Context, input service.PublishWorkbenchPresentationCandidateInput) (*types.WorkbenchSkillRun, *types.WorkbenchArtifactVersion, error)
}

type publishPresentationSkillRequest struct {
	workbenchFileScopeRequest
	SourceRef  types.WorkbenchFileRef `json:"source_ref"`
	ArtifactID string                 `json:"artifact_id"`
	Version    int                    `json:"version"`
	CommandID  string                 `json:"command_id"`
}

func (h *WorkbenchHandler) PublishPresentationSkillCandidate(c *gin.Context) {
	ctx, tenantID, actorID, session, ok := h.authorizeWorkbench(c)
	if !ok {
		return
	}
	if !h.requireWorkbenchPresentationSkills(c) {
		return
	}
	var req publishPresentationSkillRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeWorkbenchError(c, http.StatusBadRequest, workbenchErrorMalformedRequest, err.Error())
		return
	}
	run, artifact, err := h.presentationSkills.PublishCandidate(ctx, service.PublishWorkbenchPresentationCandidateInput{
		Scope: req.scope(tenantID, session.ID), SourceRef: req.SourceRef, ArtifactID: req.ArtifactID, Version: req.Version,
		CommandID: req.CommandID, ActorID: actorID,
	})
	if err != nil {
		h.writeMappedError(c, err)
		return
	}
	writeWorkbenchData(c, http.StatusCreated, workbenchPresentationSkillResponse(run, artifact))
}

func (h *WorkbenchHandler) requireWorkbenchPresentationSkills(c *gin.Context) bool {
	if h.presentationSkills == nil {
		writeWorkbenchError(c, http.StatusServiceUnavailable, workbenchErrorRunnerUnavailable, "workbench presentation skill service is unavailable")
		return false
	}
	return true
}

func workbenchPresentationSkillResponse(run *types.WorkbenchSkillRun, artifact *types.WorkbenchArtifactVersion) gin.H {
	if run == nil {
		return gin.H{}
	}
	return gin.H{
		"skill_run_id":            run.ID,
		"skill_name":              run.SkillName,
		"skill_operation":         run.SkillOperation,
		"state":                   run.State,
		"state_version":           run.StateVersion,
		"chat_session_id":         run.ChatSessionID,
		"workbench_id":            run.WorkbenchSessionID,
		"job_id":                  run.WorkbenchJobID,
		"command_id":              run.CommandID,
		"output_file_ref":         run.OutputFileRef,
		"output_artifact_id":      run.OutputArtifactID,
		"output_artifact_version": run.OutputArtifactVersion,
		"created_by":              run.CreatedBy,
		"created_at":              run.CreatedAt,
		"closed_at":               run.ClosedAt,
		"artifact":                workbenchArtifactResponse(artifact),
	}
}
