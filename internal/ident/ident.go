// Package ident defines stable occurrence keys and ULID run identities.
package ident

import (
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"
)

func ScheduledSource(jobUID, occKey string) string { return jobUID + "\x00" + occKey }
func OccurrenceKey(t time.Time) string             { return t.UTC().Format(time.RFC3339Nano) }
func ParseOccurrenceKey(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
func AgentName(runUID string) string { return "hk-" + strings.ToLower(runUID) }
func RunUID(at time.Time, source string) string {
	digest := sha256.Sum256([]byte("herdr-kata/run/v1\x00" + source))
	return encode(at, digest[:10])
}

// NewRunUID panics when entropy cannot be read: its frozen signature cannot
// return an error, and continuing with a predictable manual identity is unsafe.
func NewRunUID(now time.Time, r io.Reader) string {
	var entropy [10]byte
	if _, err := io.ReadFull(r, entropy[:]); err != nil {
		panic(fmt.Errorf("run UID entropy: %w", err))
	}
	return encode(now, entropy[:])
}
func encode(at time.Time, entropy []byte) string {
	ms := at.UnixMilli()
	if ms < 0 || ms > 1<<48-1 {
		panic("run UID timestamp outside ULID range")
	}
	var raw [16]byte
	for i := 5; i >= 0; i-- {
		raw[i] = byte(ms)
		ms >>= 8
	}
	copy(raw[6:], entropy)
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var out [26]byte
	// A ULID has 128 bits; the first base-32 digit includes two leading zero bits.
	for i := range out {
		var digit byte
		for j := range 5 {
			bit := i*5 + j - 2
			digit <<= 1
			if bit >= 0 {
				digit |= (raw[bit/8] >> uint(7-bit%8)) & 1
			}
		}
		out[i] = alphabet[digit]
	}
	return string(out[:])
}
