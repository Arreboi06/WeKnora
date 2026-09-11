package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
)

func init() {
	_ = os.Setenv("JWT_SECRET", "test-jwt-secret-for-user-auth-token-tests")
}

type stubAuthTokenRepo struct {
	tokens         map[string]*types.AuthToken
	revokedUserIDs []string
}

func (s *stubAuthTokenRepo) CreateToken(context.Context, *types.AuthToken) error { return nil }
func (s *stubAuthTokenRepo) GetTokenByValue(_ context.Context, tokenValue string) (*types.AuthToken, error) {
	token, ok := s.tokens[tokenValue]
	if !ok {
		return nil, errors.New("token not found")
	}
	return token, nil
}
func (s *stubAuthTokenRepo) GetTokensByUserID(context.Context, string) ([]*types.AuthToken, error) {
	return nil, nil
}
func (s *stubAuthTokenRepo) UpdateToken(context.Context, *types.AuthToken) error { return nil }
func (s *stubAuthTokenRepo) DeleteToken(context.Context, string) error           { return nil }
func (s *stubAuthTokenRepo) DeleteExpiredTokens(context.Context) error           { return nil }
func (s *stubAuthTokenRepo) RevokeTokensByUserID(_ context.Context, userID string) error {
	s.revokedUserIDs = append(s.revokedUserIDs, userID)
	return nil
}

type stubUserRepoForAuth struct {
	users       map[string]*types.User
	updateCalls int
}

func (s *stubUserRepoForAuth) CreateUser(context.Context, *types.User) error { return nil }
func (s *stubUserRepoForAuth) GetUserByID(_ context.Context, id string) (*types.User, error) {
	user, ok := s.users[id]
	if !ok {
		return nil, errors.New("user not found")
	}
	return user, nil
}
func (s *stubUserRepoForAuth) GetUsersByIDs(context.Context, []string) (map[string]*types.User, error) {
	return nil, nil
}
func (s *stubUserRepoForAuth) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	for _, user := range s.users {
		if user.Email == email {
			return user, nil
		}
	}
	return nil, nil
}
func (s *stubUserRepoForAuth) GetUserByUsername(context.Context, string) (*types.User, error) {
	return nil, nil
}
func (s *stubUserRepoForAuth) GetUserByTenantID(context.Context, uint64) (*types.User, error) {
	return nil, nil
}
func (s *stubUserRepoForAuth) UpdateUser(context.Context, *types.User) error {
	s.updateCalls++
	return nil
}
func (s *stubUserRepoForAuth) DeleteUser(context.Context, string) error { return nil }
func (s *stubUserRepoForAuth) ListUsers(context.Context, int, int) ([]*types.User, error) {
	return nil, nil
}
func (s *stubUserRepoForAuth) ListSystemAdmins(context.Context, int, int) ([]*types.User, int64, error) {
	return nil, 0, nil
}
func (s *stubUserRepoForAuth) RevokeSystemAdmin(context.Context, string, string) (*types.User, error) {
	return nil, nil
}
func (s *stubUserRepoForAuth) SearchUsers(context.Context, string, int) ([]*types.User, error) {
	return nil, nil
}

func newAuthTestUserService(tokenRepo *stubAuthTokenRepo) *userService {
	return &userService{
		userRepo: &stubUserRepoForAuth{
			users: map[string]*types.User{
				"user-1": {ID: "user-1", TenantID: 1},
			},
		},
		tokenRepo: tokenRepo,
	}
}

func signTestJWT(claims jwt.MapClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(getJwtSecret()))
	if err != nil {
		panic(err)
	}
	return signed
}

