package runner

import (
	"context"
	"strings"
	"uuid"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

const maxRunTranscripts = 64

// runTranscripts uses only journaled prompt occupants, including retained retry
// attempts. Historical occupants precede current steps; first occurrence wins.
func runTranscripts(r *Run) []kata.Transcript {
	links, _ := extractRunTranscripts(r)
	return links
}

func extractRunTranscripts(r *Run) ([]kata.Transcript, bool) {
	var links []kata.Transcript
	seen := map[kata.Transcript]bool{}
	incomplete := false
	add := func(s StepState, shared bool) {
		if !submittedPrompt(s) {
			return
		}
		ref := s.Herdr
		// Recovery and shared replacement can retain a submitted attempt
		// without removing its current step. Its retained copy already supplied
		// provenance above; today's shared occupant may be empty or unused.
		if ref == nil && shared {
			if retainedPromptAttempt(r, s) {
				return
			}
			// Older journals can lack a retained copy. A replacement cannot
			// identify a prompt submitted before that occupant was launched,
			// even after it accepts a later step's input.
			predatesOccupant := s.SubmittedAt != nil && !r.Herdr.LaunchedAt.IsZero() && s.SubmittedAt.Before(r.Herdr.LaunchedAt)
			if predatesOccupant || r.Herdr.HasSubmitted != nil && !*r.Herdr.HasSubmitted {
				incomplete = true
				return
			}
			// An unretained submitted current attempt uses the established
			// legacy/shared run reference. Historical attempts never do.
			ref = &r.Herdr
		}
		var session *herdr.AgentSession
		if ref != nil {
			session = ref.Session
		}
		link, ok := nativeTranscript(session)
		if !ok {
			incomplete = true
			return
		}
		if seen[link] {
			return
		}
		seen[link] = true
		if len(links) == maxRunTranscripts {
			incomplete = true
			return
		}
		links = append(links, link)
	}
	for _, s := range r.PastAttempts {
		add(s, false)
	}
	for _, s := range r.Steps {
		add(s, true)
	}
	return links, incomplete
}

func nativeTranscript(s *herdr.AgentSession) (kata.Transcript, bool) {
	if s == nil || s.Kind != "id" || s.Agent != "codex" && s.Agent != "claude" || len(s.Value) != 36 {
		return kata.Transcript{}, false
	}
	id, err := uuid.Parse(s.Value)
	if err != nil || !strings.EqualFold(s.Value, id.String()) {
		return kata.Transcript{}, false
	}
	return kata.Transcript{Agent: s.Agent, SessionID: id.String()}, true
}

func prepareObservation(ctx context.Context, k Kata, o kata.Observation) (kata.Observation, error) {
	if p, ok := k.(interface {
		PrepareObservation(context.Context, kata.Observation) (kata.Observation, error)
	}); ok {
		return p.PrepareObservation(ctx, o)
	}
	return o, nil
}

// Observation provenance accumulates in Kata. Read-back must contain the sent
// identities but may include earlier links and the first saved URL for each.
func observationProvenance(got kata.Run, want *kata.Observation) bool {
	if want.TranscriptsIncomplete && !got.TranscriptsIncomplete {
		return false
	}
	for _, link := range want.Transcripts {
		found := false
		for _, saved := range got.Transcripts {
			if saved.Agent == link.Agent && strings.EqualFold(saved.SessionID, link.SessionID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
