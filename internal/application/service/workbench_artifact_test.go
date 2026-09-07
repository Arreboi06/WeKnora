package service

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L05WorkbenchArtifactServicePublishesImmutableCSVPreview(t *testing.T) {
	ctx := context.Background()
	repo := &t2l05ArtifactRepo{}
	files := &t2l05ArtifactFileSource{
		content: []byte("name,value\n<script>,2\n"),
		entry:   types.WorkbenchFileEntry{Name: "report.csv", Type: "file", SizeBytes: 22, Ref: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"report.csv"}}},
	}
	svc := NewWorkbenchArtifactService(repo, files, t2l05ArtifactCommands("cmd-1"))

	artifact, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"report.csv"}},
		CommandID: "cmd-1", ActorID: "actor-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, artifact.ArtifactID)
	require.Equal(t, 1, artifact.Version)
	require.Equal(t, "cmd-1", artifact.CommandID)
	require.Equal(t, types.WorkbenchPreviewClassTableCSV, artifact.PreviewClass)
	require.Equal(t, "text/csv; charset=utf-8", artifact.MimeType)
	require.Equal(t, files.entry.Ref, artifact.SourceFileRef)
	require.NotContains(t, artifact.ArtifactID, "container")
	require.Equal(t, t2l04Scope(), files.scopes[0])

	preview, err := svc.Preview(ctx, 10, "chat-1", artifact.ArtifactID, artifact.Version)
	require.NoError(t, err)
	require.Equal(t, "text/html; charset=utf-8", preview.ContentType)
	require.Contains(t, string(preview.Content), "<table")
	require.Contains(t, string(preview.Content), "&lt;script&gt;")
	require.NotContains(t, string(preview.Content), "<script>")
}

func TestT2L05WorkbenchArtifactServiceRejectsMutableOrWrongScopeSources(t *testing.T) {
	ctx := context.Background()
	files := &t2l05ArtifactFileSource{content: []byte("x"), entry: types.WorkbenchFileEntry{Name: "a.txt", Type: "file", SizeBytes: 1}}
	svc := NewWorkbenchArtifactService(&t2l05ArtifactRepo{}, files, t2l05ArtifactCommands("cmd-1"))

	_, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootWorkspace, Segments: []string{"output", "a.txt"}},
		CommandID: "cmd-1", ActorID: "actor-1",
	})
	require.ErrorIs(t, err, ErrWorkbenchFilePathDenied)
	require.Empty(t, files.scopes)

	_, err = svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"a.txt"}},
		CommandID: "cmd-1", ActorID: "",
	})
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionInvalidArgument)
}

func TestT2L05WorkbenchArtifactServiceClassifiesHTMLAndPPTX(t *testing.T) {
	ctx := context.Background()
	repo := &t2l05ArtifactRepo{}
	commands := t2l05ArtifactCommands("cmd-1", "cmd-2")
	svc := NewWorkbenchArtifactService(repo, &t2l05ArtifactFileSource{content: []byte("<html><script>document.cookie</script></html>"), entry: types.WorkbenchFileEntry{Name: "index.html", Type: "file", SizeBytes: 45}}, commands)
	htmlArtifact, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"index.html"}}, CommandID: "cmd-1", ActorID: "actor-1"})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchPreviewClassHTMLActive, htmlArtifact.PreviewClass)

	pptx := t2l05MinimalPPTX(t)
	svc = NewWorkbenchArtifactService(repo, &t2l05ArtifactFileSource{content: pptx, entry: types.WorkbenchFileEntry{Name: "slides.pptx", Type: "file", SizeBytes: int64(len(pptx))}}, commands)
	pptArtifact, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"slides.pptx"}}, CommandID: "cmd-2", ActorID: "actor-1"})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchPreviewClassPresentationPage, pptArtifact.PreviewClass)
	preview, err := svc.Preview(ctx, 10, "chat-1", pptArtifact.ArtifactID, pptArtifact.Version)
	require.NoError(t, err)
	require.Contains(t, string(preview.Content), "Slides: 1")
}

