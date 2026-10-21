package daemon

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestStatusReadsLargePinMismatch(t *testing.T) {
	for _, count := range []int{100, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			f := workerFixtureFor(t)
			f.cfg.Kata.Env = map[string]string{}
			for i := range count {
				f.cfg.Kata.Env[fmt.Sprintf("KATA_OPTION_%05d", i)] = "PRIVATE_VALUE"
			}
			k := f.k.WithConfig(f.cfg.Kata, f.l.Root)
			guard := kata.NewRouteGuard(f.cfg, k, state.OSFS{}, f.l.Project("project"))
			if err := guard.Check(t.Context()); err == nil || guard.Allowed() {
				t.Fatal("large mismatch permitted")
			}
			s, err := ReadStatus(f.l)
			if err != nil || s.Route == nil || s.Route.Allowed {
				t.Fatal("mismatch made status unreadable", err)
			}
			for _, want := range []string{"kata.env.KATA_OPTION_00000", "restore the differing fields", "herdr-kata doctor --json"} {
				if !strings.Contains(s.Route.Error, want) {
					t.Errorf("status lost %q", want)
				}
			}
			if count == 10000 && !strings.Contains(s.Route.Error, "further differences omitted") {
				t.Error("large difference list was not marked abbreviated")
			}
			raw, err := os.ReadFile(f.l.Project("project").RouteFile())
			if err != nil || len(raw) >= 64<<10 || strings.Contains(string(raw), "PRIVATE_VALUE") {
				t.Fatal("route diagnostic unbounded or exposed environment value", len(raw), err)
			}
		})
	}
}
