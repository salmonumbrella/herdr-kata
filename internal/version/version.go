// Package version identifies releases and immutable executable builds.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

func String() string {
	return "0.1.0"
}
func BuildID(exe string) (string, error) {
	f, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
