package types

import (
	"context"
	"time"
)

func WithAuthIssuedAt(ctx context.Context, issuedAt time.Time) context.Context {
	if issuedAt.IsZero() {
		return ctx
	}
	return context.WithValue(ctx, AuthIssuedAtContextKey, issuedAt.UTC())
}

func AuthIssuedAtFromContext(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	issuedAt, ok := ctx.Value(AuthIssuedAtContextKey).(time.Time)
	if !ok || issuedAt.IsZero() {
		return time.Time{}, false
	}
	return issuedAt.UTC(), true
}
