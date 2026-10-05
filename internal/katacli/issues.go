package katacli

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Ready delegates issue readiness to the selected daemon, retaining its civil
// date, recurrence, someday and blocking semantics. Zero means no list truncation;
// the ordinary subprocess output/time bounds still apply.
func (c *Client) Ready(ctx context.Context, out any) error {
	return c.Call(ctx, []string{"ready", "--limit", "0"}, nil, out)
}
func (c *Client) Show(ctx context.Context, ref string, out any) error {
	return c.Call(ctx, []string{"show", "--", ref}, nil, out)
}
func (c *Client) Create(ctx context.Context, title, body, key string, out any) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("retain an issue creation idempotency key before sending")
	}
	return c.Call(ctx, []string{"create", "--body-stdin", "--idempotency-key", key, "--", title}, json.RawMessage(body), out)
}

// CreateRunIssue uses ordinary request idempotency for one independent local run.
func (c *Client) CreateRunIssue(ctx context.Context, title, body, key string, metadata map[string]string, out any) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("retain an issue creation idempotency key before sending")
	}
	args := []string{"create", "--body-stdin", "--idempotency-key", key, "--force-new"}
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--meta", k+"="+metadata[k])
	}
	return c.Call(ctx, append(args, "--", title), json.RawMessage(body), out)
}

// Comments are included in the native show response; there is no private list API.
func (c *Client) Comments(ctx context.Context, ref string, out any) error {
	return c.Show(ctx, ref, out)
}
func (c *Client) Comment(ctx context.Context, ref, body, key string, out any) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("retain a comment idempotency key before sending")
	}
	return c.Call(ctx, []string{"comment", "--body-stdin", "--idempotency-key", key, "--", ref}, json.RawMessage(body), out)
}
func (c *Client) Inbox(ctx context.Context, recipient string, out any) error {
	if strings.TrimSpace(recipient) == "" {
		return errors.New("exact inbox recipient required")
	}
	return c.Call(ctx, []string{"inbox", "--for", recipient}, nil, out)
}
func (c *Client) Notify(ctx context.Context, ref, recipient, message string, clear bool, out any) error {
	if strings.TrimSpace(recipient) == "" {
		return errors.New("exact notification recipient required")
	}
	args := []string{"notify", "--to", recipient}
	if clear {
		args = append(args, "--clear")
	} else {
		args = append(args, "--message", message)
	}
	return c.Call(ctx, append(args, "--", ref), nil, out)
}
