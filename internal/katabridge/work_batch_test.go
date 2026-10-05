package katabridge

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"hegel.dev/go/hegel"
)

func TestWorkBatchRetainsFairProgressAndScopeWithoutChangingWork(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := strings.ToValidUTF8(hegel.Draw(ht, hegel.Text()), "\ufffd")
		b := WorkBatch{Dir: t.TempDir(), Name: "settlements", Teammate: "child", Scope: Scope{TargetKey: "target:" + text, ProjectUID: "project", Actor: "worker"}}
		keys := make([]string, 101)
		for i := range keys {
			keys[i] = fmt.Sprintf("work%03d:", i) + text
		}
		before, _ := json.Marshal(keys)
		materialized, _ := json.Marshal(workPosition{Version: 1, Scope: b.Scope, Name: b.Name, Teammate: b.Teammate, After: keys[99]})
		first, err := b.Select(keys, 100)
		if len(materialized) > 98304 {
			if err == nil {
				ht.Fatal("oversized cursor accepted")
			}
			return
		}
		if err != nil || len(first) != 100 {
			ht.Fatal("first work batch failed")
		}
		restarted := WorkBatch{Dir: b.Dir, Name: b.Name, Teammate: b.Teammate, Scope: b.Scope}
		second, err := restarted.Select(keys, 100)
		if err != nil || len(second) != 100 {
			ht.Fatal("restarted work batch failed")
		}
		seen := map[int]bool{}
		for _, batch := range [][]int{first, second} {
			unique := map[int]bool{}
			for _, index := range batch {
				if index < 0 || index >= len(keys) || unique[index] {
					ht.Fatal("batch returned duplicate or invalid work")
				}
				unique[index], seen[index] = true, true
			}
		}
		if len(seen) != len(keys) {
			ht.Fatal("persistently retained prefix starved later work")
		}
		for _, changed := range []string{"actor", "teammate", "name"} {
			other := restarted
			switch changed {
			case "actor":
				other.Scope.Actor = "another-worker"
			case "teammate":
				other.Teammate = "another-child"
			case "name":
				other.Name = "history"
			}
			batch, err := other.Select(keys, 100)
			if err != nil || batch[0] != 0 {
				ht.Fatalf("local progress crossed %s scope", changed)
			}
		}
		after, _ := json.Marshal(keys)
		if string(before) != string(after) {
			ht.Fatal("selection changed frozen work identities")
		}
	})
}
