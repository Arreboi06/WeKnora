package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type t2m01aWorkbenchRepo struct {
	createCalls int
	created     *types.WorkbenchSession
}

func (r *t2m01aWorkbenchRepo) CreateOrGet(ctx context.Context, session *types.WorkbenchSession) (*types.WorkbenchSession, error) {
	r.createCalls++
	r.created = session
	if session.ID == "" {
		session.ID = "workbench-1"
	}
	return session, nil
}

func (r *t2m01aWorkbenchRepo) GetByID(ctx context.Context, tenantID uint64, id string) (*types.WorkbenchSession, error) {
	return nil, repository.ErrWorkbenchSessionNotFound
}

func (r *t2m01aWorkbenchRepo) GetActiveByChatSession(ctx context.Context, tenantID uint64, chatSessionID string) (*types.WorkbenchSession, error) {
	return nil, repository.ErrWorkbenchSessionNotFound
}

func (r *t2m01aWorkbenchRepo) CompareAndSwapState(ctx context.Context, input repository.WorkbenchSessionStateCAS) (*types.WorkbenchSession, error) {
	return nil, repository.ErrWorkbenchSessionStaleVersion
}

func (r *t2m01aWorkbenchRepo) ProbeSchema(ctx context.Context) error {
	return nil
}

func TestT2M01AWorkbenchServiceVerifiesTenantScopedChatSession(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open("file:t2m01a-service?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Session{}))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Session{ID: "chat-a", TenantID: 2, UserID: "actor-a"}).Error)

	sessionRepo := repository.NewSessionRepository(db)
	workbenchRepo := &t2m01aWorkbenchRepo{}
	svc := NewWorkbenchSessionService(workbenchRepo, sessionRepo)

	_, err = svc.CreateOrGet(ctx, t2m01aValidCreateInput(1, "chat-a", "incarnation-a"))
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionNotFound)
	require.Zero(t, workbenchRepo.createCalls, "cross-tenant session lookup must fail before insert")

	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Session{ID: "chat-b", TenantID: 1, UserID: "actor-a"}).Error)
	created, err := svc.CreateOrGet(ctx, t2m01aValidCreateInput(1, "chat-b", "incarnation-a"))
	require.NoError(t, err)
	require.Equal(t, uint64(1), created.TenantID)
	require.Equal(t, "chat-b", created.ChatSessionID)
	require.Equal(t, types.WorkbenchPurpose, created.Purpose)
	require.Equal(t, types.WorkbenchStateProvisioning, created.State)
	require.Equal(t, "actor-a", created.CreatedBy)
	require.Equal(t, 1, workbenchRepo.createCalls)
}

func TestT2M01AWorkbenchServiceRejectsInvalidCreationEnvelope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:t2m01a-service-invalid?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Session{}))
	require.NoError(t, db.Session(&gorm.Session{SkipHooks: true}).Create(&types.Session{ID: "chat-a", TenantID: 1, UserID: "actor-a"}).Error)

	svc := NewWorkbenchSessionService(&t2m01aWorkbenchRepo{}, repository.NewSessionRepository(db))
	input := t2m01aValidCreateInput(1, "chat-a", "incarnation-a")
	input.Purpose = "terminal"
	_, err = svc.CreateOrGet(context.Background(), input)
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionInvalidArgument)

	input = t2m01aValidCreateInput(1, "chat-a", "incarnation-a")
	input.CapabilitySnapshot = nil
	_, err = svc.CreateOrGet(context.Background(), input)
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionInvalidArgument)

	input = t2m01aValidCreateInput(1, "chat-a", strings.Repeat("x", 37))
	_, err = svc.CreateOrGet(context.Background(), input)
	require.ErrorIs(t, err, repository.ErrWorkbenchSessionInvalidArgument)
}

func TestT2M01AWorkbenchCapabilityReasonOrder(t *testing.T) {
	cases := []struct {
		name   string
		status WorkbenchCapabilityStatus
		reason string
	}{
		{name: "feature disabled", status: WorkbenchCapabilityStatus{Known: true}, reason: WorkbenchCapabilityReasonFeatureDisabled},
		{name: "unsupported database", status: WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "sqlite"}, reason: WorkbenchCapabilityReasonUnsupportedDatabase},
		{name: "migration unavailable", status: WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "postgres"}, reason: WorkbenchCapabilityReasonMigrationUnavailable},
		{name: "route missing", status: WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "postgres", SchemaReady: true}, reason: WorkbenchCapabilityReasonRouteNotRegistered},
		{name: "backend missing", status: WorkbenchCapabilityStatus{Known: true, FeatureEnabled: true, DatabaseDialect: "postgres", SchemaReady: true, RouteRegistered: true}, reason: WorkbenchCapabilityReasonNoEligibleBackend},
		{name: "unknown only after all concrete prerequisites pass", status: WorkbenchCapabilityStatus{FeatureEnabled: true, DatabaseDialect: "postgres", SchemaReady: true, RouteRegistered: true, EligibleBackends: []string{"local-protected-docker"}}, reason: WorkbenchCapabilityReasonUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := EvaluateWorkbenchCapability(tc.status)
			require.False(t, result.Supported)
			require.Equal(t, tc.reason, result.Reason)
		})
	}

	result := EvaluateWorkbenchCapability(WorkbenchCapabilityStatus{
		Known:            true,
		FeatureEnabled:   true,
		DatabaseDialect:  "postgres",
		SchemaReady:      true,
		RouteRegistered:  true,
		EligibleBackends: []string{"local-protected-docker"},
	})
	require.True(t, result.Supported)
	require.Empty(t, result.Reason)
	require.Equal(t, []string{"local-protected-docker"}, result.EligibleBackends)
}

