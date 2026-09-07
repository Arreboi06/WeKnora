package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/database"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

const maxWorkbenchIncarnationIDLength = 36

const (
	WorkbenchCapabilityReasonFeatureDisabled      = "feature_disabled"
	WorkbenchCapabilityReasonUnsupportedDatabase  = "unsupported_database"
	WorkbenchCapabilityReasonMigrationUnavailable = "migration_unavailable"
	WorkbenchCapabilityReasonRouteNotRegistered   = "route_not_registered"
	WorkbenchCapabilityReasonNoEligibleBackend    = "no_eligible_backend"
	WorkbenchCapabilityReasonUnknown              = "capability_unknown"
)

type WorkbenchSessionService struct {
	repo     repository.WorkbenchSessionRepository
	sessions interfaces.SessionRepository
}

type CreateWorkbenchSessionInput struct {
	TenantID           uint64
	ChatSessionID      string
	Purpose            string
	IncarnationID      string
	SandboxConfigID    string
	BackendType        string
	ActorID            string
	CapabilitySnapshot types.JSONMap
	PolicySnapshot     types.JSONMap
}

func NewWorkbenchSessionService(repo repository.WorkbenchSessionRepository, sessions interfaces.SessionRepository) *WorkbenchSessionService {
	return &WorkbenchSessionService{repo: repo, sessions: sessions}
}

