package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"html"
	"mime"
	"path"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

var ErrWorkbenchPreviewBlocked = errors.New("workbench preview blocked")

const defaultMaxWorkbenchArtifactBytes int64 = 50 << 20

type WorkbenchArtifactFileSource interface {
	Download(ctx context.Context, scope WorkbenchFileScope, ref types.WorkbenchFileRef) ([]byte, types.WorkbenchFileEntry, error)
}

type WorkbenchArtifactCommandLookup interface {
	GetCommandByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error)
	GetActiveJobForSession(ctx context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error)
}

type PublishWorkbenchArtifactInput struct {
	Scope      WorkbenchFileScope
	SourceRef  types.WorkbenchFileRef
	ArtifactID string
	Version    int
	CommandID  string
	MessageID  string
	SkillRunID string
	ActorID    string
}

type WorkbenchArtifactDownload struct {
	Artifact    *types.WorkbenchArtifactVersion
	ContentType string
	Content     []byte
}

type WorkbenchArtifactPreview struct {
	Artifact    *types.WorkbenchArtifactVersion
	ContentType string
	Content     []byte
}

type WorkbenchArtifactService struct {
	repo     repository.WorkbenchArtifactRepository
	files    WorkbenchArtifactFileSource
	commands WorkbenchArtifactCommandLookup
	maxBytes int64
}

func NewWorkbenchArtifactService(repo repository.WorkbenchArtifactRepository, files WorkbenchArtifactFileSource, lookups ...WorkbenchArtifactCommandLookup) *WorkbenchArtifactService {
	var commands WorkbenchArtifactCommandLookup
	if len(lookups) > 0 {
		commands = lookups[0]
	}
	return &WorkbenchArtifactService{repo: repo, files: files, commands: commands, maxBytes: defaultMaxWorkbenchArtifactBytes}
}

func (s *WorkbenchArtifactService) Publish(ctx context.Context, input PublishWorkbenchArtifactInput) (*types.WorkbenchArtifactVersion, error) {
	return s.publish(ctx, input, "")
}

