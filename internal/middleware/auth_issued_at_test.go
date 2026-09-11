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

func TestAuthTokenAuthTimeIgnoresFreshIATAndExtractsOriginalAuthentication(t *testing.T) {
	authTime := time.Date(2026, 9, 3, 1, 30, 0, 0, time.UTC)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"auth_time": authTime.Unix(),
		"iat":       authTime.Add(2 * time.Hour).Unix(),
		"type":      "access",
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	got := authTokenAuthTime(signed)
	if got == nil || !got.Equal(authTime) {
		t.Fatalf("authTokenAuthTime = %v, want %v", got, authTime)
	}
	if got := authTokenAuthTime("not-a-jwt"); got != nil {
		t.Fatalf("authTokenAuthTime(invalid) = %v, want nil", got)
	}
}

func TestApplyAuthSessionStoresAuthTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authTime := time.Date(2026, 9, 3, 1, 30, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	applyAuthSession(c, authSession{
		User:      &types.User{ID: "user-1"},
		Principal: types.Principal{Type: types.PrincipalWebUser, ID: "user-1"},
		AuthTime:  &authTime,
	})

	got, ok := types.AuthTimeFromContext(c.Request.Context())
	if !ok || !got.Equal(authTime) {
		t.Fatalf("AuthTimeFromContext = %v, %v; want %v, true", got, ok, authTime)
	}
	raw, exists := c.Get(types.AuthTimeContextKey.String())
	if !exists {
		t.Fatal("gin context missing AuthTime key")
	}
	got, ok = raw.(time.Time)
	if !ok || !got.Equal(authTime) {
		t.Fatalf("gin AuthTime = %v, %v; want %v, true", raw, ok, authTime)
	}
}
