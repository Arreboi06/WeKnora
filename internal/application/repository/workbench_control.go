package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrWorkbenchStaleEpoch              = errors.New("workbench lease epoch mismatch")
	ErrWorkbenchJobNotFound             = errors.New("workbench job not found")
	ErrWorkbenchJobConflict             = errors.New("workbench job conflict")
	ErrWorkbenchStartNonceConsumed      = errors.New("workbench start nonce already consumed")
	ErrWorkbenchJobStaleVersion         = errors.New("workbench job state version mismatch or missing")
	ErrWorkbenchCommandNotFound         = errors.New("workbench command not found")
	ErrWorkbenchCommandSequenceConflict = errors.New("workbench command sequence conflict")
	ErrWorkbenchCommandStaleVersion     = errors.New("workbench command state version mismatch or missing")
	ErrWorkbenchRunnerEventConflict     = errors.New("workbench runner event sequence conflict")
	ErrWorkbenchAuditOutboxConflict     = errors.New("workbench audit outbox conflict")
)

type WorkbenchControlRepository interface {
	ProbeSchema(ctx context.Context) error
	StartJobWithAudit(ctx context.Context, input WorkbenchJobStartInput) (*types.WorkbenchJob, error)
	GetJobByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error)
	CompareAndSwapJobState(ctx context.Context, input WorkbenchJobStateCAS) (*types.WorkbenchJob, error)
	BindJobBackendAndMarkRunning(ctx context.Context, input WorkbenchJobBackendBindCAS) (*types.WorkbenchJob, error)
	CreateCommand(ctx context.Context, command *types.WorkbenchCommand) (*types.WorkbenchCommand, error)
	CreateCommandIfJobRunning(ctx context.Context, input WorkbenchCommandCreateInput) (*types.WorkbenchCommand, error)
	GetActiveJobForSession(ctx context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error)
	GetCommandByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error)
	CompareAndSwapCommandState(ctx context.Context, input WorkbenchCommandStateCAS) (*types.WorkbenchCommand, error)
	AppendRunnerEvent(ctx context.Context, event *types.WorkbenchRunnerEvent) (*types.WorkbenchRunnerEvent, error)
	ListRunnerEvents(ctx context.Context, tenantID uint64, jobID string, afterSeq int64, limit int) ([]*types.WorkbenchRunnerEvent, error)
	EnqueueAuditOutbox(ctx context.Context, row *types.WorkbenchAuditOutbox) (*types.WorkbenchAuditOutbox, error)
	ListAuditOutbox(ctx context.Context, tenantID uint64, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error)
	ListAuditOutboxForJob(ctx context.Context, tenantID uint64, jobID string, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error)
}

type WorkbenchCommandCreateInput struct {
	Command                 *types.WorkbenchCommand
	ExpectedLeaseEpoch      int64
	ExpectedJobStateVersion int64
}
type WorkbenchJobStartInput struct {
	ID                     string
	TenantID               uint64
	WorkbenchSessionID     string
	ExpectedLeaseEpoch     int64
	StartNonceHash         string
	BackendType            string
	BackendIdentity        string
	ResourcePolicySnapshot types.JSONMap
	CreatedBy              string
}
type WorkbenchJobStateCAS struct {
	TenantID             uint64
	ID                   string
	ExpectedStateVersion int64
	ExpectedState        types.WorkbenchJobState
	NextState            types.WorkbenchJobState
	TerminalReason       string
}

type WorkbenchJobBackendBindCAS struct {
	TenantID             uint64
	ID                   string
	ExpectedStateVersion int64
	ExpectedState        types.WorkbenchJobState
	BackendIdentity      string
}
type WorkbenchCommandStateCAS struct {
	TenantID             uint64
	ID                   string
	ExpectedStateVersion int64
	ExpectedState        types.WorkbenchCommandState
	NextState            types.WorkbenchCommandState
	TerminalReason       string
}

type gormWorkbenchControlRepository struct {
	db *gorm.DB
}

func NewWorkbenchControlRepository(db *gorm.DB) WorkbenchControlRepository {
	return &gormWorkbenchControlRepository{db: db}
}

func (r *gormWorkbenchControlRepository) requirePostgres() error {
	if r == nil || r.db == nil || r.db.Dialector == nil || r.db.Dialector.Name() != "postgres" {
		return ErrWorkbenchUnsupportedDatabase
	}
	return nil
}

