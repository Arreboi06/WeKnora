package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L05WorkbenchArtifactRoutesPublishAndPreviewWithIsolation(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	artifacts := &t2l05ArtifactManager{
		published: &types.WorkbenchArtifactVersion{ArtifactID: "artifact-1", Version: 1, TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", WorkbenchJobID: "job-1", SourceFileRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"index.html"}}, ContentSHA256: "abc", SizeBytes: 41, MimeType: "text/html; charset=utf-8", PreviewClass: types.WorkbenchPreviewClassHTMLActive, FileName: "index.html", CreatedBy: "actor-1", CreatedAt: time.Unix(1, 0).UTC()},
		preview:   service.WorkbenchArtifactPreview{ContentType: "text/html; charset=utf-8", Content: []byte("<html><script>document.cookie</script></html>")},
	}
	control := &t2l03ControlService{job: &types.WorkbenchJob{ID: "job-1", TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", IncarnationID: "inc-1", LeaseEpoch: 7}}
	h := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{active: &types.WorkbenchSession{ID: "wb-1", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-1", State: types.WorkbenchStateReady, LeaseEpoch: 7}}, control, nil, NewWorkbenchStreamTicketStore(time.Minute), artifacts)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	publish := httptest.NewRecorder()
	r.ServeHTTP(publish, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/artifacts", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7, "command_id": "cmd-1",
		"source_ref": map[string]any{"file_ref_version": 1, "root": "output", "segments": []string{"index.html"}},
	}))
	require.Equal(t, http.StatusCreated, publish.Code, publish.Body.String())
	publishData := t2l03ResponseData(t, publish)
	require.Equal(t, "artifact-1", publishData["artifact_id"])
	require.Equal(t, "html_active", publishData["preview_class"])
	require.NotContains(t, publish.Body.String(), "container")
	require.Equal(t, service.WorkbenchFileScope{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7}, artifacts.publishInputs[0].Scope)

	preview := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/artifacts/artifact-1/versions/1/preview", nil)
	r.ServeHTTP(preview, req)
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Equal(t, "text/html; charset=utf-8", preview.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", preview.Header().Get("X-Content-Type-Options"))
	require.Empty(t, preview.Header().Values("Set-Cookie"))
	csp := preview.Header().Get("Content-Security-Policy")
	require.Contains(t, csp, "sandbox allow-scripts")
	require.Contains(t, csp, "connect-src 'none'")
	require.Contains(t, csp, "object-src 'none'")
	require.Contains(t, csp, "frame-src 'none'")
	require.Contains(t, csp, "worker-src 'none'")
	require.Contains(t, csp, "manifest-src 'none'")
	require.Contains(t, csp, "form-action 'none'")
	require.Equal(t, "same-origin", preview.Header().Get("Cross-Origin-Opener-Policy"))
	require.Equal(t, "same-site", preview.Header().Get("Cross-Origin-Resource-Policy"))
	require.Contains(t, preview.Body.String(), "document.cookie")
}

func TestT2L09WorkbenchArtifactReadRejectsReplacedIncarnation(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{
		ID: "wb-current", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-current",
		State: types.WorkbenchStateReady, LeaseEpoch: 8,
	}}
	artifacts := &t2l05ArtifactManager{published: &types.WorkbenchArtifactVersion{
		ArtifactID: "artifact-old", Version: 1, TenantID: 10, ChatSessionID: "chat-1",
		WorkbenchSessionID: "wb-old", WorkbenchJobID: "job-old", MimeType: "text/plain",
		PreviewClass: types.WorkbenchPreviewClassDownloadOnly, FileName: "old.txt",
	}}
	h := newWorkbenchHandler(sessions, workbenches, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute), artifacts)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/artifacts/artifact-old/versions/1", nil))

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Equal(t, "workbench_not_found", t2l03ResponseCode(t, w))
}
func TestT2L10WorkbenchArtifactReadRequiresCurrentJobLineage(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	workbenches := &t2l03WorkbenchSessions{active: &types.WorkbenchSession{
		ID: "wb-current", TenantID: 10, ChatSessionID: "chat-1", IncarnationID: "inc-current", State: types.WorkbenchStateReady, LeaseEpoch: 8,
	}}
	artifacts := &t2l05ArtifactManager{published: &types.WorkbenchArtifactVersion{
		ArtifactID: "artifact-current", Version: 1, TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-current", WorkbenchJobID: "job-old",
		MimeType: "text/plain", PreviewClass: types.WorkbenchPreviewClassDownloadOnly, FileName: "current.txt",
	}}
	control := &t2l03ControlService{job: &types.WorkbenchJob{
		ID: "job-old", TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-current", IncarnationID: "inc-old", LeaseEpoch: 7,
	}}
	h := newWorkbenchHandler(sessions, workbenches, control, nil, NewWorkbenchStreamTicketStore(time.Minute), artifacts)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/artifacts/artifact-current/versions/1", nil))

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Equal(t, "workbench_not_found", t2l03ResponseCode(t, w))
}
func TestT2L05WorkbenchArtifactRoutesFailClosed(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}

	withoutArtifacts := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute))
	rNoArtifacts := t2l03WorkbenchRouter(withoutArtifacts, 10, "actor-1")
	missing := httptest.NewRecorder()
	rNoArtifacts.ServeHTTP(missing, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/artifacts", map[string]any{}))
	require.Equal(t, http.StatusServiceUnavailable, missing.Code)
	require.Equal(t, "runner_unavailable", t2l03ResponseCode(t, missing))

	artifacts := &t2l05ArtifactManager{err: service.ErrWorkbenchPreviewBlocked}
	h := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute), artifacts)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")
	blocked := httptest.NewRecorder()
	r.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/artifacts/artifact-1/versions/1/preview", nil))
	require.Equal(t, http.StatusForbidden, blocked.Code)
	require.Equal(t, "preview_blocked", t2l03ResponseCode(t, blocked))

	unsupportedDB := httptest.NewRecorder()
	artifacts.err = repository.ErrWorkbenchUnsupportedDatabase
	r.ServeHTTP(unsupportedDB, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/artifacts/artifact-1/versions/1/preview", nil))
	require.Equal(t, http.StatusNotImplemented, unsupportedDB.Code)
	require.Equal(t, "unsupported", t2l03ResponseCode(t, unsupportedDB))
	require.NotContains(t, unsupportedDB.Body.String(), "lost")

	missingMigration := httptest.NewRecorder()
	artifacts.err = repository.ErrWorkbenchMigrationUnavailable
	r.ServeHTTP(missingMigration, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/artifacts/artifact-1/versions/1/preview", nil))
	require.Equal(t, http.StatusServiceUnavailable, missingMigration.Code)
	require.Equal(t, "migration_unavailable", t2l03ResponseCode(t, missingMigration))
	require.NotContains(t, missingMigration.Body.String(), "lost")

	t.Setenv(sandbox.WorkbenchEnabledEnv, "")
	disabled := httptest.NewRecorder()
	r.ServeHTTP(disabled, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/chat-1/workbench/artifacts/artifact-1/versions/1/preview", nil))
	require.Equal(t, http.StatusNotFound, disabled.Code)
	require.Equal(t, "unsupported", t2l03ResponseCode(t, disabled))
}

type t2l05ArtifactManager struct {
	err           error
	published     *types.WorkbenchArtifactVersion
	preview       service.WorkbenchArtifactPreview
	publishInputs []service.PublishWorkbenchArtifactInput
}

func (m *t2l05ArtifactManager) Publish(_ context.Context, input service.PublishWorkbenchArtifactInput) (*types.WorkbenchArtifactVersion, error) {
	m.publishInputs = append(m.publishInputs, input)
	if m.err != nil {
		return nil, m.err
	}
	copy := *m.published
	return &copy, nil
}
func (m *t2l05ArtifactManager) Get(_ context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error) {
	if m.err != nil {
		return nil, m.err
	}
	copy := *m.published
	copy.TenantID = tenantID
	copy.ChatSessionID = chatSessionID
	copy.ArtifactID = artifactID
	copy.Version = version
	return &copy, nil
}
func (m *t2l05ArtifactManager) Download(_ context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*service.WorkbenchArtifactDownload, error) {
	if m.err != nil {
		return nil, m.err
	}
	copy := *m.published
	return &service.WorkbenchArtifactDownload{Artifact: &copy, ContentType: copy.MimeType, Content: []byte("download")}, nil
}
func (m *t2l05ArtifactManager) Preview(_ context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*service.WorkbenchArtifactPreview, error) {
	if m.err != nil {
		return nil, m.err
	}
	preview := m.preview
	copy := *m.published
	copy.TenantID = tenantID
	copy.ChatSessionID = chatSessionID
	copy.ArtifactID = artifactID
	copy.Version = version
	preview.Artifact = &copy
	return &preview, nil
}