func (s *WorkbenchSessionService) CreateOrGet(ctx context.Context, input CreateWorkbenchSessionInput) (*types.WorkbenchSession, error) {
	if s == nil || s.repo == nil || s.sessions == nil {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	input.Purpose = strings.TrimSpace(input.Purpose)
	if input.Purpose != types.WorkbenchPurpose || input.TenantID == 0 ||
		strings.TrimSpace(input.ChatSessionID) == "" ||
		strings.TrimSpace(input.IncarnationID) == "" ||
		utf8.RuneCountInString(strings.TrimSpace(input.IncarnationID)) > maxWorkbenchIncarnationIDLength ||
		strings.TrimSpace(input.SandboxConfigID) == "" ||
		strings.TrimSpace(input.BackendType) == "" ||
		strings.TrimSpace(input.ActorID) == "" ||
		input.CapabilitySnapshot == nil || input.PolicySnapshot == nil {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}

	if _, err := s.sessions.GetByID(ctx, input.TenantID, input.ChatSessionID); err != nil {
		if errors.Is(err, apperrors.ErrSessionNotFound) {
			return nil, repository.ErrWorkbenchSessionNotFound
		}
		return nil, err
	}

	return s.repo.CreateOrGet(ctx, &types.WorkbenchSession{
		TenantID:           input.TenantID,
		ChatSessionID:      strings.TrimSpace(input.ChatSessionID),
		Purpose:            types.WorkbenchPurpose,
		IncarnationID:      strings.TrimSpace(input.IncarnationID),
		SandboxConfigID:    strings.TrimSpace(input.SandboxConfigID),
		BackendType:        strings.TrimSpace(input.BackendType),
		State:              types.WorkbenchStateProvisioning,
		StateVersion:       0,
		LeaseEpoch:         0,
		CapabilitySnapshot: input.CapabilitySnapshot,
		PolicySnapshot:     input.PolicySnapshot,
		CreatedBy:          strings.TrimSpace(input.ActorID),
	})
}

func (s *WorkbenchSessionService) CompareAndSwapState(ctx context.Context, input repository.WorkbenchSessionStateCAS) (*types.WorkbenchSession, error) {
	if s == nil || s.repo == nil {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	return s.repo.CompareAndSwapState(ctx, input)
}

func (s *WorkbenchSessionService) GetActiveByChatSession(ctx context.Context, tenantID uint64, chatSessionID string) (*types.WorkbenchSession, error) {
	if s == nil || s.repo == nil || tenantID == 0 || strings.TrimSpace(chatSessionID) == "" {
		return nil, repository.ErrWorkbenchSessionInvalidArgument
	}
	return s.repo.GetActiveByChatSession(ctx, tenantID, strings.TrimSpace(chatSessionID))
}

type WorkbenchSchemaProbe interface {
	ProbeSchema(context.Context) error
}

type WorkbenchCapabilityService struct {
	db               *gorm.DB
	repo             repository.WorkbenchSessionRepository
	schemaProbes     []WorkbenchSchemaProbe
	eligibleBackends []string
}

type WorkbenchCapabilityStatus struct {
	Known            bool
	FeatureEnabled   bool
	DatabaseDialect  string
	MigrationError   string
	SchemaReady      bool
	RouteRegistered  bool
	EligibleBackends []string
}

type WorkbenchCapabilitySnapshot struct {
	Supported        bool
	Reason           string
	EligibleBackends []string
}

func NewWorkbenchCapabilityService(db *gorm.DB, repo repository.WorkbenchSessionRepository) *WorkbenchCapabilityService {
	return NewWorkbenchCapabilityServiceWithBackends(db, repo, nil)
}

func NewWorkbenchCapabilityServiceWithBackends(db *gorm.DB, repo repository.WorkbenchSessionRepository, eligibleBackends []string) *WorkbenchCapabilityService {
	return NewWorkbenchCapabilityServiceWithSchemaProbes(db, repo, []WorkbenchSchemaProbe{repo}, eligibleBackends)
}

func NewWorkbenchCapabilityServiceWithSchemaProbes(
	db *gorm.DB,
	repo repository.WorkbenchSessionRepository,
	probes []WorkbenchSchemaProbe,
	eligibleBackends []string,
) *WorkbenchCapabilityService {
	return &WorkbenchCapabilityService{
		db:               db,
		repo:             repo,
		schemaProbes:     append([]WorkbenchSchemaProbe(nil), probes...),
		eligibleBackends: normalizeWorkbenchEligibleBackends(eligibleBackends),
	}
}

func (s *WorkbenchCapabilityService) Status(ctx context.Context, routeRegistered bool, eligibleBackends []string) WorkbenchCapabilityStatus {
	effectiveBackends := normalizeWorkbenchEligibleBackends(eligibleBackends)
	if s != nil && eligibleBackends == nil {
		effectiveBackends = append([]string(nil), s.eligibleBackends...)
	}
	if !sandbox.DockerBackendEnabled() {
		effectiveBackends = nil
	}
	status := WorkbenchCapabilityStatus{
		Known:            true,
		FeatureEnabled:   sandbox.WorkbenchEnabled(),
		RouteRegistered:  routeRegistered,
		EligibleBackends: effectiveBackends,
	}
	if s == nil || s.db == nil || s.db.Dialector == nil || s.repo == nil {
		status.Known = false
		return status
	}
	status.DatabaseDialect = s.db.Dialector.Name()
	if !status.FeatureEnabled || status.DatabaseDialect != "postgres" {
		return status
	}
	status.MigrationError = database.CachedMigrationError()
	if status.MigrationError != "" {
		return status
	}
	probes := s.schemaProbes
	if len(probes) == 0 {
		probes = []WorkbenchSchemaProbe{s.repo}
	}
	for _, probe := range probes {
		if probe == nil {
			status.MigrationError = "workbench schema probe is unavailable"
			return status
		}
		if err := probe.ProbeSchema(ctx); err != nil {
			status.MigrationError = err.Error()
			return status
		}
	}
	status.SchemaReady = true
	return status
}

func (s *WorkbenchCapabilityService) Snapshot(ctx context.Context, routeRegistered bool) WorkbenchCapabilitySnapshot {
	if s == nil {
		return WorkbenchCapabilitySnapshot{Supported: false, Reason: WorkbenchCapabilityReasonUnknown}
	}
	return EvaluateWorkbenchCapability(s.Status(ctx, routeRegistered, nil))
}

func DefaultWorkbenchPolicySnapshot() types.JSONMap {
	return types.JSONMap{
		"network":     "none",
		"max_seconds": float64(60),
		"cpu_seconds": float64(30),
	}
}

func DefaultWorkbenchResourcePolicySnapshot() types.JSONMap {
	return types.JSONMap{
		"network":     "none",
		"max_seconds": float64(60),
		"cpu_seconds": float64(30),
	}
}
func normalizeWorkbenchEligibleBackends(backends []string) []string {
	if len(backends) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(backends))
	out := make([]string, 0, len(backends))
	for _, raw := range backends {
		backend := strings.TrimSpace(raw)
		if backend == "" || backend != types.WorkbenchBackendLocalProtectedDocker {
			continue
		}
		if _, ok := seen[backend]; ok {
			continue
		}
		seen[backend] = struct{}{}
		out = append(out, backend)
	}
	return out
}

func EvaluateWorkbenchCapability(status WorkbenchCapabilityStatus) WorkbenchCapabilitySnapshot {
	status.EligibleBackends = normalizeWorkbenchEligibleBackends(status.EligibleBackends)
	if !status.FeatureEnabled {
		return WorkbenchCapabilitySnapshot{Supported: false, Reason: WorkbenchCapabilityReasonFeatureDisabled}
	}
	if status.DatabaseDialect != "postgres" {
		return WorkbenchCapabilitySnapshot{Supported: false, Reason: WorkbenchCapabilityReasonUnsupportedDatabase}
	}
	if status.MigrationError != "" || !status.SchemaReady {
		return WorkbenchCapabilitySnapshot{Supported: false, Reason: WorkbenchCapabilityReasonMigrationUnavailable}
	}
	if !status.RouteRegistered {
		return WorkbenchCapabilitySnapshot{Supported: false, Reason: WorkbenchCapabilityReasonRouteNotRegistered}
	}
	if len(status.EligibleBackends) == 0 {
		return WorkbenchCapabilitySnapshot{Supported: false, Reason: WorkbenchCapabilityReasonNoEligibleBackend}
	}
	if !status.Known {
		return WorkbenchCapabilitySnapshot{Supported: false, Reason: WorkbenchCapabilityReasonUnknown}
	}
	return WorkbenchCapabilitySnapshot{
		Supported:        true,
		EligibleBackends: append([]string(nil), status.EligibleBackends...),
	}
}
