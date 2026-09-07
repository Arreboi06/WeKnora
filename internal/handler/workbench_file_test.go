package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L04WorkbenchFileRoutesCallManagerWithServerScope(t *testing.T) {
	t2l03EnableWorkbench(t)

	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}
	files := &t2l04FileManager{
		browseEntries: []types.WorkbenchFileEntry{{Name: "a.txt", Type: "file", SizeBytes: 2, Ref: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"a.txt"}}}},
		uploadEntry:   types.WorkbenchFileEntry{Name: "a.txt", Type: "file", SizeBytes: 2, Ref: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"a.txt"}}},
		downloadEntry: types.WorkbenchFileEntry{Name: "a.txt", Type: "file", SizeBytes: 2, Ref: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"a.txt"}}},
		downloadBytes: []byte("hi"),
		renameEntry:   types.WorkbenchFileEntry{Name: "b.txt", Type: "file", SizeBytes: 2, Ref: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"b.txt"}}},
	}
	h := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute), files)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")
	body := map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7,
		"ref": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"a.txt"}},
	}

	browse := httptest.NewRecorder()
	r.ServeHTTP(browse, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/browse", body))
	require.Equal(t, http.StatusOK, browse.Code, browse.Body.String())
	require.Len(t, t2l03ResponseData(t, browse)["entries"], 1)

	upload := httptest.NewRecorder()
	uploadBody := cloneT2L04Map(body)
	uploadBody["content_b64"] = "aGk="
	r.ServeHTTP(upload, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/upload", uploadBody))
	require.Equal(t, http.StatusCreated, upload.Code, upload.Body.String())

	download := httptest.NewRecorder()
	r.ServeHTTP(download, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/download", body))
	require.Equal(t, http.StatusOK, download.Code, download.Body.String())
	require.Equal(t, "aGk=", t2l03ResponseData(t, download)["content_b64"])

	rename := httptest.NewRecorder()
	renameBody := map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7,
		"source": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"a.txt"}},
		"target": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"b.txt"}},
	}
	r.ServeHTTP(rename, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/rename", renameBody))
	require.Equal(t, http.StatusOK, rename.Code, rename.Body.String())

	deleteReq := httptest.NewRecorder()
	r.ServeHTTP(deleteReq, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/delete", body))
	require.Equal(t, http.StatusOK, deleteReq.Code, deleteReq.Body.String())

	require.Equal(t, []service.WorkbenchFileScope{
		{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7},
		{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7},
		{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7},
		{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7},
		{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7},
	}, files.scopes)
	require.Equal(t, []byte("hi"), files.uploadBytes[0])
	require.NotContains(t, browse.Body.String(), "container-1")
}

func TestT2L04WorkbenchFileRoutesFailClosed(t *testing.T) {
	t2l03EnableWorkbench(t)
	sessions := newT2L03SessionAuthorizer()
	sessions.rows["10/chat-1"] = &types.Session{ID: "chat-1", TenantID: 10, UserID: "actor-1"}

	withoutFiles := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute))
	rNoFiles := t2l03WorkbenchRouter(withoutFiles, 10, "actor-1")
	missing := httptest.NewRecorder()
	rNoFiles.ServeHTTP(missing, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/browse", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7,
		"ref": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{}},
	}))
	require.Equal(t, http.StatusServiceUnavailable, missing.Code)
	require.Equal(t, "runner_unavailable", t2l03ResponseCode(t, missing))

	files := &t2l04FileManager{err: service.ErrWorkbenchFilePathDenied}
	h := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute), files)
	r := t2l03WorkbenchRouter(h, 10, "actor-1")
	denied := httptest.NewRecorder()
	r.ServeHTTP(denied, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/download", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7,
		"ref": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{".."}},
	}))
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.Equal(t, "path_denied", t2l03ResponseCode(t, denied))

	badUpload := httptest.NewRecorder()
	r.ServeHTTP(badUpload, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/upload", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7, "content_b64": "not-base64",
		"ref": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"a.txt"}},
	}))
	require.Equal(t, http.StatusBadRequest, badUpload.Code)
	require.Equal(t, "malformed_request", t2l03ResponseCode(t, badUpload))

	quotaFiles := &t2l04FileManager{err: service.ErrWorkbenchFileQuotaExceeded}
	quotaHandler := newWorkbenchHandler(sessions, &t2l03WorkbenchSessions{}, &t2l03ControlService{}, nil, NewWorkbenchStreamTicketStore(time.Minute), quotaFiles)
	quotaRouter := t2l03WorkbenchRouter(quotaHandler, 10, "actor-1")
	quota := httptest.NewRecorder()
	quotaRouter.ServeHTTP(quota, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/upload", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7, "content_b64": "aGk=",
		"ref": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{"a.txt"}},
	}))
	require.Equal(t, http.StatusRequestEntityTooLarge, quota.Code)
	require.Equal(t, "quota_exceeded", t2l03ResponseCode(t, quota))

	t.Setenv(sandbox.WorkbenchEnabledEnv, "")
	disabled := httptest.NewRecorder()
	quotaRouter.ServeHTTP(disabled, t2l03JSONRequest(http.MethodPost, "/api/v1/sessions/chat-1/workbench/files/browse", map[string]any{
		"workbench_id": "wb-1", "job_id": "job-1", "expected_lease_epoch": 7,
		"ref": map[string]any{"file_ref_version": 1, "root": "workspace", "segments": []string{}},
	}))
	require.Equal(t, http.StatusNotFound, disabled.Code)
	require.Equal(t, "unsupported", t2l03ResponseCode(t, disabled))
}

func cloneT2L04Map(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type t2l04FileManager struct {
	err           error
	browseEntries []types.WorkbenchFileEntry
	uploadEntry   types.WorkbenchFileEntry
	downloadEntry types.WorkbenchFileEntry
	downloadBytes []byte
	renameEntry   types.WorkbenchFileEntry
	scopes        []service.WorkbenchFileScope
	uploadBytes   [][]byte
}

func (f *t2l04FileManager) Browse(_ context.Context, scope service.WorkbenchFileScope, _ types.WorkbenchFileRef) ([]types.WorkbenchFileEntry, error) {
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return nil, f.err
	}
	return f.browseEntries, nil
}

func (f *t2l04FileManager) Upload(_ context.Context, scope service.WorkbenchFileScope, _ types.WorkbenchFileRef, content []byte) (types.WorkbenchFileEntry, error) {
	f.scopes = append(f.scopes, scope)
	f.uploadBytes = append(f.uploadBytes, append([]byte(nil), content...))
	if f.err != nil {
		return types.WorkbenchFileEntry{}, f.err
	}
	return f.uploadEntry, nil
}

func (f *t2l04FileManager) Download(_ context.Context, scope service.WorkbenchFileScope, _ types.WorkbenchFileRef) ([]byte, types.WorkbenchFileEntry, error) {
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return nil, types.WorkbenchFileEntry{}, f.err
	}
	return append([]byte(nil), f.downloadBytes...), f.downloadEntry, nil
}

func (f *t2l04FileManager) Rename(_ context.Context, scope service.WorkbenchFileScope, _, _ types.WorkbenchFileRef) (types.WorkbenchFileEntry, error) {
	f.scopes = append(f.scopes, scope)
	if f.err != nil {
		return types.WorkbenchFileEntry{}, f.err
	}
	return f.renameEntry, nil
}

func (f *t2l04FileManager) Delete(_ context.Context, scope service.WorkbenchFileScope, _ types.WorkbenchFileRef) error {
	f.scopes = append(f.scopes, scope)
	return f.err
}
