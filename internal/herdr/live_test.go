//go:build herdr_live

package herdr_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

// This opt-in smoke sends only ping and agent.list. No names or prompts are
// printed, so its output is suitable for a public pull request.
func TestReadOnlySmoke(t *testing.T) {
	path := os.Getenv("HERDR_KATA_TEST_HERDR_SOCKET")
	if path == "" {
		t.Fatal("HERDR_KATA_TEST_HERDR_SOCKET is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := &herdr.Client{Path: path}
	p, err := c.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := c.AgentList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sessions, seqs := 0, 0
	for _, a := range agents {
		if a.Session != nil {
			sessions++
		}
		if a.CompletionSeq != nil {
			seqs++
		}
	}
	t.Logf("ping: version=%s protocol=%d", p.Version, p.Protocol)
	t.Logf("agent.list: agents=%d with_session=%d with_completion_seq=%d", len(agents), sessions, seqs)
}
