package repository

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestT2L06WorkbenchSkillRunRepositoryRejectsSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:t2l06-workbench-skill?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	repo := NewWorkbenchSkillRunRepository(db)

	err = repo.ProbeSchema(context.Background())
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)

	_, err = repo.CreateSkillRun(context.Background(), t2l06SkillRunInput(10, "chat-1", "wb-1", "job-1", "cmd-1"))
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)

	_, err = repo.GetSkillRun(context.Background(), 10, "chat-1", "skill-run-1")
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)

	_, err = repo.GetSkillRunByCommand(context.Background(), 10, "chat-1", "cmd-1")
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)
}

func TestT2L06RealPostgresWorkbenchSkillRunRepositoryBehavior(t *testing.T) {
	if os.Getenv("WEKNORA_T2_M01A_POSTGRES") != "1" {
		return
	}
	ctx := context.Background()
	emptyDB := t2m01aPostgresRepositoryDB(t, false)
	err := NewWorkbenchSkillRunRepository(emptyDB).ProbeSchema(ctx)
	require.ErrorIs(t, err, ErrWorkbenchMigrationUnavailable)

	db := t2m01aPostgresRepositoryDB(t, true)
	sessionRepo := NewWorkbenchSessionRepository(db)
	controlRepo := NewWorkbenchControlRepository(db)
	skillRepo := NewWorkbenchSkillRunRepository(db)
	require.NoError(t, skillRepo.ProbeSchema(ctx))

	t2m01aInsertChatSession(t, db, 76, "chat-t2l06")
	workbench, err := sessionRepo.CreateOrGet(ctx, t2m01aWorkbenchRecord(76, "chat-t2l06", "incarnation-t2l06"))
	require.NoError(t, err)
	workbench, err = sessionRepo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID: 76, ID: workbench.ID, ExpectedStateVersion: 0,
		ExpectedState: types.WorkbenchStateProvisioning, NextState: types.WorkbenchStateReady,
	})
	require.NoError(t, err)
	job, err := controlRepo.StartJobWithAudit(ctx, t2l01JobStartInput(76, workbench.ID, workbench.LeaseEpoch, repeatHex("f")))
	require.NoError(t, err)
	job, err = controlRepo.CompareAndSwapJobState(ctx, WorkbenchJobStateCAS{
		TenantID: 76, ID: job.ID, ExpectedStateVersion: job.StateVersion,
		ExpectedState: types.WorkbenchJobStateQueued, NextState: types.WorkbenchJobStateStarting,
	})
	require.NoError(t, err)
	job, err = controlRepo.BindJobBackendAndMarkRunning(ctx, WorkbenchJobBackendBindCAS{
		TenantID: 76, ID: job.ID, ExpectedStateVersion: job.StateVersion,
		ExpectedState: types.WorkbenchJobStateStarting, BackendIdentity: "docker://local/t2l06",
	})
	require.NoError(t, err)
	command, err := controlRepo.CreateCommand(ctx, &types.WorkbenchCommand{
		TenantID: 76, WorkbenchJobID: job.ID, WorkbenchSessionID: workbench.ID,
		Sequence: 1, Kind: "shell", Payload: types.JSONMap{"command": "generate", "skill_name": "presentations", "skill_operation": "create_presentation"}, State: types.WorkbenchCommandStateQueued, StateVersion: 0, CreatedBy: "actor-a",
	})
	require.NoError(t, err)

	run, err := skillRepo.CreateSkillRun(ctx, t2l06SkillRunInput(76, "chat-t2l06", workbench.ID, job.ID, command.ID))
	require.NoError(t, err)
	require.NotEmpty(t, run.ID)
	require.Equal(t, types.WorkbenchSkillRunStateQueued, run.State)
	require.Equal(t, "presentations", run.SkillName)
	require.Equal(t, types.WorkbenchFileRootOutput, run.OutputFileRef.Root)

	_, err = skillRepo.CreateSkillRun(ctx, t2l06SkillRunInput(76, "chat-t2l06", workbench.ID, job.ID, command.ID))
	require.ErrorIs(t, err, ErrWorkbenchSkillRunConflict)

	loadedByCommand, err := skillRepo.GetSkillRunByCommand(ctx, 76, "chat-t2l06", command.ID)
	require.NoError(t, err)
	require.Equal(t, run.ID, loadedByCommand.ID)
	_, err = skillRepo.GetSkillRunByCommand(ctx, 77, "chat-t2l06", command.ID)
	require.ErrorIs(t, err, ErrWorkbenchSkillRunNotFound)

	completed, err := skillRepo.CompleteSkillRun(ctx, WorkbenchSkillRunCompleteCAS{
		TenantID: 76, ID: run.ID, ExpectedStateVersion: run.StateVersion, ExpectedState: types.WorkbenchSkillRunStateQueued,
		NextState: types.WorkbenchSkillRunStateSucceeded, OutputArtifactID: "artifact-t2l06", OutputArtifactVersion: 1,
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchSkillRunStateSucceeded, completed.State)
	require.Equal(t, "artifact-t2l06", completed.OutputArtifactID)
	require.NotNil(t, completed.ClosedAt)

	_, err = skillRepo.CompleteSkillRun(ctx, WorkbenchSkillRunCompleteCAS{
		TenantID: 76, ID: run.ID, ExpectedStateVersion: run.StateVersion, ExpectedState: types.WorkbenchSkillRunStateQueued,
		NextState: types.WorkbenchSkillRunStateFailed, OutputArtifactID: "other", OutputArtifactVersion: 1,
	})
	require.ErrorIs(t, err, ErrWorkbenchSkillRunStaleVersion)

	loaded, err := skillRepo.GetSkillRun(ctx, 76, "chat-t2l06", run.ID)
	require.NoError(t, err)
	require.Equal(t, completed.OutputArtifactID, loaded.OutputArtifactID)
	_, err = skillRepo.GetSkillRun(ctx, 77, "chat-t2l06", run.ID)
	require.ErrorIs(t, err, ErrWorkbenchSkillRunNotFound)
}

func t2l06SkillRunInput(tenantID uint64, chatID, workbenchID, jobID, commandID string) WorkbenchSkillRunInput {
	return WorkbenchSkillRunInput{
		TenantID: tenantID, ChatSessionID: chatID, WorkbenchSessionID: workbenchID, WorkbenchJobID: jobID, CommandID: commandID,
		SkillName: "presentations", SkillOperation: "create_presentation",
		OutputFileRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"slides.pptx"}},
		CreatedBy:     "actor-a", CreatedAt: time.Now().UTC(),
	}
}
