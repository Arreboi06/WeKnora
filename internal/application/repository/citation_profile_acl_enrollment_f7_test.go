//go:build cgo

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCitationProfileEnrollmentF7PersistsServerBindingAndIgnoresClientACLFields(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	const (
		effectiveTenantID     uint64 = 200
		authenticatedTenantID uint64 = 100
		apiKeyID              uint64 = 42
	)
	kbID := uuid.NewString()
	ctx := citationProfileEnrollmentF7Context(
		effectiveTenantID,
		types.Principal{Type: types.PrincipalAPITenant, ID: "100"},
		authenticatedTenantID,
		apiKeyID,
		types.CitationProfileACLAccessPathKBShare,
		kbID,
	)
	subjectID := types.SessionOwnerIDFromContext(ctx)
	require.Equal(t, "api_tenant_key:100:42", subjectID)

	var request types.CitationProfileEnrollmentRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"enabled": true,
		"expected_read_version": "0",
		"idempotency_key": "f7-enrollment-client-injection",
		"acl_principal_type": "web_user",
		"acl_principal_id": "attacker",
		"acl_authenticated_tenant_id": 999,
		"acl_api_key_id": 999,
		"acl_access_path": "owner",
		"acl_access_path_id": "attacker-controlled",
		"acl_generation": 999
	}`), &request))
	roundTrip, err := json.Marshal(request)
	require.NoError(t, err)
	require.NotContains(t, string(roundTrip), "acl_",
		"the public enrollment DTO must not expose any ACL binding input")

	zero := uint64(0)
	scope, err := repo.SetEnrollment(
		ctx,
		effectiveTenantID,
		subjectID,
		kbID,
		request.Enabled,
		&zero,
		request.IdempotencyKey,
	)
	require.NoError(t, err)
	require.NotNil(t, scope)
	require.Equal(t, types.PrincipalAPITenant, scope.ACLPrincipalType)
	require.Equal(t, "100", scope.ACLPrincipalID)
	require.Equal(t, authenticatedTenantID, scope.ACLAuthenticatedTenantID)
	require.Equal(t, apiKeyID, scope.ACLAPIKeyID)
	require.Equal(t, types.CitationProfileACLAccessPathKBShare, scope.ACLAccessPath)
	require.Equal(t, kbID, scope.ACLAccessPathID)
	require.Equal(t, uint64(1), scope.ACLGeneration)
	require.Equal(t, types.CitationProfileACLStateUnknown, scope.ACLCheckState,
		"a request-time binding is identity evidence, not a durable post-race ALLOW decision")
	require.NotNil(t, scope.NextACLCheckAt)
}

func TestCitationProfileEnrollmentF7EnabledFailsClosedWithoutServerBinding(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	const (
		effectiveTenantID     uint64 = 200
		authenticatedTenantID uint64 = 100
	)
	kbID := uuid.NewString()
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, effectiveTenantID)
	ctx = types.WithAuthenticatedTenantID(ctx, authenticatedTenantID)
	ctx = types.WithPrincipal(ctx, types.Principal{Type: types.PrincipalAPITenant, ID: "100"})
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 42})
	zero := uint64(0)

	scope, err := repo.SetEnrollment(
		ctx,
		effectiveTenantID,
		"api_tenant_key:100:42",
		kbID,
		true,
		&zero,
		"f7-enrollment-missing-binding",
	)
	require.Nil(t, scope)
	require.Error(t, err)
	require.True(
		t,
		errors.Is(err, types.ErrCitationProfileUnavailable) ||
			errors.Is(err, types.ErrCitationProfileInvalidRequest),
		"missing authoritative ACL binding must fail closed with unavailable/invalid, got %v",
		err,
	)
	var scopeCount int64
	require.NoError(t, db.Model(&types.CitationProfileScope{}).
		Where("tenant_id = ? AND subject_id = ? AND knowledge_base_id = ?",
			effectiveTenantID, "api_tenant_key:100:42", kbID).
		Count(&scopeCount).Error)
	require.Zero(t, scopeCount)
}

func TestCitationProfileEnrollmentF7RefreshesLiveScopeBindingAndGeneration(t *testing.T) {
	db := newCitationProfileRepositoryTestDB(t)
	repo := NewCitationProfileRepository(db)
	const (
		effectiveTenantID     uint64 = 200
		authenticatedTenantID uint64 = 100
		apiKeyID              uint64 = 42
	)
	kbID := uuid.NewString()
	subjectID := "api_tenant_key:100:42"
	existing := citationProfileTestScope(
		uuid.NewString(),
		subjectID,
		kbID,
		uuid.NewString(),
		7,
		0,
	)
	existing.TenantID = effectiveTenantID
	existing.ACLCheckState = types.CitationProfileACLStateCurrent
	existing.ACLPrincipalType = types.PrincipalWebUser
	existing.ACLPrincipalID = "stale-user"
	existing.ACLAuthenticatedTenantID = 9
	existing.ACLAPIKeyID = 0
	existing.ACLAccessPath = types.CitationProfileACLAccessPathOwner
	existing.ACLAccessPathID = ""
	existing.ACLGeneration = 5
	require.NoError(t, db.Create(existing).Error)

	ctx := citationProfileEnrollmentF7Context(
		effectiveTenantID,
		types.Principal{Type: types.PrincipalAPITenant, ID: "100"},
		authenticatedTenantID,
		apiKeyID,
		types.CitationProfileACLAccessPathKBShare,
		kbID,
	)
	expectedReadVersion := existing.ProfileReadVersion
	refreshed, err := repo.SetEnrollment(
		ctx,
		effectiveTenantID,
		subjectID,
		kbID,
		true,
		&expectedReadVersion,
		"f7-enrollment-refresh-binding",
	)
	require.NoError(t, err)
	require.NotNil(t, refreshed)
	require.Equal(t, existing.ID, refreshed.ID)
	require.Equal(t, existing.SubjectEpoch, refreshed.SubjectEpoch)
	require.Equal(t, types.PrincipalAPITenant, refreshed.ACLPrincipalType)
	require.Equal(t, "100", refreshed.ACLPrincipalID)
	require.Equal(t, authenticatedTenantID, refreshed.ACLAuthenticatedTenantID)
	require.Equal(t, apiKeyID, refreshed.ACLAPIKeyID)
	require.Equal(t, types.CitationProfileACLAccessPathKBShare, refreshed.ACLAccessPath)
	require.Equal(t, kbID, refreshed.ACLAccessPathID)
	require.Equal(t, existing.ACLGeneration+1, refreshed.ACLGeneration)
	require.Equal(t, types.CitationProfileACLStateUnknown, refreshed.ACLCheckState)
}

func citationProfileEnrollmentF7Context(
	effectiveTenantID uint64,
	principal types.Principal,
	authenticatedTenantID uint64,
	apiKeyID uint64,
	accessPath string,
	accessPathID string,
) context.Context {
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, effectiveTenantID)
	ctx = types.WithPrincipal(ctx, principal)
	ctx = types.WithAuthenticatedTenantID(ctx, authenticatedTenantID)
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: apiKeyID})
	return types.WithCitationProfileACLBinding(ctx, types.CitationProfileACLBinding{
		PrincipalType:         principal.Type,
		PrincipalID:           principal.ID,
		AuthenticatedTenantID: authenticatedTenantID,
		APIKeyID:              apiKeyID,
		AccessPath:            accessPath,
		AccessPathID:          accessPathID,
	})
}