func TestValidateTokenRejectsRefreshToken(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)

	refreshJWT := signTestJWT(jwt.MapClaims{
		"user_id": "user-1",
		"type":    "refresh",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	tokenRepo.tokens[refreshJWT] = &types.AuthToken{
		UserID:    "user-1",
		Token:     refreshJWT,
		TokenType: "refresh_token",
	}

	_, _, err := svc.ValidateToken(ctx, refreshJWT)
	if err == nil || err.Error() != "refresh token cannot be used as access token" {
		t.Fatalf("ValidateToken(refresh JWT) err = %v, want refresh rejection", err)
	}

	legacyRefresh := signTestJWT(jwt.MapClaims{
		"user_id": "user-1",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	tokenRepo.tokens[legacyRefresh] = &types.AuthToken{
		UserID:    "user-1",
		Token:     legacyRefresh,
		TokenType: "refresh_token",
	}

	_, _, err = svc.ValidateToken(ctx, legacyRefresh)
	if err == nil || err.Error() != "refresh token cannot be used as access token" {
		t.Fatalf("ValidateToken(legacy refresh in DB) err = %v, want refresh rejection", err)
	}
}

func TestRefreshTokenRejectsAccessTokenRecord(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)

	refreshJWT := signTestJWT(jwt.MapClaims{
		"user_id": "user-1",
		"type":    "refresh",
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	tokenRepo.tokens[refreshJWT] = &types.AuthToken{
		UserID:    "user-1",
		Token:     refreshJWT,
		TokenType: "access_token",
	}

	_, _, err := svc.RefreshToken(ctx, refreshJWT)
	if err == nil || err.Error() != "not a refresh token" {
		t.Fatalf("RefreshToken(access token record) err = %v, want not a refresh token", err)
	}
}

func TestRefreshTokenPreservesOriginalAuthenticationTime(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	authTime := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	refreshJWT := signTestJWT(jwt.MapClaims{
		"user_id":   "user-1",
		"type":      "refresh",
		"auth_time": authTime.Unix(),
		"iat":       time.Now().UTC().Unix(),
		"exp":       time.Now().Add(time.Hour).Unix(),
	})
	tokenRepo.tokens[refreshJWT] = &types.AuthToken{
		UserID:    "user-1",
		Token:     refreshJWT,
		TokenType: "refresh_token",
	}

	accessToken, rotatedRefreshToken, err := svc.RefreshToken(ctx, refreshJWT)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	for tokenKind, tokenValue := range map[string]string{
		"access":  accessToken,
		"refresh": rotatedRefreshToken,
	} {
		claims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(tokenValue, claims); err != nil {
			t.Fatalf("parse %s token: %v", tokenKind, err)
		}
		gotAuthTime, ok := testNumericDateClaim(claims, "auth_time")
		if !ok || !gotAuthTime.Equal(authTime) {
			t.Fatalf("%s auth_time = %v, %v; want %v", tokenKind, gotAuthTime, ok, authTime)
		}
		issuedAt, ok := testNumericDateClaim(claims, "iat")
		if !ok || !issuedAt.After(authTime) {
			t.Fatalf("%s iat = %v, %v; must be fresh while auth_time remains old", tokenKind, issuedAt, ok)
		}
	}
}

func TestLegacyRefreshTokenCannotAcquireRecentAuthenticationTime(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	legacyRefresh := signTestJWT(jwt.MapClaims{
		"user_id": "user-1", "type": "refresh",
		"iat": time.Now().UTC().Add(-time.Hour).Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	tokenRepo.tokens[legacyRefresh] = &types.AuthToken{
		UserID: "user-1", Token: legacyRefresh, TokenType: "refresh_token",
	}

	accessToken, refreshToken, err := svc.RefreshToken(ctx, legacyRefresh)
	if err != nil {
		t.Fatalf("RefreshToken(legacy): %v", err)
	}
	for tokenKind, tokenValue := range map[string]string{"access": accessToken, "refresh": refreshToken} {
		claims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(tokenValue, claims); err != nil {
			t.Fatalf("parse %s token: %v", tokenKind, err)
		}
		if _, ok := claims["auth_time"]; ok {
			t.Fatalf("legacy %s rotation minted auth_time without a new authentication ceremony", tokenKind)
		}
	}
}

func TestPasswordLoginMintsFreshAuthenticationTime(t *testing.T) {
	password := "correct-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	svc.userRepo.(*stubUserRepoForAuth).users["user-1"] = &types.User{
		ID: "user-1", TenantID: 0, Email: "user@example.com", PasswordHash: string(hash), IsActive: true,
	}
	before := time.Now().UTC().Add(-time.Second)
	response, err := svc.Login(context.Background(), &types.LoginRequest{Email: "user@example.com", Password: password})
	after := time.Now().UTC().Add(time.Second)
	if err != nil || response == nil || !response.Success {
		t.Fatalf("Login() response=%+v err=%v", response, err)
	}
	for tokenKind, tokenValue := range map[string]string{"access": response.Token, "refresh": response.RefreshToken} {
		claims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(tokenValue, claims); err != nil {
			t.Fatalf("parse %s token: %v", tokenKind, err)
		}
		authTime, ok := testNumericDateClaim(claims, "auth_time")
		if !ok || authTime.Before(before) || authTime.After(after) {
			t.Fatalf("%s auth_time = %v, %v; want fresh login time in [%v,%v]", tokenKind, authTime, ok, before, after)
		}
	}
}

func TestOIDCLoginMintsFreshAuthenticationTime(t *testing.T) {
	withOIDCSSRFWhitelist(t, "127.0.0.1")

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"access_token": "provider-access-token",
				"token_type":   "Bearer",
			})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-access-token" {
				t.Fatalf("userinfo authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sub":   "subject-1",
				"email": "oidc@example.test",
				"name":  "OIDC User",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	userRepo := &stubUserRepoForAuth{users: map[string]*types.User{
		"oidc-user": {
			ID:       "oidc-user",
			Email:    "oidc@example.test",
			IsActive: true,
		},
	}}
	svc := &userService{
		userRepo:  userRepo,
		tokenRepo: &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}},
		config: &config.Config{OIDCAuth: &config.OIDCAuthConfig{
			Enable:                true,
			AuthorizationEndpoint: provider.URL + "/authorize",
			TokenEndpoint:         provider.URL + "/token",
			UserInfoEndpoint:      provider.URL + "/userinfo",
			ClientID:              "weknora-client",
			ClientSecret:          "test-client-secret",
			UserInfoMapping: &config.OIDCUserInfoMapping{
				Username: "name",
				Email:    "email",
			},
		}},
	}

	before := time.Now().UTC().Add(-time.Second)
	response, err := svc.LoginWithOIDC(
		context.Background(), "authorization-code", "https://app.example.test/callback", types.TenantProvisioningCreatePersonal,
	)
	after := time.Now().UTC().Add(time.Second)
	if err != nil || response == nil || !response.Success {
		t.Fatalf("LoginWithOIDC() response=%+v err=%v", response, err)
	}
	for tokenKind, tokenValue := range map[string]string{"access": response.Token, "refresh": response.RefreshToken} {
		claims := jwt.MapClaims{}
		if _, _, err := jwt.NewParser().ParseUnverified(tokenValue, claims); err != nil {
			t.Fatalf("parse %s token: %v", tokenKind, err)
		}
		authTime, ok := testNumericDateClaim(claims, "auth_time")
		if !ok || authTime.Before(before) || authTime.After(after) {
			t.Fatalf("%s auth_time = %v, %v; want OIDC ceremony time in [%v,%v]", tokenKind, authTime, ok, before, after)
		}
	}
}

func testNumericDateClaim(claims jwt.MapClaims, name string) (time.Time, bool) {
	raw, ok := claims[name]
	if !ok {
		return time.Time{}, false
	}
	unix, ok := raw.(float64)
	if !ok || unix <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(unix), 0).UTC(), true
}

func TestLogoutRevokesAllUserTokens(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)

	expiredAccess := signTestJWT(jwt.MapClaims{
		"user_id": "user-1",
		"type":    "access",
		"exp":     time.Now().Add(-time.Hour).Unix(),
	})

	if err := svc.Logout(ctx, expiredAccess); err != nil {
		t.Fatalf("Logout(expired access token) err = %v", err)
	}
	if len(tokenRepo.revokedUserIDs) != 1 || tokenRepo.revokedUserIDs[0] != "user-1" {
		t.Fatalf("RevokeTokensByUserID calls = %v, want [user-1]", tokenRepo.revokedUserIDs)
	}
}

