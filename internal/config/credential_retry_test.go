package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.kenn.io/kit/secretref"
)

// Kit flattens the intermittent macOS ACL read failure to this privacy reason.
// Recovery must happen before callers can publish a route refusal.
func TestCredentialRetriesPrivacyCheckBeforeReturning(t *testing.T) {
	for _, reason := range []string{"file must be private to its owner (mode 0600)", "read extended ACL: errno 260"} {
		t.Run(reason, func(t *testing.T) {
			k := Kata{CredentialFile: "/srv/projects/example/token", CredentialEnv: "KATA_TOKEN"}
			calls := 0
			value, err := k.credential(func(ref secretref.Ref) (secretref.Secret, error) {
				if ref.File != k.CredentialFile || ref.Env != "" {
					t.Fatalf("wrong source: %+v", ref)
				}
				calls++
				if calls < 3 {
					return secretref.Secret{}, fmt.Errorf("secretref: file:token: %s", reason)
				}
				return secretref.Secret{Value: "fixture-token"}, nil
			})
			if err != nil || value != "fixture-token" || calls != 3 {
				t.Fatalf("credential=%q err=%v calls=%d", value, err, calls)
			}
		})
	}
}

func TestCredentialRetriesStayBoundedAndFailClosed(t *testing.T) {
	for _, tc := range []struct {
		reason    string
		wantCalls int
	}{
		{"file must be private to its owner (mode 0600)", 3},
		{"read extended ACL: ACL grants access", 3},
		{"file is missing", 1},
		{"file is unreadable", 1},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			calls := 0
			k := Kata{CredentialFile: "/srv/projects/example/token"}
			value, err := k.credential(func(secretref.Ref) (secretref.Secret, error) {
				calls++
				return secretref.Secret{}, errors.New(tc.reason)
			})
			if value != "" || err == nil || !strings.Contains(err.Error(), tc.reason) || calls != tc.wantCalls {
				t.Fatalf("credential=%q err=%v calls=%d", value, err, calls)
			}
			// A failed resolution never poisons subsequent attempts.
			value, err = k.credential(func(secretref.Ref) (secretref.Secret, error) { return secretref.Secret{Value: "rotated-token"}, nil })
			if err != nil || value != "rotated-token" {
				t.Fatalf("credential did not recover: %q %v", value, err)
			}
		})
	}
}

// Kit reports a failed macOS ACL read as "file must be private to its owner
// (mode 0600)". When the file's mode is already private, the error names the
// ACL read and the retries instead of asking for a mode the file has.
func TestCredentialNamesTransientACLReadOnPrivateFile(t *testing.T) {
	for _, tc := range []struct {
		name, cause, want, refuse string
		is                        error
	}{
		{"ACL read failed", "read extended ACL: input/output error", "reading its ACL failed on 3 attempts (read extended ACL: input/output error); this is usually transient and is retried on the next use", "mode 0600", ErrACLRead},
		{"ACL read recovered", "", "reading its ACL failed on 3 attempts; this is usually transient and is retried on the next use", "mode 0600", ErrACLRead},
		{"extended ACL", "safefileio: file has a macOS extended ACL", "has an extended ACL; remove it with chmod -N", "transient", ErrExtendedACL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := Kata{CredentialFile: "/srv/projects/example/token"}
			resolve := func(secretref.Ref) (secretref.Secret, error) {
				return secretref.Secret{}, errors.New("secretref: file:token: file must be private to its owner (mode 0600)")
			}
			_, err := k.resolveCredential(resolve, func(path string) (bool, error) {
				if path != k.CredentialFile {
					t.Fatalf("diagnosed %q", path)
				}
				if tc.cause == "" {
					return true, nil
				}
				return true, errors.New(tc.cause)
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.refuse) || !errors.Is(err, tc.is) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	// Kit can also report the ACL read itself; a private file gets the same diagnosis.
	k0 := Kata{CredentialFile: "/srv/projects/example/token"}
	_, err0 := k0.resolveCredential(func(secretref.Ref) (secretref.Secret, error) {
		return secretref.Secret{}, errors.New("read extended ACL: input/output error")
	}, func(string) (bool, error) { return true, errors.New("read extended ACL: input/output error") })
	if !errors.Is(err0, ErrACLRead) {
		t.Fatalf("err=%v", err0)
	}
	// A file whose mode is shared keeps the mode remedy.
	k := Kata{CredentialFile: "/srv/projects/example/token"}
	_, err := k.resolveCredential(func(secretref.Ref) (secretref.Secret, error) {
		return secretref.Secret{}, errors.New("file must be private to its owner (mode 0600)")
	}, func(string) (bool, error) { return false, nil })
	if err == nil || !strings.Contains(err.Error(), "mode 0600") || strings.Contains(err.Error(), "ACL") {
		t.Fatalf("err=%v", err)
	}
}
