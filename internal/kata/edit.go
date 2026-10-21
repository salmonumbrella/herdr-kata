package kata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// SaveJob preserves the editor's revision guard; a failed write is never retried.
func (c *CLI) SaveJob(ctx context.Context, j Job, parent string) (Job, error) {
	if j.UID == "" {
		return Job{}, fmt.Errorf("retained job UID is required")
	}
	args := []string{"cron", "job", "create", "--uid", j.UID}
	if j.DefinitionEventUID != "" {
		args = []string{"cron", "job", "update", j.UID, "--expected-event-uid", j.DefinitionEventUID}
	}

	args = append(args, "--file", "-")
	if parent != "" {
		args = append(args, "--parent", parent)
	}
	body, err := json.Marshal(struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
	}{j.Name, j.Definition})
	if err != nil {
		return Job{}, err
	}
	var v struct {
		Job Job `json:"job"`
	}
	err = c.call(ctx, body, &v, args...)
	return v.Job, err
}
func (c *CLI) DeleteJob(ctx context.Context, j Job) error {
	if j.UID == "" || j.DefinitionEventUID == "" {
		return fmt.Errorf("job UID and definition event UID are required")
	}
	return c.call(ctx, nil, nil, "cron", "job", "delete", j.UID, "--expected-event-uid", j.DefinitionEventUID)
}

func (c *CLI) SaveWorkflow(ctx context.Context, w Workflow) (Workflow, error) {
	if w.UID == "" {
		return Workflow{}, fmt.Errorf("retained workflow UID is required")
	}
	args := []string{"cron", "workflow", "create", "--uid", w.UID}
	if w.DefinitionEventUID != "" {
		args = []string{"cron", "workflow", "update", w.UID, "--expected-event-uid", w.DefinitionEventUID}
	}
	body, err := json.Marshal(struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
	}{w.Name, w.Definition})
	if err != nil {
		return Workflow{}, err
	}
	var v struct {
		Workflow Workflow `json:"workflow"`
	}
	err = c.call(ctx, body, &v, append(args, "--file", "-")...)
	return v.Workflow, err
}

// SameDefinition compares readback without rounding opaque JSON numbers.
func SameDefinition(a, b Job) bool {
	var x, y any
	first := json.NewDecoder(bytes.NewReader(a.Definition))
	first.UseNumber()
	second := json.NewDecoder(bytes.NewReader(b.Definition))
	second.UseNumber()
	return a.UID == b.UID && a.Name == b.Name && a.DeletedAt == nil && b.DeletedAt == nil && first.Decode(&x) == nil && second.Decode(&y) == nil && reflect.DeepEqual(x, y)
}
