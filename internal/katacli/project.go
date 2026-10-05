package katacli

import (
	"context"
	"errors"
)

// ProjectID reconciles ordinary numeric read identity with the installation's
// portable project UID. It is scoped to this routed read, never persisted.
func (c *Client) ProjectID(ctx context.Context, expectedUID string) (int64, error) {
	if id, err := NormalizeUID(expectedUID); err != nil || id != expectedUID {
		return 0, errors.New("configured project UID must be canonical")
	}
	var out struct {
		Project struct {
			ID  int64  `json:"id"`
			UID string `json:"uid"`
		} `json:"project"`
	}
	if err := c.Call(ctx, []string{"projects", "show", "--", c.Target.Project}, nil, &out); err != nil {
		return 0, err
	}
	if out.Project.ID <= 0 || out.Project.UID != expectedUID {
		return 0, errors.New("selected ordinary project identity differs from the configured binding")
	}
	return out.Project.ID, nil
}
