package kata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

const carrierUID = "01J00000000000000000000020"
const carrierKey = "cron-worker:01J00000000000000000000010:alert:2026-11-02"

func carrierReplies() (map[string]any, map[string]any) {
	issue := map[string]any{"uid": carrierUID, "short_id": "0020", "project_uid": "01J00000000000000000000001", "title": "Example carrier", "body": "Stable carrier body", "author": "worker", "status": "open", "metadata": map[string]any{"teammate": "herdr-kata"}}
	payload := map[string]any{"uid": carrierUID, "title": "Example carrier", "body": "Stable carrier body", "author": "worker", "status": "open", "metadata": map[string]any{"teammate": "herdr-kata"}, "idempotency_key": carrierKey, "idempotency_fingerprint": strings.Repeat("a", 64)}
	event := map[string]any{"uid": "01J00000000000000000000030", "issue_uid": carrierUID, "project_uid": issue["project_uid"], "project_name": "example-project", "type": "issue.created", "actor": "worker", "payload": payload}
	return map[string]any{"issue": issue, "original_event": event}, map[string]any{"issue": issue, "labels": []any{}, "links": []any{}}
}

func carrierClient(t *testing.T, create, show map[string]any, code string) (Kata, string) {
	t.Helper()
	dir := t.TempDir()
	// The actual CLI encodes db.Event.Payload as a JSON string.
	for _, name := range []string{"event", "original_event"} {
		if event, ok := create[name].(map[string]any); ok {
			b, err := json.Marshal(event["payload"])
			if err != nil {
				t.Fatal(err)
			}
			event["payload"] = string(b)
		}
	}
	for name, value := range map[string]any{"create": create, "show": show} {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(dir, "kata")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$FIXTURE_CALLS"
shift 9
case "$1" in
 create) cat "$FIXTURE_CREATE"; exit "$FIXTURE_CODE";;
 show) cat "$FIXTURE_SHOW";;
 *) exit 3;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for key, val := range map[string]string{"FIXTURE_CALLS": filepath.Join(dir, "calls"), "FIXTURE_CREATE": filepath.Join(dir, "create"), "FIXTURE_SHOW": filepath.Join(dir, "show"), "FIXTURE_CODE": code} {
		t.Setenv(key, val)
	}
	return New(bin, "fixture", nil, "example-project", "worker"), filepath.Join(dir, "calls")
}

func resolveCarrier(t *testing.T, k Kata) (string, error) {
	t.Helper()
	c, ok := k.(interface {
		CreateAlertCarrier(string, string, string) (string, error)
	})
	if !ok {
		t.Fatal("Kata client has no verified daily carrier create")
	}
	return c.CreateAlertCarrier("Example carrier", "Stable carrier body", carrierKey)
}

// A replay must prove original intent and use an explicit nil priority.
func TestAlertCarrierVerifiedReplay(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		create, show := carrierReplies()
		if fresh {
			create["event"] = create["original_event"]
			delete(create, "original_event")
		}
		k, calls := carrierClient(t, create, show, "0")
		ref, err := resolveCarrier(t, k)
		if err != nil || ref != carrierUID {
			t.Fatalf("carrier %q: %v", ref, err)
		}
		args, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(args), "--priority\nnone\n") || !strings.Contains(string(args), "show\n"+carrierUID+"\n") {
			t.Fatalf("unverified/defaulted carrier calls: %s", args)
		}
	}
}

// Current matching text cannot make unrelated immutable history safe.
func TestAlertCarrierRefusesUnprovenIdentity(t *testing.T) {
	cases := map[string]func(map[string]any, map[string]any){
		"missing event":    func(c, s map[string]any) { delete(c, "original_event") },
		"wrong event kind": func(c, s map[string]any) { c["original_event"].(map[string]any)["type"] = "issue.edited" },
		"wrong project":    func(c, s map[string]any) { c["original_event"].(map[string]any)["project_name"] = "another-project" },
		"wrong event issue": func(c, s map[string]any) {
			c["original_event"].(map[string]any)["issue_uid"] = "01J00000000000000000000099"
		},
		"wrong actor": func(c, s map[string]any) { c["original_event"].(map[string]any)["actor"] = "another-worker" },
		"wrong key": func(c, s map[string]any) {
			c["original_event"].(map[string]any)["payload"].(map[string]any)["idempotency_key"] = "another-key"
		},
		"mutated original body": func(c, s map[string]any) {
			c["original_event"].(map[string]any)["payload"].(map[string]any)["body"] = "Unrelated original intent"
		},
		"original numeric priority": func(c, s map[string]any) {
			c["original_event"].(map[string]any)["payload"].(map[string]any)["priority"] = 2
		},
		"missing fingerprint": func(c, s map[string]any) {
			delete(c["original_event"].(map[string]any)["payload"].(map[string]any), "idempotency_fingerprint")
		},
		"closed":               func(c, s map[string]any) { s["issue"].(map[string]any)["status"] = "closed" },
		"changed current body": func(c, s map[string]any) { s["issue"].(map[string]any)["body"] = "Changed body" },
		"assigned":             func(c, s map[string]any) { s["issue"].(map[string]any)["owner"] = "another-worker" },
		"changed teammate": func(c, s map[string]any) {
			s["issue"].(map[string]any)["metadata"] = map[string]any{"teammate": "another-worker"}
		},
		"changed priority": func(c, s map[string]any) { s["issue"].(map[string]any)["priority"] = 2 },
		"related":          func(c, s map[string]any) { s["links"] = []any{map[string]any{"type": "related"}} },
		"labelled":         func(c, s map[string]any) { s["labels"] = []any{map[string]any{"name": "another-label"}} },
		"missing links":    func(c, s map[string]any) { delete(s, "links") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			create, show := carrierReplies()
			mutate(create, show)
			k, _ := carrierClient(t, create, show, "0")
			if ref, err := resolveCarrier(t, k); err == nil || ref != "" || Classify(err) != Definitive {
				t.Fatalf("accepted unproven carrier %q: %v", ref, err)
			}
		})
	}
}

func TestAlertCarrierNeverAdoptsConflict(t *testing.T) {
	create, show := carrierReplies()
	k, calls := carrierClient(t, create, show, "5")
	if ref, err := resolveCarrier(t, k); ref != "" || err == nil || Classify(err) != Conflict {
		t.Fatalf("adopted conflict %q: %v", ref, err)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "show\n") {
		t.Fatalf("conflict triggered adoption: %s", b)
	}
}

func TestAlertCarrierRejectsArbitraryOriginalBodies(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		changed := "Changed intent: " + hegel.Draw(ht, hegel.Text())
		create, show := carrierReplies()
		create["original_event"].(map[string]any)["payload"].(map[string]any)["body"] = changed
		k, _ := carrierClient(t, create, show, "0")
		if ref, err := resolveCarrier(t, k); err == nil || ref != "" {
			ht.Fatalf("adopted changed original body %q: %v", ref, err)
		}
	})
}