func TestT2L09WorkbenchCapabilityRejectsUnqualifiedBackendNames(t *testing.T) {
	require.Equal(t, []string{"local-protected-docker"}, normalizeWorkbenchEligibleBackends([]string{
		"docker", "local-protected-docker", "unverified-backend", "local-protected-docker",
	}))
}
func TestT2L09WorkbenchCapabilityRequiresEverySchemaProbe(t *testing.T) {
	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
	sandbox.SetDockerBackendEnabled(true)
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 user=weknora dbname=weknora sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)

	sessionProbe := &t2m01aWorkbenchRepo{}
	failingProbe := &t2l09CapabilityProbe{err: errors.New("artifact table missing")}
	svc := NewWorkbenchCapabilityServiceWithSchemaProbes(db, sessionProbe, []WorkbenchSchemaProbe{sessionProbe, failingProbe}, []string{"local-protected-docker"})

	status := svc.Status(context.Background(), true, nil)
	require.False(t, status.SchemaReady)
	require.Equal(t, "artifact table missing", status.MigrationError)
	require.Equal(t, WorkbenchCapabilityReasonMigrationUnavailable, EvaluateWorkbenchCapability(status).Reason)
	require.Equal(t, 1, failingProbe.calls)
}
func TestT2L08WorkbenchCapabilityUsesConfiguredEligibleBackends(t *testing.T) {
	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)
	t.Setenv(sandbox.DockerBackendEnabledEnv, "true")
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  "host=127.0.0.1 user=weknora dbname=weknora sslmode=disable",
		PreferSimpleProtocol: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)

	svc := NewWorkbenchCapabilityServiceWithBackends(db, &t2m01aWorkbenchRepo{}, []string{
		"",
		"local-protected-docker",
		"local-protected-docker",
	})

	status := svc.Status(context.Background(), true, nil)
	require.Equal(t, []string{"local-protected-docker"}, status.EligibleBackends)
	result := EvaluateWorkbenchCapability(status)
	require.True(t, result.Supported)
	require.Equal(t, []string{"local-protected-docker"}, result.EligibleBackends)

	explicitNone := svc.Status(context.Background(), true, []string{})
	require.Empty(t, explicitNone.EligibleBackends)
	require.False(t, EvaluateWorkbenchCapability(explicitNone).Supported)

	sandbox.SetDockerBackendEnabled(false)
	dockerDisabled := svc.Status(context.Background(), true, nil)
	require.Empty(t, dockerDisabled.EligibleBackends)
	disabledResult := EvaluateWorkbenchCapability(dockerDisabled)
	require.False(t, disabledResult.Supported)
	require.Equal(t, WorkbenchCapabilityReasonNoEligibleBackend, disabledResult.Reason)
}

func TestT2M01AWorkbenchFlagDefaultAndMalformedFalse(t *testing.T) {
	t.Setenv(sandbox.WorkbenchEnabledEnv, "")
	require.False(t, sandbox.WorkbenchEnabled())

	t.Setenv(sandbox.WorkbenchEnabledEnv, "definitely")
	require.False(t, sandbox.WorkbenchEnabled())

	t.Setenv(sandbox.WorkbenchEnabledEnv, "true")
	require.True(t, sandbox.WorkbenchEnabled())
}

func t2m01aValidCreateInput(tenantID uint64, chatSessionID, incarnationID string) CreateWorkbenchSessionInput {
	return CreateWorkbenchSessionInput{
		TenantID:           tenantID,
		ChatSessionID:      chatSessionID,
		Purpose:            types.WorkbenchPurpose,
		IncarnationID:      incarnationID,
		SandboxConfigID:    "sandbox-config-a",
		BackendType:        "docker-r1",
		ActorID:            "actor-a",
		CapabilitySnapshot: types.JSONMap{"sandbox.workbench": true},
		PolicySnapshot:     types.JSONMap{"max_seconds": float64(60)},
	}
}

type t2l09CapabilityProbe struct {
	err   error
	calls int
}

func (p *t2l09CapabilityProbe) ProbeSchema(context.Context) error {
	p.calls++
	return p.err
}

var _ repository.WorkbenchSessionRepository = (*t2m01aWorkbenchRepo)(nil)
