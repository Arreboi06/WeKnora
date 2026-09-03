package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestCitationProfileSensitiveOperationGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 9, 3, 1, 15, 0, 0, time.UTC)

	tests := []struct {
		name       string
		headers    map[string]string
		issuedAt   *time.Time
		apiKey     bool
		wantStatus int
	}{
		{
			name:     "fresh auth with same origin passes",
			issuedAt: citationProfileTestTime(now.Add(-2 * time.Minute)),
			headers: map[string]string{
				"Origin": "http://api.example.test",
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:     "fresh auth with csrf header passes without origin",
			issuedAt: citationProfileTestTime(now.Add(-2 * time.Minute)),
			headers: map[string]string{
				citationProfileCSRFHeader: "1",
			},
			wantStatus: http.StatusNoContent,
		},
		{
			name:     "cross origin without csrf header is rejected",
			issuedAt: citationProfileTestTime(now.Add(-2 * time.Minute)),
			headers: map[string]string{
				"Origin": "http://evil.example.test",
			},
			wantStatus: http.StatusForbidden,
		},
		{
			name:     "stale auth with same origin requires recent auth",
			issuedAt: citationProfileTestTime(now.Add(-16 * time.Minute)),
			headers: map[string]string{
				"Origin": "http://api.example.test",
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "same origin without auth issued-at requires recent auth",
			headers: map[string]string{
				"Origin": "http://api.example.test",
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "api key principal uses machine authorization path",
			headers: map[string]string{
				"Origin": "http://evil.example.test",
			},
			apiKey:     true,
			wantStatus: http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			if tt.apiKey || tt.issuedAt != nil {
				router.Use(func(c *gin.Context) {
					ctx := c.Request.Context()
					if tt.apiKey {
						ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{FullAccess: true})
					}
					if tt.issuedAt != nil {
						ctx = types.WithAuthIssuedAt(ctx, *tt.issuedAt)
					}
					c.Request = c.Request.WithContext(ctx)
					c.Next()
				})
			}
			router.Use(citationProfileSensitiveOperationGuardWithClock(func() time.Time { return now }))
			router.POST("/mutate", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			req := httptest.NewRequest(http.MethodPost, "http://api.example.test/mutate", nil)
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func citationProfileTestTime(t time.Time) *time.Time {
	return &t
}
func TestCitationProfileSensitiveOperationGuardRejectsSchemeMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 9, 3, 1, 15, 0, 0, time.UTC)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := types.WithAuthIssuedAt(c.Request.Context(), now.Add(-2*time.Minute))
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.Use(citationProfileSensitiveOperationGuardWithClock(func() time.Time { return now }))
	router.POST("/mutate", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodPost, "http://api.example.test/mutate", nil)
	req.Header.Set("Origin", "https://api.example.test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}
