package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestT2L06PresentationSkillCandidatePublishesArtifactWithServerRunID(t *testing.T) {
	ctx := context.Background()
	closed := time.Now().UTC()
	commands := t2l05ArtifactCommands("cmd-1")
	commands.rows["cmd-1"].State = types.WorkbenchCommandStateSucceeded
	commands.rows["cmd-1"].ClosedAt = &closed
	commands.rows["cmd-1"].Payload = types.JSONMap{"command": "generate", "skill_name": "presentations", "skill_operation": "create_presentation"}
	files := &t2l05ArtifactFileSource{content: t2l05MinimalPPTX(t), entry: types.WorkbenchFileEntry{Name: "slides.pptx", Type: "file", Ref: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"slides.pptx"}}}}
	artifacts := NewWorkbenchArtifactService(&t2l05ArtifactRepo{}, files, commands)
	skillRuns := &t2l06SkillRunRepo{}
	svc := NewWorkbenchPresentationSkillService(skillRuns, commands, artifacts)

	run, artifact, err := svc.PublishCandidate(ctx, PublishWorkbenchPresentationCandidateInput{
		Scope: t2l04Scope(), CommandID: "cmd-1", ActorID: "actor-1",
		SourceRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"slides.pptx"}}, ArtifactID: "client-supplied", Version: 5,
	})
	require.NoError(t, err)
	require.NotEmpty(t, run.ID)
	require.Equal(t, types.WorkbenchSkillRunStateSucceeded, run.State)
	require.Equal(t, "presentations", run.SkillName)
	require.Equal(t, "create_presentation", run.SkillOperation)
	require.Equal(t, run.ID, artifact.SkillRunID)
	require.Equal(t, run.ID, artifact.ArtifactID)
	require.Equal(t, 1, artifact.Version)
	require.Equal(t, artifact.ArtifactID, run.OutputArtifactID)
	require.Equal(t, artifact.Version, run.OutputArtifactVersion)
	require.Equal(t, types.WorkbenchPreviewClassPresentationPage, artifact.PreviewClass)
	require.Equal(t, t2l04Scope(), files.scopes[0])
}

func TestT2L06PresentationSkillCandidateIsIdempotentForCompletedRun(t *testing.T) {
	ctx := context.Background()
	commands := t2l06PresentationCommands("cmd-idem")
	publisher := &t2l06ArtifactPublisher{}
	runs := &t2l06SkillRunRepo{}
	svc := NewWorkbenchPresentationSkillService(runs, commands, publisher)
	input := PublishWorkbenchPresentationCandidateInput{Scope: t2l04Scope(), CommandID: "cmd-idem", ActorID: "actor-1", SourceRef: t2l06PPTXRef()}

	firstRun, firstArtifact, err := svc.PublishCandidate(ctx, input)
	require.NoError(t, err)
	secondRun, secondArtifact, err := svc.PublishCandidate(ctx, input)
	require.NoError(t, err)

	require.Equal(t, firstRun.ID, secondRun.ID)
	require.Equal(t, firstArtifact.ArtifactID, secondArtifact.ArtifactID)
	require.Equal(t, types.WorkbenchSkillRunStateSucceeded, secondRun.State)
	require.Equal(t, 1, publisher.publishCalls)
	require.Len(t, runs.rows, 1)
}

func TestT2L06PresentationSkillCandidateRecoversExistingQueuedRun(t *testing.T) {
	ctx := context.Background()
	transientErr := errors.New("temporary artifact store failure")
	commands := t2l06PresentationCommands("cmd-retry")
	publisher := &t2l06ArtifactPublisher{publishErr: transientErr}
	runs := &t2l06SkillRunRepo{}
	svc := NewWorkbenchPresentationSkillService(runs, commands, publisher)
	input := PublishWorkbenchPresentationCandidateInput{Scope: t2l04Scope(), CommandID: "cmd-retry", ActorID: "actor-1", SourceRef: t2l06PPTXRef()}

	_, _, err := svc.PublishCandidate(ctx, input)
	require.ErrorIs(t, err, transientErr)
	queued, err := runs.GetSkillRunByCommand(ctx, 10, "chat-1", "cmd-retry")
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchSkillRunStateQueued, queued.State)
	require.Empty(t, queued.OutputArtifactID)

	run, artifact, err := svc.PublishCandidate(ctx, input)
	require.NoError(t, err)
	require.Equal(t, queued.ID, run.ID)
	require.Equal(t, queued.ID, artifact.SkillRunID)
	require.Equal(t, types.WorkbenchSkillRunStateSucceeded, run.State)
	require.Equal(t, 2, publisher.publishCalls)
	require.Len(t, runs.rows, 1)
}

