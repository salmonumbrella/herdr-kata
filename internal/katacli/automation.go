package katacli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
)

func (c *Client) Definitions(ctx context.Context, resource string, deleted bool) ([]Definition, error) {
	if resource != "job" && resource != "flow" {
		return nil, errors.New("unknown definition resource")
	}
	args := []string{"automation", resource, "list"}
	if deleted {
		args = append(args, "--include-deleted")
	}
	var out map[string]json.RawMessage
	if e := c.automationCall(ctx, args, nil, &out); e != nil {
		return nil, e
	}
	var defs []Definition
	if e := Decode(out[resource+"s"], &defs); e != nil {
		return nil, e
	}
	return defs, nil
}
func (c *Client) Definition(ctx context.Context, resource, uid string) (Definition, error) {
	if resource != "job" && resource != "flow" {
		return Definition{}, errors.New("unknown definition resource")
	}
	uid, e := NormalizeUID(uid)
	if e != nil {
		return Definition{}, e
	}
	var out map[string]json.RawMessage
	if e := c.automationCall(ctx, []string{"automation", resource, "show", uid}, nil, &out); e != nil {
		return Definition{}, e
	}
	var def Definition
	e = Decode(out[resource], &def)
	if e == nil && def.UID != uid {
		e = errors.New("Kata returned another definition identity")
	}
	return def, e
}
func exactJSON(a, b json.RawMessage) bool {
	var x, y any
	return Decode(a, &x) == nil && Decode(b, &y) == nil && reflect.DeepEqual(x, y)
}
func (c *Client) Save(ctx context.Context, d Draft) (Definition, error) {
	checked, e := NewDraft(d.Resource, d.UID, d.Name, d.Definition, d.ExpectedEventUID)
	if e != nil {
		return Definition{}, e
	}
	var response map[string]json.RawMessage
	e = c.automationCall(ctx, checked.Args(), checked.Body(c.Target.Actor), &response)
	if e != nil {
		// Readback recovers only a retained create whose exact whole document still
		// wins. Updates never turn stale revision failures into success.
		if checked.ExpectedEventUID == "" {
			current, readErr := c.Definition(ctx, checked.Resource, checked.UID)
			if readErr == nil && current.DeletedAt == nil && current.Name == checked.Name && exactJSON(current.Definition, checked.Definition) {
				return current, nil
			}
		}
		return Definition{}, e
	}
	var def Definition
	if e := Decode(response[checked.Resource], &def); e != nil {
		return Definition{}, e
	}
	if def.UID != checked.UID || def.DefinitionEventUID == "" {
		return Definition{}, errors.New("Kata returned an invalid definition receipt")
	}
	return def, nil
}
func (c *Client) DefinitionAction(ctx context.Context, resource, action, uid, expected string) (Definition, error) {
	if resource != "job" && resource != "flow" || action != "delete" && action != "restore" {
		return Definition{}, errors.New("unknown definition action")
	}
	uid, e := NormalizeUID(uid)
	if e != nil {
		return Definition{}, e
	}
	expected, e = NormalizeUID(expected)
	if e != nil {
		return Definition{}, e
	}
	body, _ := json.Marshal(map[string]string{"actor": c.Target.Actor, "expected_event_uid": expected})
	var out map[string]json.RawMessage
	e = c.automationCall(ctx, []string{"automation", resource, action, uid, "--expected-event-uid", expected, "--file", "-"}, body, &out)
	if e != nil {
		return Definition{}, e
	}
	var def Definition
	e = Decode(out[resource], &def)
	return def, e
}

func (c *Client) Runs(ctx context.Context, jobUID, before string, out any) error {
	args := []string{"automation", "run", "list", "--limit", "100"}
	if jobUID != "" {
		return errors.New("native run list has no job filter; filter canonical results locally")
	}
	if before != "" {
		uid, e := NormalizeUID(before)
		if e != nil {
			return e
		}
		args = append(args, "--before-uid", uid)
	}
	return c.automationCall(ctx, args, nil, out)
}
func (c *Client) Run(ctx context.Context, uid string, out any) error {
	uid, e := NormalizeUID(uid)
	if e != nil {
		return e
	}
	return c.automationCall(ctx, []string{"automation", "run", "show", uid}, nil, out)
}
