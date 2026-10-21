// Package usage collects optional local usage evidence without controlling runs.
package usage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheCreationTokens int64
	CostMicroUSD                                                    *int64 `json:",omitempty"`
	Found                                                           bool
	Source                                                          string `json:",omitempty"`
	// Partial marks a run total that lacks at least one attempt's data.
	Partial bool `json:",omitempty"`
}
type Request struct {
	Session                        *herdr.AgentSession
	Agent, Cwd, PromptPath         string
	LaunchedAt, StartedAt, EndedAt time.Time
	// Env reads the agent's own environment; nil reads the worker's.
	Env agentlimit.Env
}

func Collect(ctx context.Context, req Request) Usage {
	remote := agentsviewUsage(ctx, req)
	if remote.Found {
		return remote
	}
	local := transcriptUsage(req)
	if remote.CostMicroUSD != nil {
		local.CostMicroUSD = remote.CostMicroUSD
	}
	return local
}
func Add(a *Usage, b Usage) *Usage {
	u := b
	if a != nil {
		u.InputTokens += a.InputTokens
		u.OutputTokens += a.OutputTokens
		u.CacheReadTokens += a.CacheReadTokens
		u.CacheCreationTokens += a.CacheCreationTokens
		u.Partial = u.Partial || a.Partial
		u.Found = u.Found || a.Found
		if a.CostMicroUSD != nil {
			cost := *a.CostMicroUSD
			if u.CostMicroUSD != nil {
				cost += *u.CostMicroUSD
			}
			u.CostMicroUSD = &cost
		}
		if u.Source == "" {
			u.Source = a.Source
		} else if a.Source != "" && a.Source != u.Source {
			u.Source = "mixed"
		}
	}
	return &u
}
func count(n int64) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return fmt.Sprint(n)
	case n < 1000000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0") + "k"
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000000), ".0") + "M"
	}
}
func Line(u *Usage) string {
	if u == nil {
		u = &Usage{}
	}
	return fmt.Sprintf("%s in · %s out · %s cache read · %s cache write", count(u.InputTokens), count(u.OutputTokens), count(u.CacheReadTokens), count(u.CacheCreationTokens))
}
