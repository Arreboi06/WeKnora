package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrWorkbenchSkillRunNotFound     = errors.New("workbench skill run not found")
	ErrWorkbenchSkillRunConflict     = errors.New("workbench skill run conflict")
	ErrWorkbenchSkillRunStaleVersion = errors.New("workbench skill run state version mismatch or missing")
)

type WorkbenchSkillRunRepository interface {
	ProbeSchema(ctx context.Context) error
	CreateSkillRun(ctx context.Context, input WorkbenchSkillRunInput) (*types.WorkbenchSkillRun, error)
	GetSkillRun(ctx context.Context, tenantID uint64, chatSessionID, id string) (*types.WorkbenchSkillRun, error)
	GetSkillRunByCommand(ctx context.Context, tenantID uint64, chatSessionID, commandID string) (*types.WorkbenchSkillRun, error)
	CompleteSkillRun(ctx context.Context, input WorkbenchSkillRunCompleteCAS) (*types.WorkbenchSkillRun, error)
}

type WorkbenchSkillRunInput struct {
	ID                 string
	TenantID           uint64
	ChatSessionID      string
	WorkbenchSessionID string
	WorkbenchJobID     string
	CommandID          string
	SkillName          string
	SkillOperation     string
	OutputFileRef      types.WorkbenchFileRef
	CreatedBy          string
	CreatedAt          time.Time
}

type WorkbenchSkillRunCompleteCAS struct {
	TenantID              uint64
	ID                    string
	ExpectedStateVersion  int64
	ExpectedState         types.WorkbenchSkillRunState
	NextState             types.WorkbenchSkillRunState
	OutputArtifactID      string
	OutputArtifactVersion int
	TerminalReason        string
}

type gormWorkbenchSkillRunRepository struct{ db *gorm.DB }

func NewWorkbenchSkillRunRepository(db *gorm.DB) WorkbenchSkillRunRepository {
	return &gormWorkbenchSkillRunRepository{db: db}
}

func (r *gormWorkbenchSkillRunRepository) requirePostgres() error {
	if r == nil || r.db == nil || r.db.Dialector == nil || r.db.Dialector.Name() != "postgres" {
		return ErrWorkbenchUnsupportedDatabase
	}
	return nil
}

func (r *gormWorkbenchSkillRunRepository) ProbeSchema(ctx context.Context) error {
	if err := r.requirePostgres(); err != nil {
		return err
	}
	var exists bool
	if err := r.db.WithContext(ctx).Raw("SELECT to_regclass(?) IS NOT NULL", "public.workbench_skill_runs").Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: missing workbench_skill_runs", ErrWorkbenchMigrationUnavailable)
	}
	return nil
}

func (r *gormWorkbenchSkillRunRepository) CreateSkillRun(ctx context.Context, input WorkbenchSkillRunInput) (*types.WorkbenchSkillRun, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if err := validateWorkbenchSkillRunInput(input); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = uuid.NewString()
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	row := &types.WorkbenchSkillRun{
		ID: id, TenantID: input.TenantID, ChatSessionID: strings.TrimSpace(input.ChatSessionID),
		WorkbenchSessionID: strings.TrimSpace(input.WorkbenchSessionID), WorkbenchJobID: strings.TrimSpace(input.WorkbenchJobID),
		CommandID: strings.TrimSpace(input.CommandID), SkillName: normalizeWorkbenchSkillName(input.SkillName),
		SkillOperation: strings.TrimSpace(input.SkillOperation), OutputFileRef: input.OutputFileRef,
		State: types.WorkbenchSkillRunStateQueued, StateVersion: 0, CreatedBy: strings.TrimSpace(input.CreatedBy),
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		if isWorkbenchDuplicateKey(err) {
			return nil, ErrWorkbenchSkillRunConflict
		}
		return nil, err
	}
	return row, nil
}

func (r *gormWorkbenchSkillRunRepository) GetSkillRun(ctx context.Context, tenantID uint64, chatSessionID, id string) (*types.WorkbenchSkillRun, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(chatSessionID) == "" || strings.TrimSpace(id) == "" {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	var row types.WorkbenchSkillRun
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND chat_session_id = ? AND id = ?", tenantID, strings.TrimSpace(chatSessionID), strings.TrimSpace(id)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchSkillRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *gormWorkbenchSkillRunRepository) GetSkillRunByCommand(ctx context.Context, tenantID uint64, chatSessionID, commandID string) (*types.WorkbenchSkillRun, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(chatSessionID) == "" || strings.TrimSpace(commandID) == "" {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	var row types.WorkbenchSkillRun
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND chat_session_id = ? AND command_id = ?", tenantID, strings.TrimSpace(chatSessionID), strings.TrimSpace(commandID)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchSkillRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (r *gormWorkbenchSkillRunRepository) CompleteSkillRun(ctx context.Context, input WorkbenchSkillRunCompleteCAS) (*types.WorkbenchSkillRun, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if input.TenantID == 0 || strings.TrimSpace(input.ID) == "" || input.ExpectedStateVersion < 0 || !input.ExpectedState.IsValid() || !input.NextState.IsValid() || !input.NextState.IsTerminal() {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	if input.NextState == types.WorkbenchSkillRunStateSucceeded && (strings.TrimSpace(input.OutputArtifactID) == "" || input.OutputArtifactVersion <= 0) {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	now := time.Now().UTC()
	updates := map[string]any{
		"state":                   string(input.NextState),
		"state_version":           gorm.Expr("state_version + 1"),
		"updated_at":              now,
		"closed_at":               now,
		"terminal_reason":         strings.TrimSpace(input.TerminalReason),
		"output_artifact_id":      strings.TrimSpace(input.OutputArtifactID),
		"output_artifact_version": input.OutputArtifactVersion,
	}
	res := r.db.WithContext(ctx).Model(&types.WorkbenchSkillRun{}).
		Where("tenant_id = ? AND id = ? AND state_version = ? AND state = ? AND closed_at IS NULL", input.TenantID, strings.TrimSpace(input.ID), input.ExpectedStateVersion, string(input.ExpectedState)).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrWorkbenchSkillRunStaleVersion
	}
	return r.getSkillRunByID(ctx, input.TenantID, input.ID)
}

func (r *gormWorkbenchSkillRunRepository) getSkillRunByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchSkillRun, error) {
	var row types.WorkbenchSkillRun
	err := r.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, strings.TrimSpace(id)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchSkillRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func validateWorkbenchSkillRunInput(input WorkbenchSkillRunInput) error {
	if input.TenantID == 0 || strings.TrimSpace(input.ChatSessionID) == "" || strings.TrimSpace(input.WorkbenchSessionID) == "" ||
		strings.TrimSpace(input.WorkbenchJobID) == "" || strings.TrimSpace(input.CommandID) == "" || normalizeWorkbenchSkillName(input.SkillName) != "presentations" ||
		strings.TrimSpace(input.SkillOperation) != "create_presentation" || input.OutputFileRef.FileRefVersion != 1 || input.OutputFileRef.Root != types.WorkbenchFileRootOutput ||
		len(input.OutputFileRef.Segments) == 0 || strings.TrimSpace(input.CreatedBy) == "" {
		return ErrWorkbenchSessionInvalidArgument
	}
	return nil
}

func normalizeWorkbenchSkillName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
