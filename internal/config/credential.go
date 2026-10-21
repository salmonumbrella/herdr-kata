package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/kit/safefileio"
	"go.kenn.io/kit/secretref"
)

// A credential file whose mode is already private can still fail kit's
// privacy check on its macOS ACL. These name that cause for the remedy.
var (
	ErrACLRead     = errors.New("reading its ACL failed")
	ErrExtendedACL = errors.New("has an extended ACL")
)

// Credential is resolved only for a Kata child. Never export it into the
// supervisor, persist it, or pass it to a Herdr tab or command step.
func (k Kata) Credential() (string, error) {
	return k.credential(secretref.Ref.Resolve)
}

func (k Kata) credential(resolve func(secretref.Ref) (secretref.Secret, error)) (string, error) {
	return k.resolveCredential(resolve, filePrivacy)
}

// filePrivacy reports whether a credential file's mode is owner-only, and
// the macOS ACL check's own error, which kit flattens to a mode reason.
func filePrivacy(path string) (bool, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	f, err := safefileio.OpenCurrentUserFile(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return false, err
	}
	return true, safefileio.ValidatePrivateCurrentUserFile(f)
}

func (k Kata) resolveCredential(resolve func(secretref.Ref) (secretref.Secret, error), privacy func(string) (bool, error)) (string, error) {
	if k.CredentialEnv == "" && k.CredentialFile == "" {
		return "", nil
	}
	ref := secretref.Ref{Env: k.CredentialEnv}
	source := "credential_env " + k.CredentialEnv
	if k.CredentialFile != "" {
		ref = secretref.Ref{File: k.CredentialFile}
		source = "credential_file " + k.CredentialFile
	}
	secret, err := resolve(ref)
	// Kit flattens private-file validation failures, including macOS ACL
	// read races, to a privacy reason. Re-resolve the whole source so every
	// attempt still checks ownership, permissions and ACLs before reading.
	for attempt := 1; attempt < 3 && ref.File != "" && err != nil; attempt++ {
		if !privacyFailure(err) {
			break
		}
		secret, err = resolve(ref)
	}
	if err != nil && ref.File != "" && privacyFailure(err) {
		if private, aclErr := privacy(ref.File); private {
			switch {
			case aclErr != nil && strings.Contains(aclErr.Error(), "has a macOS extended ACL"): //nolint:kennlint // safefileio reports an ACL entry as untyped text.
				return "", fmt.Errorf("%s: the file is mode 0600 but %w; remove it with chmod -N", source, ErrExtendedACL)
			case aclErr != nil:
				return "", fmt.Errorf("%s: the file is already owner-only, but %w on 3 attempts (%v); this is usually transient and is retried on the next use", source, ErrACLRead, aclErr)
			default:
				return "", fmt.Errorf("%s: the file is already owner-only, but %w on 3 attempts; this is usually transient and is retried on the next use", source, ErrACLRead)
			}
		}
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", source, err)
	}
	if strings.ContainsRune(secret.Value, 0) {
		return "", fmt.Errorf("%s: credential contains NUL", source)
	}
	return secret.Value, nil
}

// Compare source identity without reading credential values. Missing files still
// compare by normalized path; existing files also catch hard-link aliases.
func sameFileSource(a, b string) bool {
	expand := func(path string) string {
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		return filepath.Clean(path)
	}
	a, b = expand(a), expand(b)
	if a == b {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// privacyFailure matches kit's private-file failures, including a macOS ACL
// read reported as a privacy reason.
func privacyFailure(err error) bool {
	return strings.Contains(err.Error(), "file must be private to its owner") || strings.Contains(err.Error(), "read extended ACL") //nolint:kennlint // secretref flattens privacy failures into untyped reason text.
}
