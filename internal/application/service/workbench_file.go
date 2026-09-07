package service

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

var (
	ErrWorkbenchFileInvalidArgument = errors.New("workbench file invalid argument")
	ErrWorkbenchFileNotFound        = errors.New("workbench file not found")
	ErrWorkbenchFilePathDenied      = errors.New("workbench file path denied")
	ErrWorkbenchFileQuotaExceeded   = errors.New("workbench file quota exceeded")
	ErrWorkbenchFileUnsupported     = errors.New("workbench file unsupported")
)

const workbenchFileRenameTimeout = 30 * time.Second

type WorkbenchFileControl interface {
	GetJobByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error)
	GetActiveJobForSession(ctx context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error)
}

type WorkbenchFileClient interface {
	Connect(ctx context.Context, request sandbox.RemoteConnectRequest) (sandbox.RemoteSandboxHandle, error)
	WriteFile(ctx context.Context, handle sandbox.RemoteSandboxHandle, path string, content []byte) error
	ReadFile(ctx context.Context, handle sandbox.RemoteSandboxHandle, path string) ([]byte, error)
	ListDir(ctx context.Context, handle sandbox.RemoteSandboxHandle, path string) ([]sandbox.RemoteDirEntry, error)
	Remove(ctx context.Context, handle sandbox.RemoteSandboxHandle, path string) error
	Stat(ctx context.Context, handle sandbox.RemoteSandboxHandle, path string) (*sandbox.RemoteStatEntry, error)
	Exec(ctx context.Context, handle sandbox.RemoteSandboxHandle, req sandbox.RemoteExecRequest) (*sandbox.RemoteExecResult, error)
}

type WorkbenchFileScope struct {
	TenantID           uint64
	ChatSessionID      string
	WorkbenchSessionID string
	JobID              string
	ExpectedLeaseEpoch int64
}

type WorkbenchFileService struct {
	control        WorkbenchFileControl
	client         WorkbenchFileClient
	maxUploadBytes int64
}

func NewWorkbenchFileService(control WorkbenchFileControl, client WorkbenchFileClient) *WorkbenchFileService {
	return &WorkbenchFileService{control: control, client: client, maxUploadBytes: 100 << 20}
}

func (s *WorkbenchFileService) SetMaxUploadBytes(max int64) {
	if max > 0 {
		s.maxUploadBytes = max
	}
}

func (s *WorkbenchFileService) Browse(ctx context.Context, scope WorkbenchFileScope, ref types.WorkbenchFileRef) ([]types.WorkbenchFileEntry, error) {
	handle, clean, normalized, err := s.resolve(ctx, scope, ref, false)
	if err != nil {
		return nil, err
	}
	stat, err := s.safeStat(ctx, handle, clean, false)
	if err != nil {
		return nil, err
	}
	if stat.Type != sandbox.RemoteEntryDir {
		return nil, ErrWorkbenchFilePathDenied
	}
	entries, err := s.client.ListDir(ctx, handle, clean)
	if err != nil {
		return nil, mapWorkbenchFileRemoteError(err)
	}
	out := make([]types.WorkbenchFileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Type == sandbox.RemoteEntryOther {
			return nil, ErrWorkbenchFilePathDenied
		}
		fileRef, err := remotePathToWorkbenchFileRef(normalized.Root, entry.Path)
		if err != nil {
			return nil, ErrWorkbenchFilePathDenied
		}
		out = append(out, types.WorkbenchFileEntry{
			Name:      entry.Name,
			Type:      string(entry.Type),
			SizeBytes: entry.Size,
			ModTime:   entry.ModTime,
			Ref:       fileRef,
		})
	}
	return out, nil
}