func TestAdminResetPasswordHashesPasswordAndRevokesSessions(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	repo := svc.userRepo.(*stubUserRepoForAuth)

	if err := svc.AdminResetPassword(ctx, "user-1", "NewSecure9"); err != nil {
		t.Fatalf("AdminResetPassword() err = %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("UpdateUser calls = %d, want 1", repo.updateCalls)
	}
	user := repo.users["user-1"]
	if user.PasswordHash == "NewSecure9" || user.PasswordHash == "" {
		t.Fatalf("password was not stored as a hash")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("NewSecure9")); err != nil {
		t.Fatalf("stored hash does not match new password: %v", err)
	}
	if len(tokenRepo.revokedUserIDs) != 1 || tokenRepo.revokedUserIDs[0] != "user-1" {
		t.Fatalf("RevokeTokensByUserID calls = %v, want [user-1]", tokenRepo.revokedUserIDs)
	}
}

func TestAdminResetPasswordRejectsWeakPasswordBeforeWrite(t *testing.T) {
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	repo := svc.userRepo.(*stubUserRepoForAuth)

	err := svc.AdminResetPassword(context.Background(), "user-1", "password")
	if !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("AdminResetPassword() err = %v, want ErrPasswordPolicy", err)
	}
	if repo.updateCalls != 0 || len(tokenRepo.revokedUserIDs) != 0 {
		t.Fatalf("weak password caused side effects: updates=%d revocations=%v",
			repo.updateCalls, tokenRepo.revokedUserIDs)
	}
}

