package clock

import (
	"context"
	"time"
)

type key struct{}
type sourceKey struct{}

func WithNow(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, key{}, at.UTC())
}
func WithSource(ctx context.Context, source func() time.Time) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}
func Progress(ctx context.Context, at time.Time) context.Context {
	if ctx.Value(key{}) != nil {
		return ctx
	}
	ctx = WithNow(ctx, at)
	if ctx.Value(sourceKey{}) != nil {
		return ctx
	}
	started := time.Now()
	return WithSource(ctx, func() time.Time { return at.Add(time.Since(started)) })
}
func Current(ctx context.Context, fallback time.Time) time.Time {
	if source, ok := ctx.Value(sourceKey{}).(func() time.Time); ok {
		return source().UTC()
	}
	return fallback
}
func Now(ctx context.Context) time.Time {
	if at, ok := ctx.Value(key{}).(time.Time); ok {
		return at
	}
	return Current(ctx, time.Now().UTC())
}
