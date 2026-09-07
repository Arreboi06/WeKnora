package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestT2L05WorkbenchArtifactRepositoryRejectsSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:t2l05-workbench-artifact?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	repo := NewWorkbenchArtifactRepository(db)

	err = repo.ProbeSchema(context.Background())
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)

	_, err = repo.CreateArtifactVersion(context.Background(), t2l05ArtifactInput(1, "chat-1", "wb-1", "job-1", "artifact-1", 1, []byte("x")))
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)

	_, err = repo.GetArtifactVersion(context.Background(), 1, "chat-1", "artifact-1", 1)
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)
}

func TestT2L05RealPostgresWorkbenchArtifactRepositoryBehavior(t *testing.T) {
	if os.Getenv("WEKNORA_T2_M01A_POSTGRES") != "1" {
		return
	}
	ctx := context.Background()
	emptyDB := t2m01aPostgresRepositoryDB(t, false)
	err := NewWorkbenchArtifactRepository(emptyDB).ProbeSchema(ctx)
	require.ErrorIs(t, err, ErrWorkbenchMigrationUnavailable)

	db := t2m01aPostgresRepositoryDB(t, true)
	sessionRepo := NewWorkbenchSessionRepository(db)
	controlRepo := NewWorkbenchControlRepository(db)
	artifactRepo := NewWorkbenchArtifactRepository(db)
	require.NoError(t, artifactRepo.ProbeSchema(ctx))

	t2m01aInsertChatSession(t, db, 75, "chat-t2l05")
	workbench, err := sessionRepo.CreateOrGet(ctx, t2m01aWorkbenchRecord(75, "chat-t2l05", "incarnation-t2l05"))
	require.NoError(t, err)
	workbench, err = sessionRepo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID: 75, ID: workbench.ID, ExpectedStateVersion: 0,
		ExpectedState: types.WorkbenchStateProvisioning, NextState: types.WorkbenchStateReady,
	})
	require.NoError(t, err)
	job, err := controlRepo.StartJobWithAudit(ctx, t2l01JobStartInput(75, workbench.ID, workbench.LeaseEpoch, repeatHex("e")))
	require.NoError(t, err)
	job, err = controlRepo.CompareAndSwapJobState(ctx, WorkbenchJobStateCAS{
		TenantID: 75, ID: job.ID, ExpectedStateVersion: job.StateVersion,
		ExpectedState: types.WorkbenchJobStateQueued, NextState: types.WorkbenchJobStateStarting,
	})
	require.NoError(t, err)
	job, err = controlRepo.BindJobBackendAndMarkRunning(ctx, WorkbenchJobBackendBindCAS{
		TenantID: 75, ID: job.ID, ExpectedStateVersion: job.StateVersion,
		ExpectedState: types.WorkbenchJobStateStarting, BackendIdentity: "docker://local/t2l05",
	})
	require.NoError(t, err)
	command, err := controlRepo.CreateCommand(ctx, &types.WorkbenchCommand{
		TenantID: 75, WorkbenchJobID: job.ID, WorkbenchSessionID: workbench.ID,
		Sequence: 1, Kind: "shell", Payload: types.JSONMap{"command": "printf ok"}, State: types.WorkbenchCommandStateQueued, StateVersion: 0, CreatedBy: "actor-a",
	})
	require.NoError(t, err)

	content := []byte("a,b\n1,2\n")
	input := t2l05ArtifactInput(75, "chat-t2l05", workbench.ID, job.ID, "artifact-t2l05", 1, content)
	input.CommandID = command.ID
	created, err := artifactRepo.CreateArtifactVersion(ctx, input)
	require.NoError(t, err)
	require.Equal(t, "artifact-t2l05", created.ArtifactID)
	require.Equal(t, 1, created.Version)
	require.Equal(t, int64(len(content)), created.SizeBytes)
	require.Equal(t, shaHex(content), created.ContentSHA256)
	require.Equal(t, types.WorkbenchPreviewClassTableCSV, created.PreviewClass)
	require.Equal(t, types.WorkbenchFileRootOutput, created.SourceFileRef.Root)

	_, err = artifactRepo.CreateArtifactVersion(ctx, input)
	require.ErrorIs(t, err, ErrWorkbenchArtifactConflict)

	loaded, err := artifactRepo.GetArtifactVersion(ctx, 75, "chat-t2l05", "artifact-t2l05", 1)
	require.NoError(t, err)
	require.Equal(t, content, loaded.Content)

	closed, err := sessionRepo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID: 75, ID: workbench.ID, ExpectedStateVersion: workbench.StateVersion,
		ExpectedState: types.WorkbenchStateReady, NextState: types.WorkbenchStateClosed,
		TerminalReason: "replaced",
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchStateClosed, closed.State)
	_, err = artifactRepo.GetArtifactVersion(ctx, 75, "chat-t2l05", "artifact-t2l05", 1)
	require.ErrorIs(t, err, ErrWorkbenchArtifactNotFound, "artifacts from a closed incarnation must be hidden")
	_, err = artifactRepo.GetArtifactVersion(ctx, 76, "chat-t2l05", "artifact-t2l05", 1)
	require.ErrorIs(t, err, ErrWorkbenchArtifactNotFound)
	_, err = artifactRepo.GetArtifactVersion(ctx, 75, "chat-other", "artifact-t2l05", 1)
	require.ErrorIs(t, err, ErrWorkbenchArtifactNotFound)
}

func t2l05ArtifactInput(tenantID uint64, chatID, workbenchID, jobID, artifactID string, version int, content []byte) WorkbenchArtifactVersionInput {
	return WorkbenchArtifactVersionInput{
		ArtifactID: artifactID, Version: version, TenantID: tenantID, ChatSessionID: chatID,
		WorkbenchSessionID: workbenchID, WorkbenchJobID: jobID, CommandID: "cmd-1",
		SourceFileRef: types.WorkbenchFileRef{FileRefVersion: 1, Root: types.WorkbenchFileRootOutput, Segments: []string{"report.csv"}},
		ContentSHA256: shaHex(content), SizeBytes: int64(len(content)), MimeType: "text/csv; charset=utf-8",
		PreviewClass: types.WorkbenchPreviewClassTableCSV, FileName: "report.csv", Content: content,
		CreatedBy: "actor-a",
	}
}

func shaHex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func repeatHex(s string) string {
	return strings.Repeat(s, 64)
}
