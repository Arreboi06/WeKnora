package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L06PresentationSkillRoutePublishesArtifactChain(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	manager := &t2l06PresentationSkillManager{
		run:      &types.WorkbenchSkillRun{ID: "skill-run-1", TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", WorkbenchJobID: "job-1", CommandID: "cmd-1", SkillName: "presentations", SkillOperation: "create_presentation", State: types.WorkbenchSkillRunStateSucceeded, OutputArtifactID: "artifact-1", OutputArtifactVersion: 1, CreatedBy: "actor-1", CreatedAt: time.Unix(1, 0).UTC()},
		artifact: &types.WorkbenchArtifactVersion{ArtifactID: "artifact-1", Version: 1, TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", WorkbenchJobID: "job-1", CommandID: "cmd-1", SkillRunID: "skill-run-1", SourceFileRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"slides.pptx"}}, ContentSHA256: "abc", SizeBytes: 41, MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", PreviewClass: types.WorkbenchPreviewClassPresentationPage, FileName: "slides.pptx", CreatedBy: "actor-1", CreatedAt: time.Unix(1, 0).UTC()},
	}
	h := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute), manager)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/skill-runs/presentation", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7, "command_id": "cmd-1",
		"source_ref": map[string]any{"file_ref_version": 1, "root": "output", "segments": []string{"slides.pptx"}},
	}))
	require.Equal(t, http.StatusCreated, resp.Code, resp.Body.String())
	data := t2l03ResponseData(t, resp)
	require.Equal(t, "skill-run-1", data["skill_run_id"])
	require.Equal(t, "presentations", data["skill_name"])
	artifact, ok := data["artifact"].(map[string]any)
	require.True(t, ok, "body=%s", resp.Body.String())
	require.Equal(t, "skill-run-1", artifact["skill_run_id"])
	require.Equal(t, "presentation_page", artifact["preview_class"])
	require.NotContains(t, resp.Body.String(), "container")
	require.Equal(t, service.WorkbenchFileScope{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7}, manager.inputs[0].Scope)
}

func TestT2L06PresentationSkillRouteFailsClosed(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}

	withoutManager := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute))
	rNoManager := t2l03WorkbenchRouter(withoutManager, 10, "actor-1")
	missing := httptest.NewRecorder()
	rNoManager.ServeHTTP(missing, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/skill-runs/presentation", map[string]any{}))
	require.Equal(t, http.StatusServiceUnavailable, missing.Code)
	require.Equal(t, "runner_unavailable", t2l03ResponseCode(t, missing))

	manager := &t2l06PresentationSkillManager{err: repository.ErrWorkbenchCommandNotFound}
	h := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute), manager)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")
	notFound := httptest.NewRecorder()
	r.ServeHTTP(notFound, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/skill-runs/presentation", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7, "command_id": "cmd-other",
		"source_ref": map[string]any{"file_ref_version": 1, "root": "output", "segments": []string{"slides.pptx"}},
	}))
	require.Equal(t, http.StatusNotFound, notFound.Code)
	require.Equal(t, "workbench_not_found", t2l03ResponseCode(t, notFound))
}

type t2l06PresentationSkillManager struct {
	err      error
	run      *types.WorkbenchSkillRun
	artifact *types.WorkbenchArtifactVersion
	inputs   []service.PublishWorkbenchPresentationCandidateInput
}

func (m *t2l06PresentationSkillManager) PublishCandidate(_ context.Context, input service.PublishWorkbenchPresentationCandidateInput) (*types.WorkbenchSkillRun, *types.WorkbenchArtifactVersion, error) {
	m.inputs = append(m.inputs, input)
	if m.err != nil {
		return nil, nil, m.err
	}
	run := *m.run
	artifact := *m.artifact
	return &run, &artifact, nil
}
