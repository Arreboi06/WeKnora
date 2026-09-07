package service

import (
	"context"
	"errors"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	WorkbenchPresentationSkillName      = "presentations"
	WorkbenchPresentationSkillOperation = "create_presentation"
)

type WorkbenchPresentationSkillRepository interface {
	ProbeSchema(ctx context.Context) error
	CreateSkillRun(ctx context.Context, input repository.WorkbenchSkillRunInput) (*types.WorkbenchSkillRun, error)
	GetSkillRun(ctx context.Context, tenantID uint64, chatSessionID, id string) (*types.WorkbenchSkillRun, error)
	GetSkillRunByCommand(ctx context.Context, tenantID uint64, chatSessionID, commandID string) (*types.WorkbenchSkillRun, error)
	CompleteSkillRun(ctx context.Context, input repository.WorkbenchSkillRunCompleteCAS) (*types.WorkbenchSkillRun, error)
}

type WorkbenchPresentationArtifactPublisher interface {
	PublishFromSkillRun(ctx context.Context, input PublishWorkbenchArtifactInput, skillRunID string) (*types.WorkbenchArtifactVersion, error)
	Get(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error)
}

type PublishWorkbenchPresentationCandidateInput struct {
	Scope      WorkbenchFileScope
	CommandID  string
	SourceRef  types.WorkbenchFileRef
	ArtifactID string
	Version    int
	ActorID    string
}

type WorkbenchPresentationSkillService struct {
	runs      WorkbenchPresentationSkillRepository
	commands  WorkbenchArtifactCommandLookup
	artifacts WorkbenchPresentationArtifactPublisher
}

func NewWorkbenchPresentationSkillService(runs WorkbenchPresentationSkillRepository, commands WorkbenchArtifactCommandLookup, artifacts WorkbenchPresentationArtifactPublisher) *WorkbenchPresentationSkillService {
	return &WorkbenchPresentationSkillService{runs: runs, commands: commands, artifacts: artifacts}
}

