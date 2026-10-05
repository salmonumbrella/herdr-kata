package board

import (
	"errors"
	"testing"
)

func TestFlowErrorTableRetainsBasenameAndInspectorPath(t *testing.T) {
	for _, path := range []string{"/home/worker/flows/broken.yml", "relative/flows/broken.yml", "/home/worker/my: files/broken.yml"} {
		err := errors.New(path + ": yaml: parse failure\n  line 2: expected key")
		if got := flowErrorLine(err); got != "! broken.yml: yaml: parse failure line 2: expected key" {
			t.Errorf("table for %q: %q", path, got)
		}
		full, _ := flowErrorParts(err)
		if full != path {
			t.Errorf("inspector path=%q want %q", full, path)
		}
	}
}