func TestT2L06PresentationSkillCandidateRejectsUntrustedCommands(t *testing.T) {
	ctx := context.Background()
	closed := time.Now().UTC()
	commands := t2l05ArtifactCommands("queued", "plain", "wrong-actor", "wrong-job", "wrong-workbench")
	commands.rows["plain"].State = types.WorkbenchCommandStateSucceeded
	commands.rows["plain"].ClosedAt = &closed
	commands.rows["plain"].Payload = types.JSONMap{"command": "printf ok"}
	commands.rows["wrong-actor"].State = types.WorkbenchCommandStateSucceeded
	commands.rows["wrong-actor"].ClosedAt = &closed
	commands.rows["wrong-actor"].CreatedBy = "actor-other"
	commands.rows["wrong-actor"].Payload = types.JSONMap{"skill_name": "presentations", "skill_operation": "create_presentation"}
	commands.rows["wrong-job"].State = types.WorkbenchCommandStateSucceeded
	commands.rows["wrong-job"].ClosedAt = &closed
	commands.rows["wrong-job"].WorkbenchJobID = "job-other"
	commands.rows["wrong-job"].Payload = types.JSONMap{"skill_name": "presentations", "skill_operation": "create_presentation"}
	commands.rows["wrong-workbench"].State = types.WorkbenchCommandStateSucceeded
	commands.rows["wrong-workbench"].ClosedAt = &closed
	commands.rows["wrong-workbench"].WorkbenchSessionID = "wb-other"
	commands.rows["wrong-workbench"].Payload = types.JSONMap{"skill_name": "presentations", "skill_operation": "create_presentation"}
	artifacts := NewWorkbenchArtifactService(&t2l05ArtifactRepo{}, &t2l05ArtifactFileSource{content: []byte("x"), entry: types.WorkbenchFileEntry{Name: "slides.pptx", Type: "file"}}, commands)
	svc := NewWorkbenchPresentationSkillService(&t2l06SkillRunRepo{}, commands, artifacts)

	base := PublishWorkbenchPresentationCandidateInput{Scope: t2l04Scope(), ActorID: "actor-1", SourceRef: t2l06PPTXRef()}
	base.CommandID = "queued"
	_, _, err := svc.PublishCandidate(ctx, base)
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)

	base.CommandID = "plain"
	_, _, err = svc.PublishCandidate(ctx, base)
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)

	base.CommandID = "wrong-actor"
	_, _, err = svc.PublishCandidate(ctx, base)
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)

	base.CommandID = "wrong-job"
	_, _, err = svc.PublishCandidate(ctx, base)
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)

	base.CommandID = "wrong-workbench"
	_, _, err = svc.PublishCandidate(ctx, base)
	require.ErrorIs(t, err, repository.ErrWorkbenchCommandNotFound)
}

func t2l06PresentationCommands(ids ...string) *t2l05ArtifactCommandsRepo {
	commands := t2l05ArtifactCommands(ids...)
	closed := time.Now().UTC()
	for _, id := range ids {
		commands.rows[id].State = types.WorkbenchCommandStateSucceeded
		commands.rows[id].ClosedAt = &closed
		commands.rows[id].Payload = types.JSONMap{"skill_name": "presentations", "skill_operation": "create_presentation"}
	}
	return commands
}

func t2l06PPTXRef() types.WorkbenchFileRef {
	return types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"slides.pptx"}}
}

type t2l06SkillRunRepo struct {
	rows      map[string]*types.WorkbenchSkillRun
	byCommand map[string]string
}

func (r *t2l06SkillRunRepo) ProbeSchema(context.Context) error { return nil }

