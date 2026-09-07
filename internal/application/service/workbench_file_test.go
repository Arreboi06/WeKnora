package service

import (
	"context"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L04WorkbenchFileServiceFiveOpsUseServerDerivedFileRefs(t *testing.T) {
	ctx := context.Background()
	control := &t2l04FileControl{job: t2l04RunningJob()}
	remote := newT2L04Remote()
	svc := NewWorkbenchFileService(control, remote)
	scope := t2l04Scope()

	uploaded, err := svc.Upload(ctx, scope, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"notes", "a.txt"}}, []byte("hello"))
	require.NoError(t, err)
	require.Equal(t, []string{"/workspace/notes/a.txt"}, remote.writePaths)
	require.Equal(t, []string{"container-1"}, remote.connectIDs)
	require.Equal(t, []sandbox.RemoteConnectRequest{{SandboxID: "container-1"}}, remote.connectReqs)
	require.Equal(t, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"notes", "a.txt"}}, uploaded.Ref)
	require.Equal(t, int64(5), uploaded.SizeBytes)

	entries, err := svc.Browse(ctx, scope, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"notes"}})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "a.txt", entries[0].Name)
	require.Equal(t, []string{"notes", "a.txt"}, entries[0].Ref.Segments)
	require.NotContains(t, entries[0].Ref.Segments, "container-1")

	content, downloaded, err := svc.Download(ctx, scope, uploaded.Ref)
	require.NoError(t, err)
	require.Equal(t, "hello", string(content))
	require.Equal(t, uploaded.Ref, downloaded.Ref)

	renamed, err := svc.Rename(ctx, scope, uploaded.Ref, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"notes", "b.txt"}})
	require.NoError(t, err)
	require.Equal(t, []string{"/workspace/notes/a.txt->/workspace/notes/b.txt"}, remote.renamePaths)
	require.Equal(t, []string{"notes", "b.txt"}, renamed.Ref.Segments)

	require.NoError(t, svc.Delete(ctx, scope, renamed.Ref))
	require.Equal(t, []string{"/workspace/notes/b.txt"}, remote.removePaths)
	require.NotContains(t, strings.Join(control.calls, "\n"), "tenant=0")
}

func TestT2L04WorkbenchFileServiceRejectsUntrustedRefsAndWrongScope(t *testing.T) {
	ctx := context.Background()
	svc := NewWorkbenchFileService(&t2l04FileControl{job: t2l04RunningJob()}, newT2L04Remote())

	badRefs := []types.WorkbenchFileRef{
		{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"..", "secret.txt"}},
		{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"C:\\Users\\x"}},
		{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"https://example.com/a.txt"}},
		{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"CON"}},
	}
	for _, ref := range badRefs {
		_, _, err := svc.Download(ctx, t2l04Scope(), ref)
		require.ErrorIs(t, err, ErrWorkbenchFilePathDenied, "ref=%#v", ref)
	}

	_, _, err := svc.Download(ctx, t2l04Scope(), types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootArtifact, Segments: []string{"report.pdf"}, ArtifactID: "artifact-1", Version: 1})
	require.ErrorIs(t, err, ErrWorkbenchFileUnsupported)

	wrongSession := t2l04Scope()
	wrongSession.ChatSessionID = "chat-other"
	_, _, err = svc.Download(ctx, wrongSession, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"ok.txt"}})
	require.ErrorIs(t, err, ErrWorkbenchFileNotFound)

	stale := t2l04Scope()
	stale.ExpectedLeaseEpoch = 99
	_, _, err = svc.Download(ctx, stale, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"ok.txt"}})
	require.ErrorIs(t, err, repository.ErrWorkbenchStaleEpoch)
}

func TestT2L04WorkbenchFileServiceDeniesSymlinkAndQuota(t *testing.T) {
	ctx := context.Background()
	remote := newT2L04Remote()
	remote.other["/workspace/link"] = true
	svc := NewWorkbenchFileService(&t2l04FileControl{job: t2l04RunningJob()}, remote)
	svc.SetMaxUploadBytes(4)

	_, err := svc.Upload(ctx, t2l04Scope(), types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"too-big.txt"}}, []byte("12345"))
	require.ErrorIs(t, err, ErrWorkbenchFileQuotaExceeded)

	_, _, err = svc.Download(ctx, t2l04Scope(), types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"link"}})
	require.ErrorIs(t, err, ErrWorkbenchFilePathDenied)

	_, err = svc.Upload(ctx, t2l04Scope(), types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"link"}}, []byte("x"))
	require.ErrorIs(t, err, ErrWorkbenchFilePathDenied)

	remote.files["/workspace/link/secret.txt"] = []byte("secret")
	_, _, err = svc.Download(ctx, t2l04Scope(), types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"link", "secret.txt"}})
	require.ErrorIs(t, err, ErrWorkbenchFilePathDenied)
}

