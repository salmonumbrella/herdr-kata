package kata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
)

// heldHub admits every call, records it on entered, then holds it until
// released, as a hub under load does.
func heldHub(entered chan<- string, release <-chan struct{}) Runner {
	return func(ctx context.Context, argv, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
		entered <- argv[3]
		select {
		case <-ctx.Done():
			return nil, nil, 1, ctx.Err()
		case <-release:
		}
		return []byte(`{"schema_version":33}`), nil, 0, nil
	}
}

func laneCLI(project string, r Runner) *CLI {
	return New(config.Kata{Bin: "/bin/kata", Project: project, Actor: "worker"}, "/state", r, nil)
}

// Lanes have separate CLIs, but one runtime sends at most four one-shot
// calls to Kata at a time. The fifth starts only when one finishes.
func TestOneShotCallsShareRuntimeCeiling(t *testing.T) {
	entered := make(chan string, 8)
	release := make(chan struct{})
	r := heldHub(entered, release)
	done := make(chan error, 8)
	for _, project := range []string{"project-0", "project-1", "project-2", "project-3", "project-4", "project-5"} {
		go func() { _, err := laneCLI(project, r).Health(t.Context()); done <- err }()
	}
	for range 4 {
		<-entered
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case project := <-entered:
		close(release)
		t.Fatalf("fifth concurrent call reached Kata for %s", project)
	case <-timer.C:
	}
	release <- struct{}{}
	<-entered
	close(release)
	for range 6 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// A call still queued for a slot when its caller gives up never reaches
// Kata, so a write abandoned in the queue was not sent.
func TestQueuedCallCancelledBeforeKata(t *testing.T) {
	entered := make(chan string, 8)
	release := make(chan struct{})
	defer close(release)
	r := heldHub(entered, release)
	for _, project := range []string{"project-0", "project-1", "project-2", "project-3"} {
		go func() { _, _ = laneCLI(project, r).Health(t.Context()) }()
	}
	for range 4 {
		<-entered
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := laneCLI("queued", r).Health(ctx); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued call returned %v, want cancellation", err)
	}
	select {
	case project := <-entered:
		t.Fatalf("cancelled call reached Kata for %s", project)
	default:
	}
}
