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
	ErrWorkbenchArtifactNotFound = errors.New("workbench artifact not found")
	ErrWorkbenchArtifactConflict = errors.New("workbench artifact conflict")
)

type WorkbenchArtifactRepository interface {
	ProbeSchema(ctx context.Context) error
	CreateArtifactVersion(ctx context.Context, input WorkbenchArtifactVersionInput) (*types.WorkbenchArtifactVersion, error)
	GetArtifactVersion(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error)
}

type WorkbenchArtifactVersionInput struct {
	ID                 string
	ArtifactID         string
	Version            int
	TenantID           uint64
	ChatSessionID      string
	WorkbenchSessionID string
	WorkbenchJobID     string
	CommandID          string
	MessageID          string
	SkillRunID         string
	SourceFileRef      types.WorkbenchFileRef
	ContentSHA256      string
	SizeBytes          int64
	MimeType           string
	PreviewClass       types.WorkbenchPreviewClass
	FileName           string
	Content            []byte
	CreatedBy          string
}

type gormWorkbenchArtifactRepository struct{ db *gorm.DB }

func NewWorkbenchArtifactRepository(db *gorm.DB) WorkbenchArtifactRepository {
	return &gormWorkbenchArtifactRepository{db: db}
}

func (r *gormWorkbenchArtifactRepository) requirePostgres() error {
	if r == nil || r.db == nil || r.db.Dialector == nil || r.db.Dialector.Name() != "postgres" {
		return ErrWorkbenchUnsupportedDatabase
	}
	return nil
}

func (r *gormWorkbenchArtifactRepository) ProbeSchema(ctx context.Context) error {
	if err := r.requirePostgres(); err != nil {
		return err
	}
	var exists bool
	if err := r.db.WithContext(ctx).Raw("SELECT to_regclass(?) IS NOT NULL", "public.workbench_artifact_versions").Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: missing workbench_artifact_versions", ErrWorkbenchMigrationUnavailable)
	}
	return nil
}

func (r *gormWorkbenchArtifactRepository) CreateArtifactVersion(ctx context.Context, input WorkbenchArtifactVersionInput) (*types.WorkbenchArtifactVersion, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if err := validateWorkbenchArtifactVersionInput(input); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = uuid.NewString()
	}
	row := &types.WorkbenchArtifactVersion{
		ID:                 id,
		ArtifactID:         strings.TrimSpace(input.ArtifactID),
		Version:            input.Version,
		TenantID:           input.TenantID,
		ChatSessionID:      strings.TrimSpace(input.ChatSessionID),
		WorkbenchSessionID: strings.TrimSpace(input.WorkbenchSessionID),
		WorkbenchJobID:     strings.TrimSpace(input.WorkbenchJobID),
		CommandID:          strings.TrimSpace(input.CommandID),
		MessageID:          strings.TrimSpace(input.MessageID),
		SkillRunID:         strings.TrimSpace(input.SkillRunID),
		SourceFileRef:      input.SourceFileRef,
		ContentSHA256:      strings.TrimSpace(input.ContentSHA256),
		SizeBytes:          input.SizeBytes,
		MimeType:           strings.TrimSpace(input.MimeType),
		PreviewClass:       input.PreviewClass,
		FileName:           strings.TrimSpace(input.FileName),
		Content:            append([]byte(nil), input.Content...),
		CreatedBy:          strings.TrimSpace(input.CreatedBy),
		CreatedAt:          time.Now().UTC(),
	}
	if err := r.db.WithContext(ctx).Create(row).Error; err != nil {
		if isWorkbenchDuplicateKey(err) {
			return nil, ErrWorkbenchArtifactConflict
		}
		return nil, err
	}
	return row, nil
}

func (r *gormWorkbenchArtifactRepository) GetArtifactVersion(ctx context.Context, tenantID uint64, chatSessionID, artifactID string, version int) (*types.WorkbenchArtifactVersion, error) {
	if err := r.requirePostgres(); err != nil {
		return nil, err
	}
	if tenantID == 0 || strings.TrimSpace(chatSessionID) == "" || strings.TrimSpace(artifactID) == "" || version <= 0 {
		return nil, ErrWorkbenchSessionInvalidArgument
	}
	var row types.WorkbenchArtifactVersion
	err := r.db.WithContext(ctx).
		Table("workbench_artifact_versions AS artifacts").
		Select("artifacts.*").
		Joins("JOIN workbench_sessions AS sessions ON sessions.tenant_id = artifacts.tenant_id AND sessions.id = artifacts.workbench_session_id AND sessions.chat_session_id = artifacts.chat_session_id").
		Where("artifacts.tenant_id = ? AND artifacts.chat_session_id = ? AND artifacts.artifact_id = ? AND artifacts.version = ?", tenantID, strings.TrimSpace(chatSessionID), strings.TrimSpace(artifactID), version).
		Where("sessions.state = ? AND sessions.closed_at IS NULL", string(types.WorkbenchStateReady)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrWorkbenchArtifactNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func validateWorkbenchArtifactVersionInput(input WorkbenchArtifactVersionInput) error {
	if input.TenantID == 0 || strings.TrimSpace(input.ArtifactID) == "" || input.Version <= 0 ||
		strings.TrimSpace(input.ChatSessionID) == "" || strings.TrimSpace(input.WorkbenchSessionID) == "" ||
		strings.TrimSpace(input.WorkbenchJobID) == "" || strings.TrimSpace(input.ContentSHA256) == "" ||
		len(strings.TrimSpace(input.ContentSHA256)) != 64 || input.SizeBytes < 0 || int64(len(input.Content)) != input.SizeBytes ||
		strings.TrimSpace(input.MimeType) == "" || !input.PreviewClass.IsValid() || strings.TrimSpace(input.FileName) == "" ||
		strings.TrimSpace(input.CreatedBy) == "" || input.SourceFileRef.FileRefVersion != 1 {
		return ErrWorkbenchSessionInvalidArgument
	}
	return nil
}