func (s *WorkbenchArtifactService) PublishFromSkillRun(ctx context.Context, input PublishWorkbenchArtifactInput, skillRunID string) (*types.WorkbenchArtifactVersion, error) {
	if strings.TrimSpace(skillRunID) == "" {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	return s.publish(ctx, input, strings.TrimSpace(skillRunID))
}

func (s *WorkbenchArtifactService) publish(ctx context.Context, input PublishWorkbenchArtifactInput, verifiedSkillRunID string) (*types.WorkbenchArtifactVersion, error) {
	if s == nil || s.repo == nil || s.files == nil || s.commands == nil || input.Scope.TenantID == 0 || strings.TrimSpace(input.Scope.ChatSessionID) == "" ||
		strings.TrimSpace(input.Scope.WorkbenchSessionID) == "" || strings.TrimSpace(input.Scope.JobID) == "" || strings.TrimSpace(input.CommandID) == "" || strings.TrimSpace(input.ActorID) == "" {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	if input.SourceRef.Root != types.WorkbenchFileRootOutput || len(input.SourceRef.Segments) == 0 {
		return nil, ErrWorkbenchFilePathDenied
	}
	if _, err := s.commands.GetActiveJobForSession(ctx, input.Scope.TenantID, strings.TrimSpace(input.Scope.ChatSessionID), strings.TrimSpace(input.Scope.WorkbenchSessionID), strings.TrimSpace(input.Scope.JobID), input.Scope.ExpectedLeaseEpoch); err != nil {
		return nil, err
	}
	if err := s.repo.ProbeSchema(ctx); err != nil {
		return nil, err
	}
	command, err := s.resolveCommand(ctx, input)
	if err != nil {
		return nil, err
	}
	content, entry, err := s.files.Download(ctx, input.Scope, input.SourceRef)
	if err != nil {
		return nil, err
	}
	if s.maxBytes > 0 && int64(len(content)) > s.maxBytes {
		return nil, ErrWorkbenchFileQuotaExceeded
	}
	artifactID := strings.TrimSpace(input.ArtifactID)
	if artifactID == "" {
		artifactID = uuid.NewString()
	}
	version := input.Version
	if version <= 0 {
		version = 1
	}
	fileName := strings.TrimSpace(entry.Name)
	if fileName == "" {
		fileName = input.SourceRef.Segments[len(input.SourceRef.Segments)-1]
	}
	fileName = path.Base(fileName)
	mimeType, previewClass := classifyWorkbenchArtifact(fileName)
	sum := sha256.Sum256(content)
	return s.repo.CreateArtifactVersion(ctx, repository.WorkbenchArtifactVersionInput{
		ArtifactID:         artifactID,
		Version:            version,
		TenantID:           input.Scope.TenantID,
		ChatSessionID:      strings.TrimSpace(input.Scope.ChatSessionID),
		WorkbenchSessionID: strings.TrimSpace(input.Scope.WorkbenchSessionID),
		WorkbenchJobID:     strings.TrimSpace(input.Scope.JobID),
		CommandID:          command.ID,
		MessageID:          "",
		SkillRunID:         verifiedSkillRunID,
		SourceFileRef:      entry.Ref,
		ContentSHA256:      hex.EncodeToString(sum[:]),
		SizeBytes:          int64(len(content)),
		MimeType:           mimeType,
		PreviewClass:       previewClass,
		FileName:           fileName,
		Content:            content,
		CreatedBy:          strings.TrimSpace(input.ActorID),
	})
}

func (s *WorkbenchArtifactService) resolveCommand(ctx context.Context, input PublishWorkbenchArtifactInput) (*types.WorkbenchCommand, error) {
	commandID := strings.TrimSpace(input.CommandID)
	command, err := s.commands.GetCommandByID(ctx, input.Scope.TenantID, commandID)
	if err != nil {
		return nil, err
	}
	if command == nil || command.TenantID != input.Scope.TenantID || strings.TrimSpace(command.ID) != commandID ||
		strings.TrimSpace(command.WorkbenchJobID) != strings.TrimSpace(input.Scope.JobID) ||
		strings.TrimSpace(command.WorkbenchSessionID) != strings.TrimSpace(input.Scope.WorkbenchSessionID) ||
		strings.TrimSpace(command.CreatedBy) != strings.TrimSpace(input.ActorID) ||
		command.State != types.WorkbenchCommandStateSucceeded || command.ClosedAt == nil {
		return nil, repository.ErrWorkbenchCommandNotFound
	}
	return command, nil
}

func (s *WorkbenchArtifactService) Get(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error) {
	if s == nil || s.repo == nil {
		return nil, ErrWorkbenchFileUnsupported
	}
	if err := s.repo.ProbeSchema(ctx); err != nil {
		return nil, err
	}
	return s.repo.GetArtifactVersion(ctx, tenantID, strings.TrimSpace(chatSessionID), strings.TrimSpace(artifactID), version)
}

func (s *WorkbenchArtifactService) Download(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*WorkbenchArtifactDownload, error) {
	artifact, err := s.Get(ctx, tenantID, chatSessionID, artifactID, version)
	if err != nil {
		return nil, err
	}
	contentType := strings.TrimSpace(artifact.MimeType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &WorkbenchArtifactDownload{Artifact: artifact, ContentType: contentType, Content: append([]byte(nil), artifact.Content...)}, nil
}

func (s *WorkbenchArtifactService) Preview(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*WorkbenchArtifactPreview, error) {
	artifact, err := s.Get(ctx, tenantID, chatSessionID, artifactID, version)
	if err != nil {
		return nil, err
	}
	switch artifact.PreviewClass {
	case types.WorkbenchPreviewClassHTMLActive:
		return &WorkbenchArtifactPreview{Artifact: artifact, ContentType: "text/html; charset=utf-8", Content: append([]byte(nil), artifact.Content...)}, nil
	case types.WorkbenchPreviewClassTableCSV:
		return &WorkbenchArtifactPreview{Artifact: artifact, ContentType: "text/html; charset=utf-8", Content: renderWorkbenchCSVPreview(artifact)}, nil
	case types.WorkbenchPreviewClassPresentationPage:
		return &WorkbenchArtifactPreview{Artifact: artifact, ContentType: "text/html; charset=utf-8", Content: renderWorkbenchPPTXPreview(artifact)}, nil
	default:
		return nil, ErrWorkbenchPreviewBlocked
	}
}

func classifyWorkbenchArtifact(fileName string) (string, types.WorkbenchPreviewClass) {
	ext := strings.ToLower(path.Ext(fileName))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8", types.WorkbenchPreviewClassHTMLActive
	case ".csv":
		return "text/csv; charset=utf-8", types.WorkbenchPreviewClassTableCSV
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation", types.WorkbenchPreviewClassPresentationPage
	default:
		if guessed := mime.TypeByExtension(ext); guessed != "" {
			return guessed, types.WorkbenchPreviewClassDownloadOnly
		}
		return "application/octet-stream", types.WorkbenchPreviewClassDownloadOnly
	}
}

func renderWorkbenchCSVPreview(artifact *types.WorkbenchArtifactVersion) []byte {
	reader := csv.NewReader(bytes.NewReader(artifact.Content))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><table>")
	if err != nil {
		b.WriteString("<tbody><tr><td>")
		b.WriteString(html.EscapeString(string(artifact.Content)))
		b.WriteString("</td></tr></tbody></table>")
		return []byte(b.String())
	}
	for _, row := range rows {
		b.WriteString("<tr>")
		for _, cell := range row {
			b.WriteString("<td>")
			b.WriteString(html.EscapeString(cell))
			b.WriteString("</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	return []byte(b.String())
}

func renderWorkbenchPPTXPreview(artifact *types.WorkbenchArtifactVersion) []byte {
	zr, err := zip.NewReader(bytes.NewReader(artifact.Content), int64(len(artifact.Content)))
	slideCount := 0
	if err == nil {
		for _, file := range zr.File {
			name := strings.ToLower(file.Name)
			if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") {
				slideCount++
			}
		}
	}
	var b strings.Builder
	b.WriteString("<!doctype html><meta charset=\"utf-8\"><section><h1>")
	b.WriteString(html.EscapeString(artifact.FileName))
	b.WriteString("</h1><p>Slides: ")
	b.WriteString(html.EscapeString(fmtInt(slideCount)))
	b.WriteString("</p><p>SHA-256: ")
	b.WriteString(html.EscapeString(artifact.ContentSHA256))
	b.WriteString("</p></section>")
	return []byte(b.String())
}

func fmtInt(v int) string {
	if v == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for v > 0 {
		i--
		digits[i] = byte('0' + v%10)
		v /= 10
	}
	return string(digits[i:])
}
