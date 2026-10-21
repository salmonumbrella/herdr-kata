//go:build crashpoint

package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
)

func TestCommandCompletedOutputSurvivesDelayedCopier(t *testing.T) {
	for _, point := range []string{"command.output.start", "command.output.drained"} {
		t.Run(point, func(t *testing.T) { testCommandDelayedCopier(t, point) })
	}
}

func testCommandDelayedCopier(t *testing.T, pausePoint string) {
	t.Helper()
	ctx := t.Context()
	copied := make(chan struct{})
	release := make(chan struct{})
	// Delay either reading the closed pipe or publishing its EOF result.
	// Release the copier only after the watchdog has checked the pipe.
	crashpoint.Hook = func(point string) {
		switch point {
		case pausePoint:
			close(copied)
			select {
			case <-release:
			case <-ctx.Done():
			}
		case "command.output.timeout":
			select {
			case <-copied:
			case <-ctx.Done():
			}
			close(release)
		}
	}
	t.Cleanup(func() { crashpoint.Hook = nil })
	spec := commandSpec(t, "printf completed")
	receipt, err := RunCommand(ctx, spec)
	select {
	case <-release:
	default:
		t.Fatal("drain watchdog did not release the delayed notification")
	}
	if err != nil || receipt.ExitCode != 0 || receipt.Interrupted || receipt.GroupAlive || receipt.Uncertain {
		t.Fatalf("completed command became uncertain: receipt=%+v err=%v", receipt, err)
	}
	if saved := readReceipt(t, spec); !reflect.DeepEqual(saved, receipt) {
		t.Fatalf("saved=%+v returned=%+v", saved, receipt)
	}
	output, err := os.ReadFile(filepath.Join(spec.AttemptDir, "output.log"))
	if err != nil || string(output) != "completed" {
		t.Fatalf("output=%q err=%v", output, err)
	}
}
