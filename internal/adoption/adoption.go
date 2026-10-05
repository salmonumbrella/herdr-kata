// Package adoption converts explicit upstream snapshots into dormant native
// definitions. It never imports authority, runtime handles or execution history.
package adoption

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/flow"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

type Plan struct {
	Drafts   []katacli.Draft
	Warnings []string
}

// StableUID binds an operator-retained source namespace, resource and upstream
// item identity. Length framing avoids separator ambiguity; content/path changes
// keep the same create UID and require explicit ordinary CAS editing later.
func StableUID(source, resource, item string) string {
	hash := sha256.New()
	for _, part := range []string{"herdr-kata-upstream-import-v1", source, resource, item} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		hash.Write(size[:])
		hash.Write([]byte(part))
	}
	n := new(big.Int).SetBytes(hash.Sum(nil)[:16])
	base := big.NewInt(32)
	rem := new(big.Int)
	var result [26]byte
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for i := 25; i >= 0; i-- {
		n.QuoRem(n, base, rem)
		result[i] = alphabet[rem.Int64()]
	}
	return string(result[:])
}

// Build validates the entire input before any native save. Legacy directories
// are operator-supplied offline snapshots, never the current installation store.
func Build(ctx context.Context, dir, source, checkout, zone string, r *store.NativeRepository) (Plan, error) {
	var plan Plan
	if strings.TrimSpace(source) == "" || len(source) > 128 || strings.ContainsAny(source, "\x00\r\n") {
		return plan, errors.New("import requires a retained --source-id of 1–128 bytes on one line")
	}
	if strings.TrimSpace(checkout) == "" {
		return plan, errors.New("import requires an explicit --checkout-key; source paths are not adopted")
	}
	if r == nil || r.Client == nil {
		return plan, store.ErrNativeUnconfigured
	}
	info, err := os.Stat(dir)
	if err != nil {
		return plan, err
	}
	if !info.IsDir() {
		return plan, errors.New("source must be an offline snapshot directory")
	}
	flows, bad := flow.List(filepath.Join(dir, "flows"))
	if err := errors.Join(bad...); err != nil {
		return plan, err
	}
	flowUIDs := map[string]string{}
	warn := func(label, text string) {
		lower := strings.ToLower(text)
		for _, marker := range []string{"bermuda issue", "bermuda forum", "bermuda memory", "herdr-kata issue", "herdr-kata forum", "herdr-kata memory"} {
			if strings.Contains(lower, marker) {
				plan.Warnings = append(plan.Warnings, label+": obsolete collaboration prompt; review against ordinary Kata commands before execution")
				break
			}
		}
	}
	for _, f := range flows {
		uid := StableUID(source, "flow", f.ID)
		flowUIDs[f.ID] = uid
		f.NativeName = f.ID
		draft, err := flow.NativeDraft(f, uid, "")
		if err != nil {
			return plan, err
		}
		// Kata omits empty optional flow text from canonical responses. Emit the
		// same portable shape so retained create readback stays exact.
		var body map[string]json.RawMessage
		if err := katacli.Decode(draft.Definition, &body); err != nil {
			return plan, err
		}
		if f.About == "" {
			delete(body, "about")
		}
		if f.Input == "" {
			delete(body, "input")
		}
		draft.Definition, err = json.Marshal(body)
		if err != nil {
			return plan, err
		}
		plan.Drafts = append(plan.Drafts, draft)
		warn("flow "+f.ID, f.About)
		for _, step := range f.Steps {
			warn("flow "+f.ID+" step "+step.ID, step.Agent+"\n"+step.Run)
		}
	}
	database, err := sourceDatabase(dir)
	if err != nil {
		return plan, err
	}
	var jobs []store.Job
	if database != "" {
		jobs, err = store.ReadLegacyJobs(ctx, database)
		if err != nil {
			return plan, err
		}
	} else if len(flows) == 0 {
		return plan, errors.New("source has no recognized bermuda.db/herdr-kata.db or flow YAML")
	}
	for _, j := range jobs {
		oldID := j.ID
		j.ID = StableUID(source, "job", oldID)
		j.Enabled = false
		if j.Name == "" {
			j.Name = oldID
		}
		if j.Timeout == 0 {
			j.Timeout = 15 * time.Minute
		}
		if j.Schedule == store.ScheduleCron {
			if zone == "" {
				return plan, fmt.Errorf("job %s requires explicit --cron-timezone", oldID)
			}
			if _, err := time.LoadLocation(zone); err != nil {
				return plan, err
			}
		}
		if j.CWD != "" {
			plan.Warnings = append(plan.Warnings, "job "+oldID+": source checkout omitted; review local checkout mapping before activation")
		}
		j.CWD = ""
		j.CheckoutKey = checkout
		if j.Flow != "" {
			uid, ok := flowUIDs[j.Flow]
			if !ok {
				return plan, fmt.Errorf("job %s references missing upstream flow %s", oldID, j.Flow)
			}
			j.Flow = uid
		}
		draft, err := r.JobDraft(j)
		if err != nil {
			return plan, fmt.Errorf("job %s: %w", oldID, err)
		}
		if j.Schedule == store.ScheduleCron {
			var body map[string]any
			if err := katacli.Decode(draft.Definition, &body); err != nil {
				return plan, err
			}
			body["trigger"].(map[string]any)["timezone"] = zone
			draft.Definition, err = json.Marshal(body)
			if err != nil {
				return plan, err
			}
		}
		plan.Drafts = append(plan.Drafts, draft)
		warn("job "+oldID, j.Prompt)
	}
	return plan, nil
}

// Both names are recognized explicitly: original upstream and the renamed fork.
// Lstat retains broken/missing-target symlinks as errors in the reader instead of
// silently interpreting a recognized source as a flow-only snapshot.
func sourceDatabase(dir string) (string, error) {
	var selected string
	for _, name := range []string{"bermuda.db", "herdr-kata.db"} {
		candidate := filepath.Join(dir, name)
		if _, err := os.Lstat(candidate); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if selected != "" {
			return "", errors.New("source contains both bermuda.db and herdr-kata.db; provide a snapshot with exactly one database")
		}
		selected = candidate
	}
	return selected, nil
}
