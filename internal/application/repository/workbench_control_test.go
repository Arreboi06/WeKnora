package repository

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestT2L01WorkbenchControlRepositoryRejectsSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:t2l01-workbench-control?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	repo := NewWorkbenchControlRepository(db)

	err = repo.ProbeSchema(context.Background())
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)

	_, err = repo.StartJobWithAudit(context.Background(), t2l01JobStartInput(1, "workbench-a", 0, strings.Repeat("a", 64)))
	require.ErrorIs(t, err, ErrWorkbenchUnsupportedDatabase)
}

func TestT2L01RealPostgresWorkbenchControlRepositoryBehavior(t *testing.T) {
	if os.Getenv("WEKNORA_T2_M01A_POSTGRES") != "1" {
		return
	}
	ctx := context.Background()
	emptyDB := t2m01aPostgresRepositoryDB(t, false)
	err := NewWorkbenchControlRepository(emptyDB).ProbeSchema(ctx)
	require.ErrorIs(t, err, ErrWorkbenchMigrationUnavailable)

	db := t2m01aPostgresRepositoryDB(t, true)
	sessionRepo := NewWorkbenchSessionRepository(db)
	controlRepo := NewWorkbenchControlRepository(db)
	require.NoError(t, controlRepo.ProbeSchema(ctx))

	t2m01aInsertChatSession(t, db, 71, "chat-t2l01")
	workbench, err := sessionRepo.CreateOrGet(ctx, t2m01aWorkbenchRecord(71, "chat-t2l01", "incarnation-t2l01"))
	require.NoError(t, err)
	workbench, err = sessionRepo.CompareAndSwapState(ctx, WorkbenchSessionStateCAS{
		TenantID:             71,
		ID:                   workbench.ID,
		ExpectedStateVersion: 0,
		ExpectedState:        types.WorkbenchStateProvisioning,
		NextState:            types.WorkbenchStateReady,
	})
	require.NoError(t, err)
	require.Equal(t, int64(0), workbench.LeaseEpoch)

	created, err := controlRepo.StartJobWithAudit(ctx, t2l01JobStartInput(71, workbench.ID, 0, strings.Repeat("a", 64)))
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, types.WorkbenchJobStateQueued, created.State)
	require.Equal(t, int64(0), created.StateVersion)
	require.Equal(t, strings.Repeat("a", 64), created.StartNonceHash)
	require.Equal(t, "chat-t2l01", created.ChatSessionID)
	require.Equal(t, "incarnation-t2l01", created.IncarnationID)

	outbox, err := controlRepo.ListAuditOutbox(ctx, 71, types.WorkbenchAuditOutboxStatePending, 10)
	require.NoError(t, err)
	require.Len(t, outbox, 1)
	require.Equal(t, types.WorkbenchAuditActionJobStarted, outbox[0].Action)
	require.Equal(t, created.ID, outbox[0].WorkbenchJobID)

	retried, err := controlRepo.StartJobWithAudit(ctx, t2l01JobStartInput(71, workbench.ID, 0, strings.Repeat("a", 64)))
	require.NoError(t, err)
	require.Equal(t, created.ID, retried.ID)

	staleInput := t2l01JobStartInput(71, workbench.ID, 99, strings.Repeat("b", 64))
	staleInput.ID = "job-stale-epoch"
	_, err = controlRepo.StartJobWithAudit(ctx, staleInput)
	require.ErrorIs(t, err, ErrWorkbenchStaleEpoch)
	_, err = controlRepo.GetJobByID(ctx, 71, "job-stale-epoch")
	require.ErrorIs(t, err, ErrWorkbenchJobNotFound)

	require.NoError(t, db.Exec(`INSERT INTO workbench_audit_outbox (id, tenant_id, workbench_session_id, workbench_job_id, action, actor_user_id, outcome, payload, idempotency_key) VALUES ('audit-preexisting', 71, ?, 'job-audit-fail', 'workbench.job.started', 'actor-a', 'success', '{}'::jsonb, 'workbench.job.started:job-audit-fail')`, workbench.ID).Error)
	rollbackInput := t2l01JobStartInput(71, workbench.ID, 0, strings.Repeat("c", 64))
	rollbackInput.ID = "job-audit-fail"
	_, err = controlRepo.StartJobWithAudit(ctx, rollbackInput)
	require.ErrorIs(t, err, ErrWorkbenchAuditOutboxConflict)
	_, err = controlRepo.GetJobByID(ctx, 71, "job-audit-fail")
	require.ErrorIs(t, err, ErrWorkbenchJobNotFound)
	retryInput := t2l01JobStartInput(71, workbench.ID, 0, strings.Repeat("c", 64))
	retryInput.ID = "job-audit-retry"
	_, err = controlRepo.StartJobWithAudit(ctx, retryInput)
	require.NoError(t, err, "audit rollback must not consume the start nonce")

	runningJob, err := controlRepo.CompareAndSwapJobState(ctx, WorkbenchJobStateCAS{
		TenantID:             71,
		ID:                   created.ID,
		ExpectedStateVersion: 0,
		ExpectedState:        types.WorkbenchJobStateQueued,
		NextState:            types.WorkbenchJobStateRunning,
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchJobStateRunning, runningJob.State)
	require.Equal(t, int64(1), runningJob.StateVersion)
	succeededJob, err := controlRepo.CompareAndSwapJobState(ctx, WorkbenchJobStateCAS{
		TenantID:             71,
		ID:                   created.ID,
		ExpectedStateVersion: 1,
		ExpectedState:        types.WorkbenchJobStateRunning,
		NextState:            types.WorkbenchJobStateSucceeded,
		TerminalReason:       "exit_0",
	})
	require.NoError(t, err)
	require.NotNil(t, succeededJob.ClosedAt)
	require.Equal(t, "exit_0", succeededJob.TerminalReason)
	_, err = controlRepo.CompareAndSwapJobState(ctx, WorkbenchJobStateCAS{
		TenantID:             71,
		ID:                   created.ID,
		ExpectedStateVersion: 2,
		NextState:            types.WorkbenchJobStateRunning,
	})
	require.ErrorIs(t, err, ErrWorkbenchJobStaleVersion)

	commandJob, err := controlRepo.StartJobWithAudit(ctx, t2l01JobStartInput(71, workbench.ID, 0, strings.Repeat("d", 64)))
	require.NoError(t, err)
	_, err = controlRepo.GetJobByID(ctx, 72, commandJob.ID)
	require.ErrorIs(t, err, ErrWorkbenchJobNotFound)

	startingCommandJob, err := controlRepo.CompareAndSwapJobState(ctx, WorkbenchJobStateCAS{
		TenantID:             71,
		ID:                   commandJob.ID,
		ExpectedStateVersion: 0,
		ExpectedState:        types.WorkbenchJobStateQueued,
		NextState:            types.WorkbenchJobStateStarting,
	})
	require.NoError(t, err)
	boundCommandJob, err := controlRepo.BindJobBackendAndMarkRunning(ctx, WorkbenchJobBackendBindCAS{
		TenantID:             71,
		ID:                   commandJob.ID,
		ExpectedStateVersion: startingCommandJob.StateVersion,
		ExpectedState:        types.WorkbenchJobStateStarting,
		BackendIdentity:      "container-command",
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchJobStateRunning, boundCommandJob.State)
	require.Equal(t, "container-command", boundCommandJob.BackendIdentity)
	_, err = controlRepo.BindJobBackendAndMarkRunning(ctx, WorkbenchJobBackendBindCAS{
		TenantID:             72,
		ID:                   commandJob.ID,
		ExpectedStateVersion: startingCommandJob.StateVersion,
		ExpectedState:        types.WorkbenchJobStateStarting,
		BackendIdentity:      "container-cross-tenant",
	})
	require.ErrorIs(t, err, ErrWorkbenchJobStaleVersion)

	cmd := t2l01CommandRecord(71, commandJob.ID, workbench.ID, 1)
	createdCommand, err := controlRepo.CreateCommand(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchCommandStateQueued, createdCommand.State)

	_, err = controlRepo.CreateCommand(ctx, t2l01CommandRecordWithID(71, "command-duplicate-sequence", commandJob.ID, workbench.ID, 1))
	require.ErrorIs(t, err, ErrWorkbenchCommandSequenceConflict)

	running, err := controlRepo.CompareAndSwapCommandState(ctx, WorkbenchCommandStateCAS{
		TenantID:             71,
		ID:                   createdCommand.ID,
		ExpectedStateVersion: 0,
		ExpectedState:        types.WorkbenchCommandStateQueued,
		NextState:            types.WorkbenchCommandStateRunning,
	})
	require.NoError(t, err)
	require.Equal(t, types.WorkbenchCommandStateRunning, running.State)
	require.Equal(t, int64(1), running.StateVersion)

	done, err := controlRepo.CompareAndSwapCommandState(ctx, WorkbenchCommandStateCAS{
		TenantID:             71,
		ID:                   createdCommand.ID,
		ExpectedStateVersion: 1,
		ExpectedState:        types.WorkbenchCommandStateRunning,
		NextState:            types.WorkbenchCommandStateSucceeded,
		TerminalReason:       "exit_0",
	})
	require.NoError(t, err)
	require.NotNil(t, done.ClosedAt)
	require.Equal(t, "exit_0", done.TerminalReason)

	_, err = controlRepo.CompareAndSwapCommandState(ctx, WorkbenchCommandStateCAS{
		TenantID:             71,
		ID:                   createdCommand.ID,
		ExpectedStateVersion: 2,
		NextState:            types.WorkbenchCommandStateRunning,
	})
	require.ErrorIs(t, err, ErrWorkbenchCommandStaleVersion)

	event, err := controlRepo.AppendRunnerEvent(ctx, &types.WorkbenchRunnerEvent{
		TenantID:           71,
		WorkbenchJobID:     commandJob.ID,
		WorkbenchSessionID: workbench.ID,
		CommandID:          createdCommand.ID,
		Seq:                1,
		EventType:          "stdout",
		Payload:            types.JSONMap{"chunk": "ok"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, event.ID)

	_, err = controlRepo.AppendRunnerEvent(ctx, &types.WorkbenchRunnerEvent{
		TenantID:           71,
		WorkbenchJobID:     commandJob.ID,
		WorkbenchSessionID: workbench.ID,
		CommandID:          createdCommand.ID,
		Seq:                1,
		EventType:          "stdout",
		Payload:            types.JSONMap{"chunk": "dup"},
	})
	require.ErrorIs(t, err, ErrWorkbenchRunnerEventConflict)

	events, err := controlRepo.ListRunnerEvents(ctx, 71, commandJob.ID, 0, 20)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, event.ID, events[0].ID)
	events, err = controlRepo.ListRunnerEvents(ctx, 72, commandJob.ID, 0, 20)
	require.NoError(t, err)
	require.Empty(t, events)

	audit, err := controlRepo.EnqueueAuditOutbox(ctx, &types.WorkbenchAuditOutbox{
		TenantID:           71,
		WorkbenchSessionID: workbench.ID,
		WorkbenchJobID:     commandJob.ID,
		CommandID:          createdCommand.ID,
		Action:             types.WorkbenchAuditActionCommandSucceeded,
		ActorUserID:        "actor-a",
		Outcome:            "success",
		Payload:            types.JSONMap{"exit_code": float64(0)},
		IdempotencyKey:     "command-succeeded:" + createdCommand.ID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, audit.ID)
	require.Equal(t, types.WorkbenchAuditOutboxStatePending, audit.State)

	duplicateAudit, err := controlRepo.EnqueueAuditOutbox(ctx, &types.WorkbenchAuditOutbox{
		TenantID:           71,
		WorkbenchSessionID: workbench.ID,
		WorkbenchJobID:     commandJob.ID,
		CommandID:          createdCommand.ID,
		Action:             types.WorkbenchAuditActionCommandSucceeded,
		ActorUserID:        "actor-a",
		Outcome:            "success",
		Payload:            types.JSONMap{"exit_code": float64(0)},
		IdempotencyKey:     "command-succeeded:" + createdCommand.ID,
	})
	require.NoError(t, err)
	require.Equal(t, audit.ID, duplicateAudit.ID)

	outbox, err = controlRepo.ListAuditOutbox(ctx, 71, types.WorkbenchAuditOutboxStatePending, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(outbox), 4)
	outbox, err = controlRepo.ListAuditOutbox(ctx, 72, types.WorkbenchAuditOutboxStatePending, 10)
	require.NoError(t, err)
	require.Empty(t, outbox)
	jobOutbox, err := controlRepo.ListAuditOutboxForJob(ctx, 71, commandJob.ID, types.WorkbenchAuditOutboxStatePending, 10)
	require.NoError(t, err)
	require.NotEmpty(t, jobOutbox)
	for _, row := range jobOutbox {
		require.Equal(t, commandJob.ID, row.WorkbenchJobID)
	}
	jobOutbox, err = controlRepo.ListAuditOutboxForJob(ctx, 72, commandJob.ID, types.WorkbenchAuditOutboxStatePending, 10)
	require.NoError(t, err)
	require.Empty(t, jobOutbox)

	var currentEpoch int64
	require.NoError(t, db.Raw("SELECT lease_epoch FROM workbench_sessions WHERE id = ?", workbench.ID).Scan(&currentEpoch).Error)
	require.NoError(t, db.Exec("UPDATE workbench_sessions SET lease_epoch = lease_epoch + 1, state_version = state_version + 1 WHERE id = ?", workbench.ID).Error)
	_, err = controlRepo.GetActiveJobForSession(ctx, 71, workbench.ChatSessionID, workbench.ID, commandJob.ID, currentEpoch)
	require.Error(t, err, "a job from the prior lease epoch must not remain active")
}

func t2l01JobStartInput(tenantID uint64, workbenchID string, epoch int64, nonceHash string) WorkbenchJobStartInput {
	return WorkbenchJobStartInput{
		TenantID:               tenantID,
		WorkbenchSessionID:     workbenchID,
		ExpectedLeaseEpoch:     epoch,
		StartNonceHash:         nonceHash,
		BackendType:            "docker",
		BackendIdentity:        "docker://local/t2l01",
		ResourcePolicySnapshot: types.JSONMap{"cpu_seconds": float64(30)},
		CreatedBy:              "actor-a",
	}
}

func t2l01CommandRecord(tenantID uint64, jobID, workbenchID string, sequence int64) *types.WorkbenchCommand {
	return t2l01CommandRecordWithID(tenantID, "", jobID, workbenchID, sequence)
}

func t2l01CommandRecordWithID(tenantID uint64, id, jobID, workbenchID string, sequence int64) *types.WorkbenchCommand {
	return &types.WorkbenchCommand{
		ID:                 id,
		TenantID:           tenantID,
		WorkbenchJobID:     jobID,
		WorkbenchSessionID: workbenchID,
		Sequence:           sequence,
		Kind:               "exec",
		Payload:            types.JSONMap{"argv": []any{"echo", "ok"}},
		State:              types.WorkbenchCommandStateQueued,
		StateVersion:       0,
		CreatedBy:          "actor-a",
	}
}
