package runner

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// A refused route cannot deliver a due report. The queue must back off its
// route checks for that lane rather than probing Kata every pass.
func TestQueueBacksOffRouteChecksWhileRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		q, _, _, k, _, g := queueFixture(t, operation("a", "comment", Payload{Ref: "issue", Body: "report"}))
		q.clock = RealClock{}
		g.err = errors.New("access_lost: kata not_found")
		k.comment = func(context.Context, string, string, string) error {
			t.Error("report sent through a refused route")
			return nil
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- q.Run(ctx) }()
		time.Sleep(10 * time.Minute)
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		// Doubling from two seconds reaches the five-minute cap in about
		// four minutes; the rest of the ten minutes holds a few more checks.
		if g.calls > 15 {
			t.Fatalf("refused lane made %d route checks in ten minutes, want at most 15", g.calls)
		}
	})
}