func TestT2L04WorkbenchFileServiceDeniesOutputMutationAliases(t *testing.T) {
	ctx := context.Background()
	remote := newT2L04Remote()
	remote.files["/workspace/input/source.txt"] = []byte("source")
	remote.files["/workspace/output/existing.txt"] = []byte("out")
	svc := NewWorkbenchFileService(&t2l04FileControl{job: t2l04RunningJob()}, remote)
	scope := t2l04Scope()

	_, err := svc.Upload(ctx, scope, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"output", "new.txt"}}, []byte("x"))
	require.ErrorIs(t, err, ErrWorkbenchFilePathDenied)

	_, err = svc.Rename(ctx, scope,
		types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootInput, Segments: []string{"source.txt"}},
		types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"output", "renamed.txt"}},
	)
	require.ErrorIs(t, err, ErrWorkbenchFilePathDenied)

	err = svc.Delete(ctx, scope, types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"output", "existing.txt"}})
	require.ErrorIs(t, err, ErrWorkbenchFilePathDenied)
}
func t2l04Scope() WorkbenchFileScope {
	return WorkbenchFileScope{TenantID: 10, ChatSessionID: "chat-1", WorkbenchSessionID: "wb-1", JobID: "job-1", ExpectedLeaseEpoch: 7}
}

func t2l04RunningJob() *types.WorkbenchJob {
	return &types.WorkbenchJob{
		ID: "job-1", TenantID: 10, WorkbenchSessionID: "wb-1", ChatSessionID: "chat-1",
		LeaseEpoch: 7, BackendType: "docker", BackendIdentity: "container-1", State: types.WorkbenchJobStateRunning, StateVersion: 3,
	}
}

type t2l04FileControl struct {
	job   *types.WorkbenchJob
	err   error
	calls []string
}