func (s *WorkbenchFileService) Upload(ctx context.Context, scope WorkbenchFileScope, ref types.WorkbenchFileRef, content []byte) (types.WorkbenchFileEntry, error) {
	if s.maxUploadBytes > 0 && int64(len(content)) > s.maxUploadBytes {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFileQuotaExceeded
	}
	handle, clean, normalized, err := s.resolve(ctx, scope, ref, true)
	if err != nil {
		return types.WorkbenchFileEntry{}, err
	}
	if len(normalized.Segments) == 0 {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	if err := s.ensureMutableRef(normalized); err != nil {
		return types.WorkbenchFileEntry{}, err
	}
	parent := path.Dir(clean)
	if stat, err := s.safeStat(ctx, handle, parent, true); err != nil {
		return types.WorkbenchFileEntry{}, err
	} else if stat != nil && stat.Type != sandbox.RemoteEntryDir {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	if stat, err := s.safeStat(ctx, handle, clean, true); err != nil {
		return types.WorkbenchFileEntry{}, err
	} else if stat != nil && stat.Type != sandbox.RemoteEntryFile {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	if err := s.client.WriteFile(ctx, handle, clean, content); err != nil {
		return types.WorkbenchFileEntry{}, mapWorkbenchFileRemoteError(err)
	}
	return types.WorkbenchFileEntry{
		Name:      path.Base(clean),
		Type:      string(sandbox.RemoteEntryFile),
		SizeBytes: int64(len(content)),
		Ref:       normalized,
	}, nil
}

func (s *WorkbenchFileService) Download(ctx context.Context, scope WorkbenchFileScope, ref types.WorkbenchFileRef) ([]byte, types.WorkbenchFileEntry, error) {
	handle, clean, normalized, err := s.resolve(ctx, scope, ref, false)
	if err != nil {
		return nil, types.WorkbenchFileEntry{}, err
	}
	stat, err := s.safeStat(ctx, handle, clean, false)
	if err != nil {
		return nil, types.WorkbenchFileEntry{}, err
	}
	if stat.Type != sandbox.RemoteEntryFile {
		return nil, types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	content, err := s.client.ReadFile(ctx, handle, clean)
	if err != nil {
		return nil, types.WorkbenchFileEntry{}, mapWorkbenchFileRemoteError(err)
	}
	return content, types.WorkbenchFileEntry{
		Name:      path.Base(clean),
		Type:      string(stat.Type),
		SizeBytes: stat.Size,
		ModTime:   stat.ModTime,
		Ref:       normalized,
	}, nil
}

func (s *WorkbenchFileService) Rename(ctx context.Context, scope WorkbenchFileScope, source, target types.WorkbenchFileRef) (types.WorkbenchFileEntry, error) {
	handle, sourcePath, sourceRef, err := s.resolve(ctx, scope, source, true)
	if err != nil {
		return types.WorkbenchFileEntry{}, err
	}
	_, targetPath, targetRef, err := s.resolve(ctx, scope, target, true)
	if err != nil {
		return types.WorkbenchFileEntry{}, err
	}
	if len(sourceRef.Segments) == 0 || len(targetRef.Segments) == 0 {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	if err := s.ensureMutableRef(sourceRef); err != nil {
		return types.WorkbenchFileEntry{}, err
	}
	if err := s.ensureMutableRef(targetRef); err != nil {
		return types.WorkbenchFileEntry{}, err
	}
	sourceStat, err := s.safeStat(ctx, handle, sourcePath, false)
	if err != nil {
		return types.WorkbenchFileEntry{}, err
	}
	if sourceStat.Type == sandbox.RemoteEntryOther {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	if targetStat, err := s.safeStat(ctx, handle, targetPath, true); err != nil {
		return types.WorkbenchFileEntry{}, err
	} else if targetStat != nil {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	if parentStat, err := s.safeStat(ctx, handle, path.Dir(targetPath), true); err != nil {
		return types.WorkbenchFileEntry{}, err
	} else if parentStat != nil && parentStat.Type != sandbox.RemoteEntryDir {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	result, err := s.client.Exec(ctx, handle, sandbox.RemoteExecRequest{
		Command: "mv",
		Args:    []string{"--", sourcePath, targetPath},
		User:    sandbox.DefaultSandboxExecUser,
		Timeout: workbenchFileRenameTimeout,
	})
	if err != nil {
		return types.WorkbenchFileEntry{}, mapWorkbenchFileRemoteError(err)
	}
	if result == nil || result.ExitCode != 0 {
		return types.WorkbenchFileEntry{}, ErrWorkbenchFilePathDenied
	}
	return types.WorkbenchFileEntry{
		Name:      path.Base(targetPath),
		Type:      string(sourceStat.Type),
		SizeBytes: sourceStat.Size,
		ModTime:   sourceStat.ModTime,
		Ref:       targetRef,
	}, nil
}

func (s *WorkbenchFileService) Delete(ctx context.Context, scope WorkbenchFileScope, ref types.WorkbenchFileRef) error {
	handle, clean, normalized, err := s.resolve(ctx, scope, ref, true)
	if err != nil {
		return err
	}
	if len(normalized.Segments) == 0 {
		return ErrWorkbenchFilePathDenied
	}
	if err := s.ensureMutableRef(normalized); err != nil {
		return err
	}
	stat, err := s.safeStat(ctx, handle, clean, false)
	if err != nil {
		return err
	}
	if stat.Type == sandbox.RemoteEntryOther {
		return ErrWorkbenchFilePathDenied
	}
	if err := s.client.Remove(ctx, handle, clean); err != nil {
		return mapWorkbenchFileRemoteError(err)
	}
	return nil
}

func (s *WorkbenchFileService) resolve(ctx context.Context, scope WorkbenchFileScope, ref types.WorkbenchFileRef, mutable bool) (sandbox.RemoteSandboxHandle, string, types.WorkbenchFileRef, error) {
	if s == nil || s.control == nil || s.client == nil || scope.TenantID == 0 || strings.TrimSpace(scope.JobID) == "" || strings.TrimSpace(scope.ChatSessionID) == "" || strings.TrimSpace(scope.WorkbenchSessionID) == "" {
		return nil, "", types.WorkbenchFileRef{}, ErrWorkbenchFileInvalidArgument
	}
	clean, normalized, err := cleanWorkbenchFileRef(ref)
	if err != nil {
		return nil, "", types.WorkbenchFileRef{}, err
	}
	if mutable {
		if err := s.ensureMutableRef(normalized); err != nil {
			return nil, "", types.WorkbenchFileRef{}, err
		}
	}
	job, err := s.control.GetActiveJobForSession(ctx, scope.TenantID, strings.TrimSpace(scope.ChatSessionID), strings.TrimSpace(scope.WorkbenchSessionID), strings.TrimSpace(scope.JobID), scope.ExpectedLeaseEpoch)
	if err != nil {
		if errors.Is(err, repository.ErrWorkbenchJobNotFound) {
			return nil, "", types.WorkbenchFileRef{}, ErrWorkbenchFileNotFound
		}
		return nil, "", types.WorkbenchFileRef{}, err
	}
	if job == nil || job.TenantID != scope.TenantID || job.ChatSessionID != strings.TrimSpace(scope.ChatSessionID) || job.WorkbenchSessionID != strings.TrimSpace(scope.WorkbenchSessionID) {
		return nil, "", types.WorkbenchFileRef{}, ErrWorkbenchFileNotFound
	}
	if job.State != types.WorkbenchJobStateRunning || job.ClosedAt != nil || strings.TrimSpace(job.BackendIdentity) == "" {
		return nil, "", types.WorkbenchFileRef{}, ErrWorkbenchFileUnsupported
	}
	handle, err := s.client.Connect(ctx, sandbox.RemoteConnectRequest{
		SandboxID: strings.TrimSpace(job.BackendIdentity),
	})
	if err != nil {
		return nil, "", types.WorkbenchFileRef{}, mapWorkbenchFileRemoteError(err)
	}
	return handle, clean, normalized, nil
}

func (s *WorkbenchFileService) safeStat(ctx context.Context, handle sandbox.RemoteSandboxHandle, targetPath string, allowMissing bool) (*sandbox.RemoteStatEntry, error) {
	clean := path.Clean(targetPath)
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(parts) == 0 || parts[0] != "workspace" {
		return nil, ErrWorkbenchFilePathDenied
	}

	current := ""
	var last *sandbox.RemoteStatEntry
	for i, part := range parts {
		if part == "" {
			continue
		}
		current += "/" + part
		stat, err := s.client.Stat(ctx, handle, current)
		if err != nil {
			if allowMissing && sandbox.IsRemoteNotFound(err) {
				return nil, nil
			}
			return nil, mapWorkbenchFileRemoteError(err)
		}
		if stat == nil {
			return nil, ErrWorkbenchFileNotFound
		}
		if stat.Type == sandbox.RemoteEntryOther {
			return nil, ErrWorkbenchFilePathDenied
		}
		if i < len(parts)-1 && stat.Type != sandbox.RemoteEntryDir {
			return nil, ErrWorkbenchFilePathDenied
		}
		last = stat
	}
	return last, nil
}
func (s *WorkbenchFileService) ensureMutableRef(ref types.WorkbenchFileRef) error {
	if err := s.ensureMutableRoot(ref.Root); err != nil {
		return err
	}
	if ref.Root == types.WorkbenchFileRootWorkspace && len(ref.Segments) > 0 && ref.Segments[0] == string(types.WorkbenchFileRootOutput) {
		return ErrWorkbenchFilePathDenied
	}
	return nil
}
func (s *WorkbenchFileService) ensureMutableRoot(root types.WorkbenchFileRoot) error {
	switch root {
	case types.WorkbenchFileRootWorkspace, types.WorkbenchFileRootInput:
		return nil
	case types.WorkbenchFileRootOutput:
		return ErrWorkbenchFilePathDenied
	case types.WorkbenchFileRootArtifact:
		return ErrWorkbenchFileUnsupported
	default:
		return ErrWorkbenchFilePathDenied
	}
}

func cleanWorkbenchFileRef(ref types.WorkbenchFileRef) (string, types.WorkbenchFileRef, error) {
	if ref.FileRefVersion != 1 {
		return "", types.WorkbenchFileRef{}, ErrWorkbenchFileInvalidArgument
	}
	base, err := workbenchFileRootPath(ref.Root)
	if err != nil {
		return "", types.WorkbenchFileRef{}, err
	}
	segments := make([]string, 0, len(ref.Segments))
	for _, segment := range ref.Segments {
		clean, err := cleanWorkbenchFileSegment(segment)
		if err != nil {
			return "", types.WorkbenchFileRef{}, err
		}
		segments = append(segments, clean)
	}
	cleanPath := base
	if len(segments) > 0 {
		cleanPath = path.Join(append([]string{base}, segments...)...)
	}
	if cleanPath != base && !strings.HasPrefix(cleanPath, strings.TrimRight(base, "/")+"/") {
		return "", types.WorkbenchFileRef{}, ErrWorkbenchFilePathDenied
	}
	return cleanPath, types.WorkbenchFileRef{FileRefVersion: 1, Root: ref.Root, Segments: segments, ArtifactID: strings.TrimSpace(ref.ArtifactID), Version: ref.Version}, nil
}

func cleanWorkbenchFileSegment(segment string) (string, error) {
	if segment == "" || strings.TrimSpace(segment) != segment || segment == "." || segment == ".." {
		return "", ErrWorkbenchFilePathDenied
	}
	if strings.ContainsAny(segment, "/\\:\x00") || strings.Contains(strings.ToLower(segment), "://") {
		return "", ErrWorkbenchFilePathDenied
	}
	if strings.TrimRight(segment, " .") != segment || len(segment) > 255 {
		return "", ErrWorkbenchFilePathDenied
	}
	name := strings.ToUpper(segment)
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		name = name[:dot]
	}
	switch name {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "", ErrWorkbenchFilePathDenied
	}
	return segment, nil
}

func workbenchFileRootPath(root types.WorkbenchFileRoot) (string, error) {
	switch root {
	case types.WorkbenchFileRootWorkspace:
		return "/workspace", nil
	case types.WorkbenchFileRootInput:
		return "/workspace/input", nil
	case types.WorkbenchFileRootOutput:
		return "/workspace/output", nil
	case types.WorkbenchFileRootArtifact:
		return "", ErrWorkbenchFileUnsupported
	default:
		return "", ErrWorkbenchFilePathDenied
	}
}

func remotePathToWorkbenchFileRef(root types.WorkbenchFileRoot, remotePath string) (types.WorkbenchFileRef, error) {
	base, err := workbenchFileRootPath(root)
	if err != nil {
		return types.WorkbenchFileRef{}, err
	}
	if remotePath != base && !strings.HasPrefix(remotePath, strings.TrimRight(base, "/")+"/") {
		return types.WorkbenchFileRef{}, ErrWorkbenchFilePathDenied
	}
	trimmed := strings.TrimPrefix(remotePath, strings.TrimRight(base, "/")+"/")
	var segments []string
	if trimmed != remotePath && trimmed != "" {
		for _, segment := range strings.Split(trimmed, "/") {
			clean, err := cleanWorkbenchFileSegment(segment)
			if err != nil {
				return types.WorkbenchFileRef{}, err
			}
			segments = append(segments, clean)
		}
	}
	return types.WorkbenchFileRef{FileRefVersion: 1, Root: root, Segments: segments}, nil
}

func mapWorkbenchFileRemoteError(err error) error {
	if err == nil {
		return nil
	}
	if sandbox.IsRemoteNotFound(err) {
		return ErrWorkbenchFileNotFound
	}
	if sandbox.IsRemoteInvalidRequest(err) {
		return ErrWorkbenchFilePathDenied
	}
	return err
}
