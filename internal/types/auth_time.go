package types

import (
	"context"
	"time"
)

// WithAuthTime records the verified time of the last real password or IdP
// authentication. Refresh and tenant-switch token rotation must preserve it.
func WithAuthTime(ctx context.Context, authTime time.Time) context.Context {
	if authTime.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, AuthTimeContextKey, authTime.UTC())
}

func AuthTimeFromContext(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	authTime, ok := ctx.Value(AuthTimeContextKey).(time.Time)
	if !ok || authTime.IsZero() {
		return time.Time{}, false
	}
	return authTime.UTC(), true
}
