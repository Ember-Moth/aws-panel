package auth

import (
	"context"
	"errors"
)

var ErrDenied = errors.New("access denied")

type Principal struct {
	ID          int64
	Username    string
	DisplayName string
	Role        string
}

type RequestMeta struct {
	RequestID string
	ClientIP  string
}

type contextKey uint8

const (
	principalKey contextKey = iota
	csrfKey
	metaKey
)

func WithPrincipal(ctx context.Context, principal Principal, csrf string) context.Context {
	return context.WithValue(context.WithValue(ctx, principalKey, principal), csrfKey, csrf)
}
func Current(ctx context.Context) (Principal, bool) {
	value, ok := ctx.Value(principalKey).(Principal)
	return value, ok
}
func CSRF(ctx context.Context) string { value, _ := ctx.Value(csrfKey).(string); return value }
func WithMeta(ctx context.Context, meta RequestMeta) context.Context {
	return context.WithValue(ctx, metaKey, meta)
}
func Meta(ctx context.Context) RequestMeta {
	value, _ := ctx.Value(metaKey).(RequestMeta)
	return value
}