func TestT2L05WorkbenchArtifactServiceUsesNormalizedRefAndEnforcesQuota(t *testing.T) {
	ctx := context.Background()
	repo := &t2l05ArtifactRepo{}
	normalized := types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"final.csv"}}
	files := &t2l05ArtifactFileSource{
		content: []byte("a,b\n1,2\n"),
		entry: types.WorkbenchFileEntry{
			Name: "final.csv", Type: "file", SizeBytes: 8, Ref: normalized,
		},
	}
	svc := NewWorkbenchArtifactService(repo, files, t2l05ArtifactCommands("cmd-1", "cmd-2"))

	artifact, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"raw.csv"}},
		CommandID: "cmd-1", ActorID: "actor-1",
	})
	require.NoError(t, err)
	require.Equal(t, normalized, artifact.SourceFileRef)

	svc.maxBytes = 4
	_, err = svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"final.csv"}},
		CommandID: "cmd-2", ActorID: "actor-1",
	})
	require.ErrorIs(t, err, ErrWorkbenchFileQuotaExceeded)
}

func TestT2L05WorkbenchArtifactServiceRejectsSpoofedProvenance(t *testing.T) {
	ctx := context.Background()
	files := &t2l05ArtifactFileSource{content: []byte("a,b\n1,2\n"), entry: types.WorkbenchFileEntry{Name: "report.csv", Type: "file", SizeBytes: 8}}
	commands := t2l05ArtifactCommands("cmd-1")
	commands.rows["cmd-wrong-job"] = &types.WorkbenchCommand{ID: "cmd-wrong-job", TenantID: 10, WorkbenchJobID: "job-other", WorkbenchSessionID: "wb-1", CreatedBy: "actor-1"}
	commands.rows["cmd-wrong-actor"] = &types.WorkbenchCommand{ID: "cmd-wrong-actor", TenantID: 10, WorkbenchJobID: "job-1", WorkbenchSessionID: "wb-1", CreatedBy: "actor-other"}
	svc := NewWorkbenchArtifactService(&t2l05ArtifactRepo{}, files, commands)

	_, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"report.csv"}},
		CommandID: "cmd-wrong-job", ActorID: "actor-1",
	})
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)

	_, err = svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"report.csv"}},
		CommandID: "cmd-wrong-actor", ActorID: "actor-1",
	})
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)
	require.Empty(t, files.scopes)

	artifact, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(), SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"report.csv"}},
		CommandID: "cmd-1", MessageID: "spoofed-message", SkillRunID: "spoofed-skill", ActorID: "actor-1",
	})
	require.NoError(t, err)
	require.Equal(t, "cmd-1", artifact.CommandID)
	require.Empty(t, artifact.MessageID)
	require.Empty(t, artifact.SkillRunID)
}

func TestT2L07WorkbenchArtifactServiceRejectsNonTerminalCommand(t *testing.T) {
	ctx := context.Background()
	commands := t2l05ArtifactCommands("cmd-queued")
	commands.rows["cmd-queued"].State = types.WorkbenchCommandStateQueued
	commands.rows["cmd-queued"].ClosedAt = nil
	files := &t2l05ArtifactFileSource{
		content: []byte("a,b\n1,2\n"),
		entry:   types.WorkbenchFileEntry{Name: "report.csv", Type: "file", SizeBytes: 8},
	}
	svc := NewWorkbenchArtifactService(&t2l05ArtifactRepo{}, files, commands)

	_, err := svc.Publish(ctx, PublishWorkbenchArtifactInput{
		Scope: t2l04Scope(),
		SourceRef: types.WorkbenchFileRef{
			FileRefVersion: 1,
			Root:           types.WorkbenchFileRootOutput,
			Segments:       []string{"report.csv"},
		},
		CommandID: "cmd-queued",
		ActorID:   "actor-1",
	})
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)
	require.Empty(t, files.scopes)
}

type t2l05ArtifactRepo struct {
	rows []*types.WorkbenchArtifactVersion
}

