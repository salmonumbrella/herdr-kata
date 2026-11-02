package clock

import (
	"context"
	"time"
)

type key struct{}

func WithNow(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, key{}, at.UTC())
}
func Now(ctx context.Context) time.Time {
	if at, ok := ctx.Value(key{}).(time.Time); ok {
		return at
	}
	return time.Now().UTC()
}