func (c *t2l04FileControl) GetJobByID(_ context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error) {
	c.calls = append(c.calls, strings.TrimSpace("tenant="+strconvUint64(tenantID)+" job="+id))
	if c.err != nil {
		return nil, c.err
	}
	if c.job == nil || c.job.ID != id || c.job.TenantID != tenantID {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	copy := *c.job
	return &copy, nil
}

func (c *t2l04FileControl) GetActiveJobForSession(ctx context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error) {
	job, err := c.GetJobByID(ctx, tenantID, jobID)
	if err != nil {
		return nil, err
	}
	if job.ChatSessionID != chatSessionID || job.WorkbenchSessionID != workbenchSessionID {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	if job.LeaseEpoch != expectedLeaseEpoch {
		return nil, repository.ErrWorkbenchStaleEpoch
	}
	if job.State != types.WorkbenchJobStateRunning || job.ClosedAt != nil {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	return job, nil
}

type t2l04Remote struct {
	files       map[string][]byte
	dirs        map[string]bool
	other       map[string]bool
	connectIDs  []string
	connectReqs []sandbox.RemoteConnectRequest
	writePaths  []string
	removePaths []string
	renamePaths []string
}

func newT2L04Remote() *t2l04Remote {
	return &t2l04Remote{
		files: map[string][]byte{"/workspace/ok.txt": []byte("ok")},
		dirs: map[string]bool{
			"/workspace": true, "/workspace/input": true, "/workspace/output": true,
		},
		other: map[string]bool{},
	}
}

func (r *t2l04Remote) Connect(_ context.Context, request sandbox.RemoteConnectRequest) (sandbox.RemoteSandboxHandle, error) {
	r.connectReqs = append(r.connectReqs, request)
	r.connectIDs = append(r.connectIDs, request.SandboxID)
	return t2l04Handle{id: request.SandboxID}, nil
}

func (r *t2l04Remote) WriteFile(_ context.Context, _ sandbox.RemoteSandboxHandle, filePath string, content []byte) error {
	r.writePaths = append(r.writePaths, filePath)
	r.files[filePath] = append([]byte(nil), content...)
	r.dirs[path.Dir(filePath)] = true
	return nil
}

func (r *t2l04Remote) ReadFile(_ context.Context, _ sandbox.RemoteSandboxHandle, filePath string) ([]byte, error) {
	content, ok := r.files[filePath]
	if !ok {
		return nil, sandbox.NewRemoteError(sandbox.SandboxTypeDocker, "ReadFile", sandbox.RemoteErrorKindNotFound, "missing", nil)
	}
	return append([]byte(nil), content...), nil
}

func (r *t2l04Remote) ListDir(_ context.Context, _ sandbox.RemoteSandboxHandle, dirPath string) ([]sandbox.RemoteDirEntry, error) {
	if !r.dirs[dirPath] {
		return nil, sandbox.NewRemoteError(sandbox.SandboxTypeDocker, "ListDir", sandbox.RemoteErrorKindNotFound, "missing", nil)
	}
	var entries []sandbox.RemoteDirEntry
	prefix := strings.TrimRight(dirPath, "/") + "/"
	for p, content := range r.files {
		if strings.HasPrefix(p, prefix) && !strings.Contains(strings.TrimPrefix(p, prefix), "/") {
			entries = append(entries, sandbox.RemoteDirEntry{Name: path.Base(p), Path: p, Type: sandbox.RemoteEntryFile, Size: int64(len(content)), ModTime: time.Unix(1, 0).UTC()})
		}
	}
	for p := range r.dirs {
		if p != dirPath && strings.HasPrefix(p, prefix) && !strings.Contains(strings.TrimPrefix(p, prefix), "/") {
			entries = append(entries, sandbox.RemoteDirEntry{Name: path.Base(p), Path: p, Type: sandbox.RemoteEntryDir, ModTime: time.Unix(1, 0).UTC()})
		}
	}
	return entries, nil
}

func (r *t2l04Remote) Remove(_ context.Context, _ sandbox.RemoteSandboxHandle, targetPath string) error {
	r.removePaths = append(r.removePaths, targetPath)
	delete(r.files, targetPath)
	delete(r.dirs, targetPath)
	return nil
}

func (r *t2l04Remote) Stat(_ context.Context, _ sandbox.RemoteSandboxHandle, targetPath string) (*sandbox.RemoteStatEntry, error) {
	if r.other[targetPath] {
		return &sandbox.RemoteStatEntry{Path: targetPath, Type: sandbox.RemoteEntryOther, ModTime: time.Unix(1, 0).UTC()}, nil
	}
	if content, ok := r.files[targetPath]; ok {
		return &sandbox.RemoteStatEntry{Path: targetPath, Type: sandbox.RemoteEntryFile, Size: int64(len(content)), ModTime: time.Unix(1, 0).UTC()}, nil
	}
	if r.dirs[targetPath] {
		return &sandbox.RemoteStatEntry{Path: targetPath, Type: sandbox.RemoteEntryDir, ModTime: time.Unix(1, 0).UTC()}, nil
	}
	return nil, sandbox.NewRemoteError(sandbox.SandboxTypeDocker, "Stat", sandbox.RemoteErrorKindNotFound, "missing", nil)
}

func (r *t2l04Remote) Exec(_ context.Context, _ sandbox.RemoteSandboxHandle, req sandbox.RemoteExecRequest) (*sandbox.RemoteExecResult, error) {
	if len(req.Args) < 2 {
		return &sandbox.RemoteExecResult{ExitCode: 2, Stderr: "bad mv"}, nil
	}
	src := req.Args[len(req.Args)-2]
	dst := req.Args[len(req.Args)-1]
	content, ok := r.files[src]
	if !ok {
		return &sandbox.RemoteExecResult{ExitCode: 1, Stderr: "missing"}, nil
	}
	delete(r.files, src)
	r.files[dst] = content
	r.renamePaths = append(r.renamePaths, src+"->"+dst)
	return &sandbox.RemoteExecResult{ExitCode: 0}, nil
}

type t2l04Handle struct{ id string }

func (h t2l04Handle) ID() string                       { return h.id }
func (h t2l04Handle) Provider() sandbox.RemoteProvider { return sandbox.SandboxTypeDocker }
func (h t2l04Handle) Metadata() map[string]string      { return nil }
func strconvUint64(v uint64) string                    { return strconv.FormatUint(v, 10) }