func (r *gormWorkbenchControlRepository) ProbeSchema(ctx context.Context) error {
	if err := r.requirePostgres(); err != nil {
		return err
	}
	tables := []string{"workbench_jobs", "workbench_commands", "workbench_runner_events", "workbench_audit_outbox"}
	for _, table := range tables {
		var exists bool
		if err := r.db.WithContext(ctx).Raw("SELECT to_regclass(?) IS NOT NULL", "public."+table).Scan(&exists).Error; err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: missing %s", ErrWorkbenchMigrationUnavailable, table)
		}
	}
	return nil
}

func (r *gormWorkbenchControlRepository) StartJobWithAudit(ctx context.Context, input WorkbenchJobStartInput) (*types.WorkbenchJob, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if err := validateWorkbenchJobStart(input); err != nil {
		return nil, err
	}

	var created *types.WorkbenchJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session types.WorkbenchSession
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND id = ?", input.TenantID, strings.TrimSpace(input.WorkbenchSessionID)).
			First(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrWorkbenchSessionNotFound
		}
		if err != nil {
			return err
		}
		if session.State != types.WorkbenchStateReady || session.ClosedAt != nil {
			return ErrWorkbenchSessionConflict
		}
		if session.LeaseEpoch != input.ExpectedLeaseEpoch {
			return ErrWorkbenchStaleEpoch
		}

		jobID := strings.TrimSpace(input.ID)
		if jobID == "" {
			jobID = uuid.NewString()
		}
		job := &types.WorkbenchJob{
			ID:                     jobID,
			TenantID:               input.TenantID,
			WorkbenchSessionID:     session.ID,
			ChatSessionID:          session.ChatSessionID,
			IncarnationID:          session.IncarnationID,
			LeaseEpoch:             session.LeaseEpoch,
			StartNonceHash:         strings.TrimSpace(input.StartNonceHash),
			BackendType:            strings.TrimSpace(input.BackendType),
			BackendIdentity:        strings.TrimSpace(input.BackendIdentity),
			State:                  types.WorkbenchJobStateQueued,
			StateVersion:           0,
			ResourcePolicySnapshot: input.ResourcePolicySnapshot,
			CreatedBy:              strings.TrimSpace(input.CreatedBy),
		}
		createResult := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(job)
		if createResult.Error != nil {
			return createResult.Error
		}
		if createResult.RowsAffected == 0 {
			var existing types.WorkbenchJob
			lookupErr := tx.Where(
				"tenant_id = ? AND workbench_session_id = ? AND lease_epoch = ? AND start_nonce_hash = ?",
				input.TenantID, session.ID, session.LeaseEpoch, strings.TrimSpace(input.StartNonceHash),
			).First(&existing).Error
			if lookupErr == nil {
				created = &existing
				return nil
			}
			if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return lookupErr
			}
			return ErrWorkbenchJobConflict
		}

		audit := &types.WorkbenchAuditOutbox{
			TenantID:           input.TenantID,
			WorkbenchSessionID: session.ID,
			WorkbenchJobID:     job.ID,
			Action:             types.WorkbenchAuditActionJobStarted,
			ActorUserID:        strings.TrimSpace(input.CreatedBy),
			Outcome:            "success",
			Payload: types.JSONMap{
				"backend_type":    strings.TrimSpace(input.BackendType),
				"lease_epoch":     float64(session.LeaseEpoch),
				"incarnation_id":  session.IncarnationID,
				"chat_session_id": session.ChatSessionID,
			},
			IdempotencyKey: "workbench.job.started:" + job.ID,
			State:          types.WorkbenchAuditOutboxStatePending,
		}
		if err := tx.Create(audit).Error; err != nil {
			if isWorkbenchDuplicateKey(err) {
				return ErrWorkbenchAuditOutboxConflict
			}
			return err
		}
		created = job
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (r *gormWorkbenchControlRepository) GetJobByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchJob, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(id) == "" {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	var job types.WorkbenchJob
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchJobNotFound
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *gormWorkbenchControlRepository) CompareAndSwapJobState(ctx context.Context, input WorkbenchJobStateCAS) (*types.WorkbenchJob, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if input.TenantID == 0 || strings.TrimSpace(input.ID) == "" || input.ExpectedStateVersion < 0 || !input.NextState.IsValid() {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	if input.ExpectedState != "" && !input.ExpectedState.IsValid() {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	updates := map[string]any{
		"state":         string(input.NextState),
		"state_version": gorm.Expr("state_version + 1"),
		"updated_at":    time.Now().UTC(),
	}
	if input.NextState.IsTerminal() {
		updates["closed_at"] = time.Now().UTC()
		updates["terminal_reason"] = strings.TrimSpace(input.TerminalReason)
	}
	query := r.db.WithContext(ctx).Model(&types.WorkbenchJob{}).
		Where("tenant_id = ? AND id = ? AND state_version = ? AND closed_at IS NULL", input.TenantID, input.ID, input.ExpectedStateVersion)
	if input.ExpectedState != "" {
		query = query.Where("state = ?", string(input.ExpectedState))
	}
	res := query.Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrWorkbenchJobStaleVersion
	}
	return r.GetJobByID(ctx, input.TenantID, input.ID)
}

func (r *gormWorkbenchControlRepository) BindJobBackendAndMarkRunning(ctx context.Context, input WorkbenchJobBackendBindCAS) (*types.WorkbenchJob, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if input.TenantID == 0 || strings.TrimSpace(input.ID) == "" || input.ExpectedStateVersion < 0 ||
		input.ExpectedState != types.WorkbenchJobStateStarting || strings.TrimSpace(input.BackendIdentity) == "" {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	res := r.db.WithContext(ctx).Model(&types.WorkbenchJob{}).
		Where(
			"tenant_id = ? AND id = ? AND state_version = ? AND state = ? AND closed_at IS NULL",
			input.TenantID, input.ID, input.ExpectedStateVersion, string(input.ExpectedState),
		).
		Updates(map[string]any{
			"backend_identity": strings.TrimSpace(input.BackendIdentity),
			"state":            string(types.WorkbenchJobStateRunning),
			"state_version":    gorm.Expr("state_version + 1"),
			"updated_at":       time.Now().UTC(),
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrWorkbenchJobStaleVersion
	}
	return r.GetJobByID(ctx, input.TenantID, input.ID)
}
func (r *gormWorkbenchControlRepository) CreateCommand(ctx context.Context, command *types.WorkbenchCommand) (*types.WorkbenchCommand, error) {
	return r.createCommandWithJobFence(ctx, WorkbenchCommandCreateInput{
		Command:                 command,
		ExpectedLeaseEpoch:      -1,
		ExpectedJobStateVersion: -1,
	})
}

func (r *gormWorkbenchControlRepository) CreateCommandIfJobRunning(ctx context.Context, input WorkbenchCommandCreateInput) (*types.WorkbenchCommand, error) {
	if input.ExpectedLeaseEpoch < 0 || input.ExpectedJobStateVersion < 0 {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	return r.createCommandWithJobFence(ctx, input)
}

func (r *gormWorkbenchControlRepository) createCommandWithJobFence(ctx context.Context, input WorkbenchCommandCreateInput) (*types.WorkbenchCommand, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if err := validateWorkbenchCommandForWrite(input.Command); err != nil {
		return nil, err
	}
	var created *types.WorkbenchCommand
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session types.WorkbenchSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND id = ?", input.Command.TenantID, strings.TrimSpace(input.Command.WorkbenchSessionID)).
			First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWorkbenchJobNotFound
			}
			return err
		}
		if session.State != types.WorkbenchStateReady || session.ClosedAt != nil {
			return ErrWorkbenchJobConflict
		}
		if input.ExpectedLeaseEpoch >= 0 && session.LeaseEpoch != input.ExpectedLeaseEpoch {
			return ErrWorkbenchStaleEpoch
		}

		var job types.WorkbenchJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND id = ?", input.Command.TenantID, strings.TrimSpace(input.Command.WorkbenchJobID)).
			First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWorkbenchJobNotFound
			}
			return err
		}
		if job.WorkbenchSessionID != session.ID || job.ChatSessionID != session.ChatSessionID ||
			job.IncarnationID != session.IncarnationID || job.LeaseEpoch != session.LeaseEpoch ||
			job.State != types.WorkbenchJobStateRunning || job.ClosedAt != nil {
			return ErrWorkbenchJobConflict
		}
		if input.ExpectedLeaseEpoch >= 0 && job.LeaseEpoch != input.ExpectedLeaseEpoch {
			return ErrWorkbenchStaleEpoch
		}
		if input.ExpectedJobStateVersion >= 0 && job.StateVersion != input.ExpectedJobStateVersion {
			return ErrWorkbenchJobStaleVersion
		}
		input.Command.WorkbenchSessionID = session.ID
		if err := tx.Create(input.Command).Error; err != nil {
			if isWorkbenchDuplicateKey(err) {
				return ErrWorkbenchCommandSequenceConflict
			}
			return err
		}
		created = input.Command
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}
func (r *gormWorkbenchControlRepository) GetActiveJobForSession(ctx context.Context, tenantID uint64, chatSessionID, workbenchSessionID, jobID string, expectedLeaseEpoch int64) (*types.WorkbenchJob, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(chatSessionID) == "" || strings.TrimSpace(workbenchSessionID) == "" || strings.TrimSpace(jobID) == "" || expectedLeaseEpoch < 0 {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	var job types.WorkbenchJob
	err := r.db.WithContext(ctx).Table("workbench_jobs AS jobs").
		Select("jobs.*").
		Joins("JOIN workbench_sessions AS sessions ON sessions.tenant_id = jobs.tenant_id AND sessions.id = jobs.workbench_session_id").
		Where("jobs.tenant_id = ? AND jobs.chat_session_id = ? AND jobs.workbench_session_id = ? AND jobs.id = ?",
			tenantID, strings.TrimSpace(chatSessionID), strings.TrimSpace(workbenchSessionID), strings.TrimSpace(jobID)).
		Where("jobs.incarnation_id = sessions.incarnation_id").
		Where("jobs.lease_epoch = sessions.lease_epoch AND sessions.lease_epoch = ?", expectedLeaseEpoch).
		Where("sessions.state = ? AND sessions.closed_at IS NULL AND jobs.closed_at IS NULL AND jobs.state IN ?",
			string(types.WorkbenchStateReady), []string{
				string(types.WorkbenchJobStateQueued),
				string(types.WorkbenchJobStateStarting),
				string(types.WorkbenchJobStateRunning),
			}).
		First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchJobNotFound
	}
	if err != nil {
		return nil, err
	}
	if job.LeaseEpoch != expectedLeaseEpoch {
		return nil, ErrWorkbenchStaleEpoch
	}
	return &job, nil
}
func (r *gormWorkbenchControlRepository) GetCommandByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchCommand, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(id) == "" {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	var command types.WorkbenchCommand
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).First(&command).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchCommandNotFound
	}
	if err != nil {
		return nil, err
	}
	return &command, nil
}