func (r *t2l06SkillRunRepo) CreateSkillRun(_ context.Context, input repository.WorkbenchSkillRunInput) (*types.WorkbenchSkillRun, error) {
	if r.rows == nil {
		r.rows = make(map[string]*types.WorkbenchSkillRun)
	}
	if r.byCommand == nil {
		r.byCommand = make(map[string]string)
	}
	commandID := strings.TrimSpace(input.CommandID)
	if existingID := r.byCommand[commandID]; existingID != "" && r.rows[existingID] != nil && r.rows[existingID].TenantID == input.TenantID && r.rows[existingID].ChatSessionID == input.ChatSessionID {
		return nil, repository.ErrWorkbenchSkillRunConflict
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = "skill-run-" + commandID
	}
	row := &types.WorkbenchSkillRun{ID: id, TenantID: input.TenantID, ChatSessionID: input.ChatSessionID, WorkbenchSessionID: input.WorkbenchSessionID, WorkbenchJobID: input.WorkbenchJobID, CommandID: commandID, SkillName: input.SkillName, SkillOperation: input.SkillOperation, OutputFileRef: input.OutputFileRef, State: types.WorkbenchSkillRunStateQueued, StateVersion: 0, CreatedBy: input.CreatedBy, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	r.rows[id] = row
	r.byCommand[commandID] = id
	copy := *row
	return &copy, nil
}

func (r *t2l06SkillRunRepo) GetSkillRun(_ context.Context, tenantID uint64, chatSessionID, id string) (*types.WorkbenchSkillRun, error) {
	row := r.rows[strings.TrimSpace(id)]
	if row == nil || row.TenantID != tenantID || row.ChatSessionID != chatSessionID {
		return nil, repository.ErrWorkbenchSkillRunNotFound
	}
	copy := *row
	return &copy, nil
}

func (r *t2l06SkillRunRepo) GetSkillRunByCommand(_ context.Context, tenantID uint64, chatSessionID, commandID string) (*types.WorkbenchSkillRun, error) {
	if r.byCommand == nil {
		return nil, repository.ErrWorkbenchSkillRunNotFound
	}
	id := r.byCommand[strings.TrimSpace(commandID)]
	return r.GetSkillRun(context.Background(), tenantID, chatSessionID, id)
}

func (r *t2l06SkillRunRepo) CompleteSkillRun(_ context.Context, input repository.WorkbenchSkillRunCompleteCAS) (*types.WorkbenchSkillRun, error) {
	row := r.rows[strings.TrimSpace(input.ID)]
	if row == nil || row.TenantID != input.TenantID || row.StateVersion != input.ExpectedStateVersion || row.State != input.ExpectedState {
		return nil, repository.ErrWorkbenchSkillRunStaleVersion
	}
	now := time.Now().UTC()
	row.State = input.NextState
	row.StateVersion++
	row.OutputArtifactID = input.OutputArtifactID
	row.OutputArtifactVersion = input.OutputArtifactVersion
	row.ClosedAt = &now
	row.UpdatedAt = now
	copy := *row
	return &copy, nil
}

type t2l06ArtifactPublisher struct {
	publishErr   error
	publishCalls int
	getCalls     int
	rows         map[string]*types.WorkbenchArtifactVersion
}

func (p *t2l06ArtifactPublisher) PublishFromSkillRun(_ context.Context, input PublishWorkbenchArtifactInput, skillRunID string) (*types.WorkbenchArtifactVersion, error) {
	p.publishCalls++
	if p.publishErr != nil {
		err := p.publishErr
		p.publishErr = nil
		return nil, err
	}
	if p.rows == nil {
		p.rows = make(map[string]*types.WorkbenchArtifactVersion)
	}
	artifactID := strings.TrimSpace(input.ArtifactID)
	version := input.Version
	key := fmt.Sprintf("%d/%s/%s/%d", input.Scope.TenantID, input.Scope.ChatSessionID, artifactID, version)
	if p.rows[key] != nil {
		return nil, repository.ErrWorkbenchArtifactConflict
	}
	row := &types.WorkbenchArtifactVersion{ID: "artifact-version-" + artifactID, ArtifactID: artifactID, Version: version, TenantID: input.Scope.TenantID, ChatSessionID: input.Scope.ChatSessionID, WorkbenchSessionID: input.Scope.WorkbenchSessionID, WorkbenchJobID: input.Scope.JobID, CommandID: input.CommandID, SkillRunID: strings.TrimSpace(skillRunID), SourceFileRef: input.SourceRef, ContentSHA256: strings.Repeat("a", 64), SizeBytes: 1, MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", PreviewClass: types.WorkbenchPreviewClassPresentationPage, FileName: "slides.pptx", Content: []byte("x"), CreatedBy: input.ActorID, CreatedAt: time.Now().UTC()}
	p.rows[key] = row
	copy := *row
	copy.Content = append([]byte(nil), row.Content...)
	return &copy, nil
}

func (p *t2l06ArtifactPublisher) Get(_ context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error) {
	p.getCalls++
	key := fmt.Sprintf("%d/%s/%s/%d", tenantID, chatSessionID, strings.TrimSpace(artifactID), version)
	row := p.rows[key]
	if row == nil {
		return nil, repository.ErrWorkbenchArtifactNotFound
	}
	copy := *row
	copy.Content = append([]byte(nil), row.Content...)
	return &copy, nil
}