func TestChangePasswordRequiresPolicyAndRevokesSessions(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	repo := svc.userRepo.(*stubUserRepoForAuth)

	hashed, err := bcrypt.GenerateFromPassword([]byte("OldSecure9"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	repo.users["user-1"].PasswordHash = string(hashed)

	if err := svc.ChangePassword(ctx, "user-1", "OldSecure9", "weak"); !errors.Is(err, ErrPasswordPolicy) {
		t.Fatalf("ChangePassword(weak) err = %v, want ErrPasswordPolicy", err)
	}
	if repo.updateCalls != 0 || len(tokenRepo.revokedUserIDs) != 0 {
		t.Fatalf("weak password caused side effects: updates=%d revocations=%v",
			repo.updateCalls, tokenRepo.revokedUserIDs)
	}

	if err := svc.ChangePassword(ctx, "user-1", "wrong-pass", "NewSecure9"); !errors.Is(err, ErrInvalidOldPassword) {
		t.Fatalf("ChangePassword(wrong old) err = %v, want ErrInvalidOldPassword", err)
	}

	if err := svc.ChangePassword(ctx, "user-1", "OldSecure9", "NewSecure9"); err != nil {
		t.Fatalf("ChangePassword() err = %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(repo.users["user-1"].PasswordHash), []byte("NewSecure9")); err != nil {
		t.Fatalf("stored hash does not match new password: %v", err)
	}
	if len(tokenRepo.revokedUserIDs) != 1 || tokenRepo.revokedUserIDs[0] != "user-1" {
		t.Fatalf("revoked users = %v, want [user-1]", tokenRepo.revokedUserIDs)
	}
}

func TestChangePasswordRejectsSamePassword(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	repo := svc.userRepo.(*stubUserRepoForAuth)

	hashed, err := bcrypt.GenerateFromPassword([]byte("OldSecure9"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	repo.users["user-1"].PasswordHash = string(hashed)

	if err := svc.ChangePassword(ctx, "user-1", "OldSecure9", "OldSecure9"); !errors.Is(err, ErrSamePassword) {
		t.Fatalf("ChangePassword(same) err = %v, want ErrSamePassword", err)
	}
	if repo.updateCalls != 0 || len(tokenRepo.revokedUserIDs) != 0 {
		t.Fatalf("same password caused side effects: updates=%d revocations=%v", repo.updateCalls, tokenRepo.revokedUserIDs)
	}
}

func TestChangePasswordHonoursRuntimeComplexPolicy(t *testing.T) {
	ctx := context.Background()
	tokenRepo := &stubAuthTokenRepo{tokens: map[string]*types.AuthToken{}}
	svc := newAuthTestUserService(tokenRepo)
	svc.systemSettingSvc = &stubComplexPasswordSettings{enabled: true}
	repo := svc.userRepo.(*stubUserRepoForAuth)

	hashed, err := bcrypt.GenerateFromPassword([]byte("OldSecure9"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	repo.users["user-1"].PasswordHash = string(hashed)

	if err := svc.ChangePassword(ctx, "user-1", "wrong-pass", "weak"); !errors.Is(err, ErrInvalidOldPassword) {
		t.Fatalf("ChangePassword(wrong old, weak new) err = %v, want ErrInvalidOldPassword", err)
	}
	if err := svc.ChangePassword(ctx, "user-1", "OldSecure9", "NewSecure9"); !errors.Is(err, ErrComplexPasswordPolicy) {
		t.Fatalf("ChangePassword(simple new) err = %v, want ErrComplexPasswordPolicy", err)
	}
	if repo.updateCalls != 0 || len(tokenRepo.revokedUserIDs) != 0 {
		t.Fatalf("complex-policy reject caused side effects: updates=%d revocations=%v",
			repo.updateCalls, tokenRepo.revokedUserIDs)
	}
	if err := svc.ChangePassword(ctx, "user-1", "OldSecure9", "NewSecure9!"); err != nil {
		t.Fatalf("ChangePassword(complex new) err = %v", err)
	}
}

func TestUserIDFromSignedTokenAcceptsExpiredToken(t *testing.T) {
	expired := signTestJWT(jwt.MapClaims{
		"user_id": "user-1",
		"type":    "access",
		"exp":     time.Now().Add(-time.Hour).Unix(),
	})

	userID, err := userIDFromSignedToken(expired)
	if err != nil {
		t.Fatalf("userIDFromSignedToken(expired) err = %v", err)
	}
	if userID != "user-1" {
		t.Fatalf("userIDFromSignedToken(expired) = %q, want user-1", userID)
	}
}