func (r *gormWorkbenchControlRepository) CompareAndSwapCommandState(ctx context.Context, input WorkbenchCommandStateCAS) (*types.WorkbenchCommand, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if input.TenantID == 0 || strings.TrimSpace(input.ID) == "" || input.ExpectedStateVersion < 0 || !input.NextState.IsValid() {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	if input.ExpectedState != "" && !input.ExpectedState.IsValid() {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	updates := map[string]any{
		"state":         string(input.NextState),
		"state_version": gorm.Expr("state_version + 1"),
		"updated_at":    time.Now().UTC(),
	}
	if input.NextState.IsTerminal() {
		updates["closed_at"] = time.Now().UTC()
		updates["terminal_reason"] = strings.TrimSpace(input.TerminalReason)
	}
	query := r.db.WithContext(ctx).Model(&types.WorkbenchCommand{}).
		Where("tenant_id = ? AND id = ? AND state_version = ? AND closed_at IS NULL", input.TenantID, input.ID, input.ExpectedStateVersion)
	if input.ExpectedState != "" {
		query = query.Where("state = ?", string(input.ExpectedState))
	}
	res := query.Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrWorkbenchCommandStaleVersion
	}
	return r.GetCommandByID(ctx, input.TenantID, input.ID)
}

func (r *gormWorkbenchControlRepository) AppendRunnerEvent(ctx context.Context, event *types.WorkbenchRunnerEvent) (*types.WorkbenchRunnerEvent, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if err := validateWorkbenchRunnerEventForWrite(event); err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Create(event).Error; err == nil {
		return event, nil
	} else if !isWorkbenchDuplicateKey(err) {
		return nil, err
	}
	return nil, ErrWorkbenchRunnerEventConflict
}

func (r *gormWorkbenchControlRepository) ListRunnerEvents(ctx context.Context, tenantID uint64, jobID string, afterSeq int64, limit int) ([]*types.WorkbenchRunnerEvent, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(jobID) == "" || afterSeq < 0 {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var events []*types.WorkbenchRunnerEvent
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND workbench_job_id = ? AND seq > ?", tenantID, jobID, afterSeq).
		Order("seq ASC").
		Limit(limit).
		Find(&events).Error
	return events, err
}

func (r *gormWorkbenchControlRepository) EnqueueAuditOutbox(ctx context.Context, row *types.WorkbenchAuditOutbox) (*types.WorkbenchAuditOutbox, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if err := validateWorkbenchAuditOutboxForWrite(row); err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err == nil {
		return row, nil
	} else if !isWorkbenchDuplicateKey(err) {
		return nil, err
	} else if existing, getErr := r.getAuditOutboxByIdempotencyKey(ctx, row.TenantID, row.IdempotencyKey); getErr == nil {
		return existing, nil
	}
	return nil, ErrWorkbenchAuditOutboxConflict
}

func (r *gormWorkbenchControlRepository) ListAuditOutbox(ctx context.Context, tenantID uint64, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || (state != "" && !state.IsValid()) {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID).Order("created_at ASC").Limit(limit)
	if state != "" {
		query = query.Where("state = ?", string(state))
	}
	var rows []*types.WorkbenchAuditOutbox
	err := query.Find(&rows).Error
	return rows, err
}

func (r *gormWorkbenchControlRepository) ListAuditOutboxForJob(ctx context.Context, tenantID uint64, jobID string, state types.WorkbenchAuditOutboxState, limit int) ([]*types.WorkbenchAuditOutbox, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(jobID) == "" || (state != "" && !state.IsValid()) {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND workbench_job_id = ?", tenantID, strings.TrimSpace(jobID)).
		Order("created_at ASC").
		Limit(limit)
	if state != "" {
		query = query.Where("state = ?", string(state))
	}
	var rows []*types.WorkbenchAuditOutbox
	err := query.Find(&rows).Error
	return rows, err
}
func (r *gormWorkbenchControlRepository) getAuditOutboxByIdempotencyKey(ctx context.Context, tenantID uint64, key string) (*types.WorkbenchAuditOutbox, error) {
	var row types.WorkbenchAuditOutbox
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND idempotency_key = ?", tenantID, key).First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func validateWorkbenchJobStart(input WorkbenchJobStartInput) error {
	if input.TenantID == 0 || strings.TrimSpace(input.WorkbenchSessionID) == "" ||
		input.ExpectedLeaseEpoch < 0 || len(strings.TrimSpace(input.StartNonceHash)) != 64 ||
		strings.TrimSpace(input.BackendType) == "" ||
		input.ResourcePolicySnapshot == nil || strings.TrimSpace(input.CreatedBy) == "" {
		return ErrWorkbenchSessionInvalidArgument
	}
	return nil
}
func validateWorkbenchJobForWrite(job *types.WorkbenchJob) error {
	if job == nil || job.TenantID == 0 || strings.TrimSpace(job.WorkbenchSessionID) == "" ||
		strings.TrimSpace(job.ChatSessionID) == "" || strings.TrimSpace(job.IncarnationID) == "" ||
		job.LeaseEpoch < 0 || len(strings.TrimSpace(job.StartNonceHash)) != 64 ||
		strings.TrimSpace(job.BackendType) == "" ||
		!job.State.IsValid() || job.State.IsTerminal() || job.StateVersion < 0 ||
		job.ResourcePolicySnapshot == nil || strings.TrimSpace(job.CreatedBy) == "" {
		return ErrWorkbenchSessionInvalidArgument
	}
	job.StartNonceHash = strings.TrimSpace(job.StartNonceHash)
	return nil
}

func validateWorkbenchCommandForWrite(command *types.WorkbenchCommand) error {
	if command == nil || command.TenantID == 0 || strings.TrimSpace(command.WorkbenchJobID) == "" ||
		strings.TrimSpace(command.WorkbenchSessionID) == "" || command.Sequence <= 0 ||
		strings.TrimSpace(command.Kind) == "" || command.Payload == nil || !command.State.IsValid() ||
		command.State.IsTerminal() || command.StateVersion < 0 || strings.TrimSpace(command.CreatedBy) == "" {
		return ErrWorkbenchSessionInvalidArgument
	}
	return nil
}

func validateWorkbenchRunnerEventForWrite(event *types.WorkbenchRunnerEvent) error {
	if event == nil || event.TenantID == 0 || strings.TrimSpace(event.WorkbenchJobID) == "" ||
		strings.TrimSpace(event.WorkbenchSessionID) == "" || event.Seq <= 0 ||
		strings.TrimSpace(event.EventType) == "" || event.Payload == nil {
		return ErrWorkbenchSessionInvalidArgument
	}
	return nil
}

func validateWorkbenchAuditOutboxForWrite(row *types.WorkbenchAuditOutbox) error {
	if row == nil || row.TenantID == 0 || strings.TrimSpace(row.WorkbenchSessionID) == "" ||
		strings.TrimSpace(string(row.Action)) == "" || strings.TrimSpace(row.IdempotencyKey) == "" ||
		row.Payload == nil || (row.State != "" && !row.State.IsValid()) || row.Attempts < 0 {
		return ErrWorkbenchSessionInvalidArgument
	}
	return nil
}

func workbenchConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}