func (r *t2l05ArtifactRepo) ProbeSchema(context.Context) error { return nil }
func (r *t2l05ArtifactRepo) CreateArtifactVersion(_ context.Context, input repository.WorkbenchArtifactVersionInput) (*types.WorkbenchArtifactVersion, error) {
	row := &types.WorkbenchArtifactVersion{ID: input.ID, ArtifactID: input.ArtifactID, Version: input.Version, TenantID: input.TenantID, ChatSessionID: input.ChatSessionID, WorkbenchSessionID: input.WorkbenchSessionID, WorkbenchJobID: input.WorkbenchJobID, CommandID: input.CommandID, MessageID: input.MessageID, SkillRunID: input.SkillRunID, SourceFileRef: input.SourceFileRef, ContentSHA256: input.ContentSHA256, SizeBytes: input.SizeBytes, MimeType: input.MimeType, PreviewClass: input.PreviewClass, FileName: input.FileName, Content: append([]byte(nil), input.Content...), CreatedBy: input.CreatedBy, CreatedAt: time.Now().UTC()}
	r.rows = append(r.rows, row)
	return row, nil
}
func (r *t2l05ArtifactRepo) GetArtifactVersion(_ context.Context, tenantID uint64, chatID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error) {
	for _, row := range r.rows {
		if row.TenantID == tenantID && row.ChatSessionID == chatID && row.ArtifactID == artifactID && row.Version == version {
			copy := *row
			copy.Content = append([]byte(nil), row.Content...)
			return &copy, nil
		}
	}
	return nil, repository.ErrWorkbenchArtifactNotFound
}

type t2l05ArtifactCommandsRepo struct {
	rows map[string]*types.WorkbenchCommand
}

func t2l05ArtifactCommands(ids ...string) *t2l05ArtifactCommandsRepo {
	repo := &t2l05ArtifactCommandsRepo{rows: make(map[string]*types.WorkbenchCommand)}
	closed := time.Now().UTC()
	for _, id := range ids {
		repo.rows[id] = &types.WorkbenchCommand{
			ID: id, TenantID: 10, WorkbenchJobID: "job-1", WorkbenchSessionID: "wb-1",
			CreatedBy: "actor-1", State: types.WorkbenchCommandStateSucceeded, ClosedAt: &closed,
		}
	}
	return repo
}

func (r *t2l05ArtifactCommandsRepo) GetCommandByID(_ context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error) {
	row, ok := r.rows[strings.TrimSpace(id)]
	if !ok || row.TenantID != tenantID {
		return nil, repository.ErrWorkbenchCommandNotFound
	}
	copy := *row
	return &copy, nil
}

func (r *t2l05ArtifactCommandsRepo) GetActiveJobForSession(_ context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error) {
	if tenantID != 10 || chatSessionID != "chat-1" || workbenchSessionID != "wb-1" || jobID != "job-1" || expectedLeaseEpoch != 7 {
		return nil, repository.ErrWorkbenchJobNotFound
	}
	return &types.WorkbenchJob{
		ID: jobID, TenantID: tenantID, ChatSessionID: chatSessionID, WorkbenchSessionID: workbenchSessionID,
		IncarnationID: "inc-1", LeaseEpoch: expectedLeaseEpoch, State: types.WorkbenchJobStateRunning,
	}, nil
}

type t2l05ArtifactFileSource struct {
	content []byte
	entry   types.WorkbenchFileEntry
	scopes  []WorkbenchFileScope
}

func (s *t2l05ArtifactFileSource) Download(_ context.Context, scope WorkbenchFileScope, ref types.WorkbenchFileRef) ([]byte, types.WorkbenchFileEntry, error) {
	s.scopes = append(s.scopes, scope)
	entry := s.entry
	if entry.Ref.FileRefVersion == 0 {
		entry.Ref = ref
	}
	if strings.TrimSpace(entry.Name) == "" && len(ref.Segments) > 0 {
		entry.Name = ref.Segments[len(ref.Segments)-1]
	}
	entry.SizeBytes = int64(len(s.content))
	return append([]byte(nil), s.content...), entry, nil
}

func t2l05MinimalPPTX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	contentTypes, err := zw.Create("[Content_Types].xml")
	require.NoError(t, err)
	_, err = contentTypes.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>`))
	require.NoError(t, err)
	slide, err := zw.Create("ppt/slides/slide1.xml")
	require.NoError(t, err)
	_, err = slide.Write([]byte(`<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"></p:sld>`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
