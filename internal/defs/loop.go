package defs

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Loop struct {
	Goto     string   `json:"goto"`
	MaxLoops int      `json:"max_loops"`
	Checker  string   `json:"-"`
	Span     []string `json:"-"`
}

// WorkflowLoop validates the single backward edge against execution order.
func WorkflowLoop(w *WorkflowDef) (*Loop, error) {
	if w == nil {
		return nil, nil
	}
	order, e := TopoOrder(w.Steps)
	if e != nil {
		return nil, e
	}
	byKey := map[string]Step{}
	var edge *Loop
	for _, s := range w.Steps {
		byKey[s.Key] = s
		var opts map[string]json.RawMessage
		if len(s.Options) > 0 {
			if e = json.Unmarshal(s.Options, &opts); e != nil {
				return nil, e
			}
		}
		raw, ok := opts["on_fail"]
		if !ok {
			continue
		}
		if edge != nil {
			return nil, fmt.Errorf("only one on_fail edge is supported")
		}
		v := Loop{MaxLoops: 1, Checker: s.Key}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("on_fail must be an object")
		}
		if e = d.Decode(&v); e != nil {
			return nil, fmt.Errorf("on_fail: %w", e)
		}
		// JSON null must not turn an explicit limit into the default.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if bytes.Equal(bytes.TrimSpace(fields["max_loops"]), []byte("null")) || v.MaxLoops < 1 || v.MaxLoops > 8 {
			return nil, fmt.Errorf("max_loops must be 1–8")
		}
		edge = &v
	}
	if edge == nil {
		return nil, nil
	}
	start, end := -1, -1
	for i, k := range order {
		if k == edge.Goto {
			start = i
		}
		if k == edge.Checker {
			end = i
		}
	}
	if start < 0 || start >= end {
		return nil, fmt.Errorf("on_fail goto must name an earlier step")
	}
	edge.Span = append([]string(nil), order[start:end+1]...)
	for i, k := range edge.Span {
		s := byKey[k]
		if s.Retries != 0 {
			return nil, fmt.Errorf("loop span requires retries: 0")
		}
		if i > 0 && (len(s.After) != 1 || s.After[0] != edge.Span[i-1]) {
			return nil, fmt.Errorf("loop span must be linear")
		}
	}
	return edge, nil
}
