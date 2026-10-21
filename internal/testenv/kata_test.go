//go:build kata || kata_children || kata_replies

package testenv

import "testing"

func TestRequiredBinaryPresent(t *testing.T) { RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN") }
