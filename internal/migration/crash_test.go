//go:build crashpoint

package migration

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestMigrationCrashRecovery(t *testing.T) {
	for _, point := range []string{"migration-intent", "migration-config-empty-removed", "migration-state-empty-removed", "migration-config-moved", "migration-config-linked", "migration-state-moved", "migration-state-linked", "migration-complete"} {
		t.Run(point, func(t *testing.T) {
			cfg, data, oldCfg, oldData := fixture(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestMigrationCrashHelper$")
			cmd.Env = append(os.Environ(), "HK_MIGRATION_CONFIG="+cfg, "HK_MIGRATION_STATE="+data, "HK_MIGRATION_CRASH="+point)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("did not crash at %s: %s", point, output)
			}
			status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("unexpected child exit: %v %s", err, output)
			}
			if err := Ensure(cfg, data); err != nil {
				t.Fatal(err)
			}
			content(t, filepath.Join(cfg, "config.toml"), "executor = \"worker\"\n")
			content(t, filepath.Join(oldCfg, "config.toml"), "executor = \"worker\"\n")
			for _, root := range []string{data, oldData} {
				content(t, filepath.Join(root, "runs/run/run.json"), `{"version":1,"uid":"run","phase":"settled"}`)
				content(t, filepath.Join(root, "projects/project/lease.json"), `{"held":true,"generation":7}`)
			}
			lock, ok, err := state.TryLock(filepath.Join(data, "worker.lock"))
			if err != nil || !ok {
				t.Fatal(err)
			}
			defer lock.Unlock()
			if held, err := state.Held(filepath.Join(oldData, "worker.lock")); err != nil || !held {
				t.Fatalf("rollback escaped lock: %t %v", held, err)
			}
		})
	}
}
func TestMigrationCrashHelper(t *testing.T) {
	point := os.Getenv("HK_MIGRATION_CRASH")
	if point == "" {
		return
	}
	crashpoint.Hook = func(got string) {
		if got == point {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		}
	}
	if err := Ensure(os.Getenv("HK_MIGRATION_CONFIG"), os.Getenv("HK_MIGRATION_STATE")); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crashpoint was not reached")
}
