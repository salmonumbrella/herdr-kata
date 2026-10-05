package katacli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

type Capabilities struct {
	ProjectUID string   `json:"project_uid"`
	Features   []string `json:"event_features"`
}

// Capabilities reads advertised support without requiring or initializing
// cron definitions or run history. It never supplies execution grants or epochs.
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var out Capabilities
	if e := c.Call(ctx, []string{"cron", "capabilities"}, nil, &out); e != nil {
		return Capabilities{}, fmt.Errorf("native discovery failed; check the configured target/credentials or upgrade Kata: %w", e)
	}
	if !slices.Contains(out.Features, "cron_v1") {
		return Capabilities{}, errors.New("selected Kata daemon must advertise cron_v1; upgrade or configure the target")
	}
	normalized, e := NormalizeUID(out.ProjectUID)
	if e != nil || normalized != out.ProjectUID {
		return Capabilities{}, errors.New("invalid native project capability identity")
	}
	return out, nil
}

func (c *Client) cronCall(ctx context.Context, args []string, body json.RawMessage, out any) error {
	if _, e := c.Capabilities(ctx); e != nil {
		return e
	}
	return c.Call(ctx, args, body, out)
}
