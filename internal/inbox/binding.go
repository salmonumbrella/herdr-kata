package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type BindingReader interface {
	ListWorkflows(context.Context) ([]kata.Workflow, error)
	ShowWorkflow(context.Context, string) (kata.Workflow, error)
}

func ResolveBinding(ctx context.Context, reader BindingReader, parent kata.IssueView, cfg config.Config) (Binding, error) {
	b := Binding{Profile: "default", ParentRef: kata.IssueReference(cfg.Kata.Project, parent)}
	profile, err := metadataString(parent, "herdr_kata.profile")
	if err != nil {
		return Binding{}, err
	}
	if profile != "" {
		b.Profile = profile
	}
	if _, ok := cfg.Profiles[b.Profile]; !ok {
		return Binding{}, fmt.Errorf("%w: unknown profile %q", ErrUnsupported, b.Profile)
	}
	checkout, err := metadataString(parent, "herdr_kata.checkout")
	if err != nil {
		return Binding{}, err
	}
	if checkout != "" {
		cwd, ok := cfg.Checkouts[checkout]
		if !ok || !filepath.IsAbs(cwd) {
			return Binding{}, fmt.Errorf("%w: unknown checkout %q", ErrUnsupported, checkout)
		}
		b.Cwd = cwd
	}
	ref, err := metadataString(parent, "herdr_kata.workflow")
	if err != nil {
		return Binding{}, err
	}
	if ref == "" {
		return b, nil
	} // nil WFDef selects the child's default prompt.
	uid := ref
	if !validUID(ref) {
		rows, err := reader.ListWorkflows(ctx)
		if err != nil {
			return Binding{}, err
		}
		uid = ""
		for _, w := range rows {
			if w.Name != ref || w.DeletedAt != nil {
				continue
			}
			if uid != "" {
				return Binding{}, fmt.Errorf("%w: ambiguous workflow %q", ErrUnsupported, ref)
			}
			uid = w.UID
		}
		if uid == "" {
			return Binding{}, fmt.Errorf("%w: unknown workflow %q", ErrUnsupported, ref)
		}
	}
	w, err := reader.ShowWorkflow(ctx, uid)
	if err != nil {
		var ke *kata.Error
		if errors.As(err, &ke) && kata.NotFound(err) {
			rows, proofErr := reader.ListWorkflows(ctx)
			if proofErr == nil {
				absent := true
				for _, row := range rows {
					if row.UID == uid {
						absent = false
					}
				}
				if absent {
					return Binding{}, fmt.Errorf("%w: unknown workflow %q", ErrUnsupported, ref)
				}
			}
			kata.ProbeAccess(ctx, reader)
		}
		return Binding{}, err
	}
	if w.DeletedAt != nil || w.UID != uid || !validUID(w.UID) || !validUID(w.DefinitionEventUID) || (!validUID(ref) && w.Name != ref) {
		return Binding{}, fmt.Errorf("%w: missing, deleted or changed workflow", ErrUnsupported)
	}
	d, err := defs.DecodeWorkflow(w.Definition)
	if err != nil {
		return Binding{}, fmt.Errorf("%w: invalid workflow: %v", ErrUnsupported, err)
	}
	// Reuse installation validation, including topology and per-step profiles.
	options, _ := json.Marshal(map[string]string{"agent": b.Profile})
	j := defs.JobDef{Version: 1, Kind: "job", Enabled: true, Trigger: defs.Trigger{Kind: "manual"}, Action: defs.Action{Kind: "execute", WorkflowUID: uid}, Issue: &defs.IssuePolicy{Kind: "existing", UID: parent.UID}, Overlap: "allow", Catchup: "skip", Options: options}
	_, support := defs.Classify(j, &d, cfg)
	if !support.Supported {
		return Binding{}, fmt.Errorf("%w: %s", ErrUnsupported, strings.Join(support.Reasons, "; "))
	}
	b.WorkflowUID, b.WorkflowEventUID, b.WFDef = w.UID, w.DefinitionEventUID, &d
	b.WorkflowName = w.Name
	return b, nil
}

func metadataString(issue kata.IssueView, key string) (string, error) {
	raw, ok := issue.Metadata[key]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%w: %s must be a nonempty string", ErrUnsupported, key)
	}
	return value, nil
}
