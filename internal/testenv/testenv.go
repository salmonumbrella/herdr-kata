// Package testenv checks prerequisites for explicitly selected integration suites.
package testenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func RequiredBinary(t testing.TB, variable string) string {
	t.Helper()
	path := os.Getenv(variable)
	if path == "" {
		t.Fatalf("%s must name an executable test binary", variable)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must name an absolute path: %s", variable, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", variable, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("%s is not an executable file: %s", variable, path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, path, "--version").Run(); err != nil {
		t.Fatalf("%s is unusable: %v", variable, err)
	}
	return path
}
