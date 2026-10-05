package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

func TestNativePruneSkipsOpaquePeerButRefusesOfflineSnapshot(t *testing.T) {
	s, _, _ := policyProduct(t)
	at := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	j := policyJob(t, s, store.Job{Name: "Finished", Prompt: "Inspect", Schedule: store.ScheduleOnce, RunAt: &at})
	_, rec := fix2SavedRun(t, s, j, "done")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(j.NativeDefinition, &raw); err != nil {
		t.Fatal(err)
	}
	raw["options"] = json.RawMessage(`{"herdr":{"Persistent":"peer-option"}}`)
	document, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := katacli.NewDraft("job", "", "Opaque peer", document, "")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.Native.Save(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := pruneJobs(t.Context(), s, &out, true); err != nil {
		t.Fatalf("opaque peer blocked healthy prune: %v", err)
	}
	if !strings.Contains(out.String(), peer.UID) || !strings.Contains(out.String(), "skipped") {
		t.Fatalf("missing skipped peer diagnostic: %s", out.String())
	}
	defs, err := s.Native.Client.Definitions(t.Context(), "job", false)
	if err != nil || len(defs) != 1 || defs[0].UID != peer.UID {
		t.Fatalf("wrong prune survivors: %+v %v", defs, err)
	}
	if _, err := s.Run(t.Context(), rec.ID); err != nil {
		t.Fatal("prune removed run history:", err)
	}
	s.Native.Client.Executable = filepath.Join(t.TempDir(), "unavailable")
	if err := pruneJobs(t.Context(), s, &out, true); err == nil {
		t.Fatal("transport failure accepted cached pruning")
	}
}