func (s *WorkbenchPresentationSkillService) PublishCandidate(ctx context.Context, input PublishWorkbenchPresentationCandidateInput) (*types.WorkbenchSkillRun, *types.WorkbenchArtifactVersion, error) {
	if s == nil || s.runs == nil || s.commands == nil || s.artifacts == nil || input.Scope.TenantID == 0 || strings.TrimSpace(input.Scope.ChatSessionID) == "" ||
		strings.TrimSpace(input.Scope.WorkbenchSessionID) == "" || strings.TrimSpace(input.Scope.JobID) == "" || strings.TrimSpace(input.CommandID) == "" || strings.TrimSpace(input.ActorID) == "" {
		return nil, nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	if input.SourceRef.Root != types.WorkbenchFileRootOutput || len(input.SourceRef.Segments) == 0 || strings.ToLower(path.Ext(input.SourceRef.Segments[len(input.SourceRef.Segments)-1])) != ".pptx" {
		return nil, nil, ErrWorkbenchFilePathDenied
	}
	if _, err := s.commands.GetActiveJobForSession(ctx, input.Scope.TenantID, strings.TrimSpace(input.Scope.ChatSessionID), strings.TrimSpace(input.Scope.WorkbenchSessionID), strings.TrimSpace(input.Scope.JobID), input.Scope.ExpectedLeaseEpoch); err != nil {
		return nil, nil, err
	}
	if err := s.runs.ProbeSchema(ctx); err != nil {
		return nil, nil, err
	}
	command, err := s.resolvePresentationCommand(ctx, input)
	if err != nil {
		return nil, nil, err
	}
	run, err := s.runs.CreateSkillRun(ctx, repository.WorkbenchSkillRunInput{
		TenantID: input.Scope.TenantID, ChatSessionID: strings.TrimSpace(input.Scope.ChatSessionID), WorkbenchSessionID: strings.TrimSpace(input.Scope.WorkbenchSessionID),
		WorkbenchJobID: strings.TrimSpace(input.Scope.JobID), CommandID: command.ID, SkillName: WorkbenchPresentationSkillName,
		SkillOperation: WorkbenchPresentationSkillOperation, OutputFileRef: input.SourceRef, CreatedBy: strings.TrimSpace(input.ActorID), CreatedAt: time.Now().UTC(),
	})
	if errors.Is(err, repository.ErrWorkbenchSkillRunConflict) {
		run, err = s.runs.GetSkillRunByCommand(ctx, input.Scope.TenantID, strings.TrimSpace(input.Scope.ChatSessionID), command.ID)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := validateWorkbenchPresentationRunForInput(run, input, command.ID); err != nil {
		return nil, nil, err
	}
	if run.State == types.WorkbenchSkillRunStateSucceeded {
		artifact, err := s.artifacts.Get(ctx, input.Scope.TenantID, strings.TrimSpace(input.Scope.ChatSessionID), run.OutputArtifactID, run.OutputArtifactVersion)
		if err != nil {
			return nil, nil, err
		}
		if !workbenchPresentationArtifactMatchesRun(artifact, run, input, command.ID) {
			return nil, nil, repository.ErrWorkbenchArtifactNotFound
		}
		return run, artifact, nil
	}
	if run.State.IsTerminal() {
		return nil, nil, repository.ErrWorkbenchSkillRunConflict
	}
	if run.State != types.WorkbenchSkillRunStateQueued {
		return nil, nil, repository.ErrWorkbenchSkillRunConflict
	}

	artifactID := run.ID
	version := 1
	artifact, err := s.artifacts.PublishFromSkillRun(ctx, PublishWorkbenchArtifactInput{
		Scope: input.Scope, SourceRef: input.SourceRef, ArtifactID: artifactID, Version: version, CommandID: command.ID, ActorID: input.ActorID,
	}, run.ID)
	if errors.Is(err, repository.ErrWorkbenchArtifactConflict) {
		artifact, err = s.artifacts.Get(ctx, input.Scope.TenantID, strings.TrimSpace(input.Scope.ChatSessionID), artifactID, version)
		if err == nil && !workbenchPresentationArtifactMatchesRun(artifact, run, input, command.ID) {
			err = repository.ErrWorkbenchArtifactConflict
		}
	}
	if err != nil {
		return nil, nil, err
	}
	completed, err := s.runs.CompleteSkillRun(ctx, repository.WorkbenchSkillRunCompleteCAS{
		TenantID: input.Scope.TenantID, ID: run.ID, ExpectedStateVersion: run.StateVersion, ExpectedState: run.State,
		NextState: types.WorkbenchSkillRunStateSucceeded, OutputArtifactID: artifact.ArtifactID, OutputArtifactVersion: artifact.Version,
	})
	if errors.Is(err, repository.ErrWorkbenchSkillRunStaleVersion) {
		completed, err = s.runs.GetSkillRunByCommand(ctx, input.Scope.TenantID, strings.TrimSpace(input.Scope.ChatSessionID), command.ID)
		if err == nil && completed.State == types.WorkbenchSkillRunStateSucceeded && workbenchPresentationArtifactMatchesRun(artifact, completed, input, command.ID) {
			return completed, artifact, nil
		}
	}
	if err != nil {
		return nil, nil, err
	}
	return completed, artifact, nil
}

func (s *WorkbenchPresentationSkillService) resolvePresentationCommand(ctx context.Context, input PublishWorkbenchPresentationCandidateInput) (*types.WorkbenchCommand, error) {
	commandID := strings.TrimSpace(input.CommandID)
	command, err := s.commands.GetCommandByID(ctx, input.Scope.TenantID, commandID)
	if err != nil {
		return nil, err
	}
	if command == nil || command.TenantID != input.Scope.TenantID || strings.TrimSpace(command.ID) != commandID ||
		strings.TrimSpace(command.WorkbenchJobID) != strings.TrimSpace(input.Scope.JobID) ||
		strings.TrimSpace(command.WorkbenchSessionID) != strings.TrimSpace(input.Scope.WorkbenchSessionID) ||
		strings.TrimSpace(command.CreatedBy) != strings.TrimSpace(input.ActorID) || command.State != types.WorkbenchCommandStateSucceeded || command.ClosedAt == nil ||
		strings.ToLower(workbenchSkillPayloadString(command.Payload, "skill_name")) != WorkbenchPresentationSkillName || workbenchSkillPayloadString(command.Payload, "skill_operation") != WorkbenchPresentationSkillOperation {
		return nil, repository.ErrWorkbenchCommandNotFound
	}
	return command, nil
}

func validateWorkbenchPresentationRunForInput(run *types.WorkbenchSkillRun, input PublishWorkbenchPresentationCandidateInput, commandID string) error {
	if run == nil || run.TenantID != input.Scope.TenantID || strings.TrimSpace(run.ChatSessionID) != strings.TrimSpace(input.Scope.ChatSessionID) ||
		strings.TrimSpace(run.WorkbenchSessionID) != strings.TrimSpace(input.Scope.WorkbenchSessionID) || strings.TrimSpace(run.WorkbenchJobID) != strings.TrimSpace(input.Scope.JobID) ||
		strings.TrimSpace(run.CommandID) != strings.TrimSpace(commandID) || strings.TrimSpace(run.CreatedBy) != strings.TrimSpace(input.ActorID) ||
		strings.ToLower(strings.TrimSpace(run.SkillName)) != WorkbenchPresentationSkillName || strings.TrimSpace(run.SkillOperation) != WorkbenchPresentationSkillOperation ||
		!reflect.DeepEqual(run.OutputFileRef, input.SourceRef) {
		return repository.ErrWorkbenchSkillRunConflict
	}
	return nil
}

func workbenchPresentationArtifactMatchesRun(artifact *types.WorkbenchArtifactVersion, run *types.WorkbenchSkillRun, input PublishWorkbenchPresentationCandidateInput, commandID string) bool {
	return artifact != nil && run != nil && artifact.TenantID == input.Scope.TenantID && strings.TrimSpace(artifact.ChatSessionID) == strings.TrimSpace(input.Scope.ChatSessionID) &&
		strings.TrimSpace(artifact.WorkbenchSessionID) == strings.TrimSpace(input.Scope.WorkbenchSessionID) && strings.TrimSpace(artifact.WorkbenchJobID) == strings.TrimSpace(input.Scope.JobID) &&
		strings.TrimSpace(artifact.CommandID) == strings.TrimSpace(commandID) && strings.TrimSpace(artifact.SkillRunID) == strings.TrimSpace(run.ID) && strings.TrimSpace(artifact.CreatedBy) == strings.TrimSpace(input.ActorID) &&
		artifact.PreviewClass == types.WorkbenchPreviewClassPresentationPage && strings.ToLower(path.Ext(artifact.FileName)) == ".pptx"
}

func workbenchSkillPayloadString(payload types.JSONMap, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
