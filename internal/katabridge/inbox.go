package katabridge

import (
	"context"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
)

type Inbox struct {
	Recipient string    `json:"recipient"`
	Requests  []Request `json:"requests"`
}

func ReadInbox(ctx context.Context, c katacli.Client, recipient string) (Inbox, error) {
	var out Inbox
	if err := c.Inbox(ctx, recipient, &out); err != nil {
		return out, err
	}
	if out.Recipient != recipient {
		return out, errors.New("Kata inbox returned another recipient")
	}
	for _, r := range out.Requests {
		if r.Ref == "" {
			return out, errors.New("inbox request has no issue reference")
		}
		if r.Project != "" && r.Project != c.Target.Project {
			return out, errors.New("inbox request belongs to another project")
		}
	}
	return out, nil
}
