package router

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

const (
	citationProfileCSRFHeader          = "X-WeKnora-CSRF"
	citationProfileRecentAuthWindow    = 15 * time.Minute
	citationProfileRecentAuthClockSkew = time.Minute
)

func citationProfileSensitiveOperationGuard() gin.HandlerFunc {
	return citationProfileSensitiveOperationGuardWithClock(time.Now)
}

func citationProfileSensitiveOperationGuardWithClock(now func() time.Time) gin.HandlerFunc {
	if now == nil {
		now = time.Now
	}
	return func(c *gin.Context) {
		if _, ok := types.TenantAPIKeyScopeFromContext(c.Request.Context()); ok {
			c.Next()
			return
		}
		if !citationProfileSameOriginOrCSRF(c) {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Forbidden: citation profile operation requires same-origin or CSRF header",
				"code":  "CITATION_PROFILE_SAME_ORIGIN_REQUIRED",
			})
			c.Abort()
			return
		}
		authTime, ok := types.AuthTimeFromContext(c.Request.Context())
		if !ok || !citationProfileAuthTimeRecent(authTime, now().UTC()) {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Unauthorized: recent authentication required",
				"code":  "CITATION_PROFILE_RECENT_AUTH_REQUIRED",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func citationProfileSameOriginOrCSRF(c *gin.Context) bool {
	csrfHeader := strings.TrimSpace(c.GetHeader(citationProfileCSRFHeader))
	if csrfHeader == "1" || strings.EqualFold(csrfHeader, "true") {
		return true
	}
	if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" {
		return citationProfileRequestHostMatches(c.Request, origin)
	}
	if referer := strings.TrimSpace(c.GetHeader("Referer")); referer != "" {
		return citationProfileRequestHostMatches(c.Request, referer)
	}
	return false
}

func citationProfileRequestHostMatches(req *http.Request, rawURL string) bool {
	if req == nil || strings.TrimSpace(req.Host) == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.Scheme == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, citationProfileRequestScheme(req)) && strings.EqualFold(parsed.Host, req.Host)
}

func citationProfileRequestScheme(req *http.Request) string {
	if req != nil {
		if req.URL != nil && strings.TrimSpace(req.URL.Scheme) != "" {
			return strings.ToLower(strings.TrimSpace(req.URL.Scheme))
		}
		if req.TLS != nil {
			return "https"
		}
	}
	return "http"
}

func citationProfileAuthTimeRecent(authTime, now time.Time) bool {
	authTime = authTime.UTC()
	now = now.UTC()
	if authTime.After(now.Add(citationProfileRecentAuthClockSkew)) {
		return false
	}
	return !authTime.Before(now.Add(-citationProfileRecentAuthWindow))
}
