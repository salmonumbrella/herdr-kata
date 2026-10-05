package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/adoption"
)

func nativeImport(ctx context.Context, argv []string) error {
	fs := flag.NewFlagSet("native import", flag.ContinueOnError)
	source := fs.String("source", "", "stopped upstream snapshot directory (never opened for writing)")
	sourceID := fs.String("source-id", "", "retain this namespace on every retry and after moving the snapshot")
	checkout := fs.String("checkout-key", "", "local checkout key to review before activation")
	zone := fs.String("cron-timezone", "", "explicit IANA timezone for upstream cron jobs")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if *source == "" || *sourceID == "" || *checkout == "" || fs.NArg() != 0 {
		return errors.New("usage: native import --source <snapshot-dir> --source-id <retained-namespace> --checkout-key <key> [--cron-timezone <IANA-zone>]")
	}
	sourcePath, err := filepath.Abs(*source)
	if err != nil {
		return err
	}
	destination, err := filepath.Abs(stateDir())
	if err != nil {
		return err
	}
	// Compare filesystem identities before openStore can create/migrate anything.
	resolvedSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return err
	}
	resolvedDestination, err := importCanonicalPath(destination)
	if err != nil {
		return err
	}
	if importPathsOverlap(resolvedSource, resolvedDestination) {
		return errors.New("source must be a separate offline snapshot, not the local installation store")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	plan, err := adoption.Build(ctx, sourcePath, *sourceID, *checkout, *zone, s.Native)
	if err != nil {
		return err
	}
	// Emit review warnings before the first write and retain them on partial failure.
	for _, warning := range plan.Warnings {
		fmt.Fprintln(os.Stdout, "warning:", warning)
	}
	for _, draft := range plan.Drafts {
		definition, err := s.Native.Save(ctx, draft)
		if err != nil {
			return fmt.Errorf("import %s %s failed; retry with the same source-id (accepted definitions stay dormant): %w", draft.Resource, draft.UID, err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"resource": draft.Resource, "uid": definition.UID, "event_uid": definition.DefinitionEventUID, "activation": "unchanged"}); err != nil {
			return err
		}
	}
	return nil
}

// Resolve existing symlink parents even when the destination is not created yet.
func importCanonicalPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = importCanonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}
func importPathsOverlap(a, b string) bool {
	contains := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	return contains(a, b) || contains(b, a)
}
