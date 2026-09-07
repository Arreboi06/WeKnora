package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

var (
	ErrWorkbenchUnsupportedDatabase    = errors.New("workbench sessions require PostgreSQL")
	ErrWorkbenchMigrationUnavailable   = errors.New("workbench sessions migration is unavailable")
	ErrWorkbenchSessionNotFound        = errors.New("workbench session not found")
	ErrWorkbenchSessionConflict        = errors.New("workbench session active incarnation conflict")
	ErrWorkbenchSessionStaleVersion    = errors.New("workbench session state version mismatch or missing")
	ErrWorkbenchSessionInvalidArgument = errors.New("invalid workbench session argument")
)

type WorkbenchSessionRepository interface {
	CreateOrGet(ctx context.Context, session *types.WorkbenchSession) (*types.WorkbenchSession, error)
	GetByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchSession, error)
	GetActiveByChatSession(ctx context.Context, tenantID uint64, chatSessionID string) (*types.WorkbenchSession, error)
	CompareAndSwapState(ctx context.Context, input WorkbenchSessionStateCAS) (*types.WorkbenchSession, error)
	ProbeSchema(ctx context.Context) error
}

type WorkbenchSessionStateCAS struct {
	TenantID             uint64
	ID                   string
	ExpectedStateVersion int64
	ExpectedState        types.WorkbenchSessionState
	NextState            types.WorkbenchSessionState
	TerminalReason       string
}

type gormWorkbenchSessionRepository struct {
	db *gorm.DB
}

func NewWorkbenchSessionRepository(db *gorm.DB) WorkbenchSessionRepository {
	return &gormWorkbenchSessionRepository{db: db}
}

func (r *gormWorkbenchSessionRepository) requirePostgres() error {
	if r == nil || r.db == nil || r.db.Dialector == nil || r.db.Dialector.Name() != "postgres" {
		return ErrWorkbenchUnsupportedDatabase
	}
	return nil
}

func (r *gormWorkbenchSessionRepository) CreateOrGet(ctx context.Context, session *types.WorkbenchSession) (*types.WorkbenchSession, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if err := validateWorkbenchSessionForWrite(session); err != nil {
		return nil, err
	}

	if existing, err := r.getExactByIncarnation(ctx, session.TenantID, session.ChatSessionID, session.Purpose, session.IncarnationID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}

	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	if err := r.db.WithContext(ctx).Create(session).Error; err == nil {
		return session, nil
	} else if !isWorkbenchDuplicateKey(err) {
		return nil, err
	}

	if existing, err := r.getExactByIncarnation(ctx, session.TenantID, session.ChatSessionID, session.Purpose, session.IncarnationID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	if active, err := r.getActiveByChatSession(ctx, session.TenantID, session.ChatSessionID); err != nil {
		return nil, err
	} else if active != nil {
		return nil, ErrWorkbenchSessionConflict
	}
	return nil, ErrWorkbenchSessionConflict
}

func (r *gormWorkbenchSessionRepository) GetByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchSession, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(id) == "" {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	var session types.WorkbenchSession
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, id).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *gormWorkbenchSessionRepository) GetActiveByChatSession(ctx context.Context, tenantID uint64, chatSessionID string) (*types.WorkbenchSession, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(chatSessionID) == "" {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	active, err := r.getActiveByChatSession(ctx, tenantID, chatSessionID)
	if err != nil {
		return nil, err
	}
	if active == nil {
		return nil, ErrWorkbenchSessionNotFound
	}
	return active, nil
}

func (r *gormWorkbenchSessionRepository) CompareAndSwapState(ctx context.Context, input WorkbenchSessionStateCAS) (*types.WorkbenchSession, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if input.TenantID == 0 || strings.TrimSpace(input.ID) == "" || input.ExpectedStateVersion < 0 || !input.NextState.IsValid() {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	if input.ExpectedState != "" && !input.ExpectedState.IsValid() {
		return nil, ErrWorkbenchSessionInvalidArgument
	}

	now := time.Now().UTC()
	updates := map[string]any{
		"state":         string(input.NextState),
		"state_version": gorm.Expr("state_version + 1"),
		"updated_at":    now,
	}
	if input.NextState.IsTerminal() {
		updates["closed_at"] = now
		updates["terminal_reason"] = strings.TrimSpace(input.TerminalReason)
	}

	query := r.db.WithContext(ctx).Model(&types.WorkbenchSession{}).
		Where("tenant_id = ? AND id = ? AND state_version = ? AND closed_at IS NULL", input.TenantID, input.ID, input.ExpectedStateVersion)
	if input.ExpectedState != "" {
		query = query.Where("state = ?", string(input.ExpectedState))
	}
	res := query.Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrWorkbenchSessionStaleVersion
	}
	return r.GetByID(ctx, input.TenantID, input.ID)
}

func (r *gormWorkbenchSessionRepository) ProbeSchema(ctx context.Context) error {
	if err := r.requirePostgres(); err != nil {
		return err
	}
	var exists bool
	if err := r.db.WithContext(ctx).Raw("SELECT to_regclass('public.workbench_sessions') IS NOT NULL").Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return ErrWorkbenchMigrationUnavailable
	}

	required := []string{
		"id", "tenant_id", "chat_session_id", "purpose", "incarnation_id", "sandbox_config_id",
		"backend_type", "state", "state_version", "lease_epoch", "capability_snapshot", "policy_snapshot",
		"created_by", "terminal_reason", "created_at", "updated_at", "closed_at",
	}
	var count int
	if err := r.db.WithContext(ctx).Raw(
		"SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'workbench_sessions' AND column_name IN ?",
		required,
	).Scan(&count).Error; err != nil {
		return err
	}
	if count != len(required) {
		return fmt.Errorf("%w: expected %d columns, found %d", ErrWorkbenchMigrationUnavailable, len(required), count)
	}
	return nil
}

func (r *gormWorkbenchSessionRepository) getExactByIncarnation(ctx context.Context, tenantID uint64, chatSessionID, purpose, incarnationID string) (*types.WorkbenchSession, error) {
	var session types.WorkbenchSession
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND chat_session_id = ? AND purpose = ? AND incarnation_id = ?", tenantID, chatSessionID, purpose, incarnationID).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *gormWorkbenchSessionRepository) getActiveByChatSession(ctx context.Context, tenantID uint64, chatSessionID string) (*types.WorkbenchSession, error) {
	var session types.WorkbenchSession
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND chat_session_id = ? AND purpose = ? AND closed_at IS NULL", tenantID, chatSessionID, types.WorkbenchPurpose).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func validateWorkbenchSessionForWrite(session *types.WorkbenchSession) error {
	if session == nil || session.TenantID == 0 ||
		strings.TrimSpace(session.ChatSessionID) == "" ||
		strings.TrimSpace(session.IncarnationID) == "" ||
		strings.TrimSpace(session.SandboxConfigID) == "" ||
		strings.TrimSpace(session.BackendType) == "" ||
		strings.TrimSpace(session.CreatedBy) == "" ||
		session.Purpose != types.WorkbenchPurpose ||
		!session.State.IsValid() || session.State.IsTerminal() ||
		session.StateVersion < 0 || session.LeaseEpoch < 0 ||
		session.CapabilitySnapshot == nil || session.PolicySnapshot == nil {
		return ErrWorkbenchSessionInvalidArgument
	}
	return nil
}

func isWorkbenchDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(err.Error(), "duplicate key value violates unique constraint")
}
