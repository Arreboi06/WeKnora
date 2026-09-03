package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestAuthTokenIssuedAtExtractsAccessTokenIAT(t *testing.T) {
	issuedAt := time.Date(2026, 9, 3, 1, 30, 0, 0, time.UTC)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iat":  issuedAt.Unix(),
		"type": "access",
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	got := authTokenIssuedAt(signed)
	if got == nil || !got.Equal(issuedAt) {
		t.Fatalf("authTokenIssuedAt = %v, want %v", got, issuedAt)
	}
	if got := authTokenIssuedAt("not-a-jwt"); got != nil {
		t.Fatalf("authTokenIssuedAt(invalid) = %v, want nil", got)
	}
}

func TestApplyAuthSessionStoresAuthIssuedAt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	issuedAt := time.Date(2026, 9, 3, 1, 30, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	applyAuthSession(c, authSession{
		User:         &types.User{ID: "user-1"},
		Principal:    types.Principal{Type: types.PrincipalWebUser, ID: "user-1"},
		AuthIssuedAt: &issuedAt,
	})

	got, ok := types.AuthIssuedAtFromContext(c.Request.Context())
	if !ok || !got.Equal(issuedAt) {
		t.Fatalf("AuthIssuedAtFromContext = %v, %v; want %v, true", got, ok, issuedAt)
	}
	raw, exists := c.Get(types.AuthIssuedAtContextKey.String())
	if !exists {
		t.Fatal("gin context missing AuthIssuedAt key")
	}
	got, ok = raw.(time.Time)
	if !ok || !got.Equal(issuedAt) {
		t.Fatalf("gin AuthIssuedAt = %v, %v; want %v, true", raw, ok, issuedAt)
	}
}
