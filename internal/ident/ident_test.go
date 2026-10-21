package ident

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRunUIDDeterministic(t *testing.T) {
	at := time.Unix(1700000000, 123000000)
	if a, b := RunUID(at, "source"), RunUID(at, "source"); a != b {
		t.Fatalf("%s != %s", a, b)
	}
}
func TestRunUIDDiffersBySource(t *testing.T) {
	at := time.Unix(1, 0)
	if RunUID(at, "a") == RunUID(at, "b") {
		t.Fatal("source ignored")
	}
}
func TestRunUIDTimePrefixSorts(t *testing.T) {
	at := time.Unix(1, 0)
	if RunUID(at, "z") >= RunUID(at.Add(time.Millisecond), "a") {
		t.Fatal("timestamp does not sort")
	}
}
func TestRunUIDKnownVector(t *testing.T) {
	const want = "01HF7YAT3VX6BJJ0Z9NVXMM492"
	at := time.UnixMilli(1700000000123)
	source := "job\x00occ"
	digest := sha256.Sum256([]byte("herdr-kata/run/v1\x00" + source))
	var raw [16]byte
	binary.BigEndian.PutUint64(raw[:8], uint64(at.UnixMilli())<<16)
	copy(raw[6:], digest[:10])
	// Independent big-integer base-32 oracle, rather than the production bit encoder.
	n := new(big.Int).SetBytes(raw[:])
	base := big.NewInt(32)
	rem := new(big.Int)
	oracle := ""
	for range 26 {
		n.QuoRem(n, base, rem)
		oracle = string("0123456789ABCDEFGHJKMNPQRSTVWXYZ"[rem.Int64()]) + oracle
	}
	if oracle != want {
		t.Fatalf("vector transcription: %s", oracle)
	}
	if got := RunUID(at, source); got != want {
		t.Fatalf("UID = %s, want %s", got, want)
	}
}
func TestOccurrenceKeyRoundTripNanos(t *testing.T) {
	at := time.Date(2026, 10, 13, 12, 3, 4, 123456789, time.FixedZone("offset", 3600))
	const want = "2026-10-13T11:03:04.123456789Z"
	if got := OccurrenceKey(at); got != want {
		t.Fatalf("key = %s", got)
	}
	got, err := ParseOccurrenceKey(want)
	if err != nil || !got.Equal(at) {
		t.Fatalf("parse = %v, %v", got, err)
	}
}
func TestParseOccurrenceKeyRejectsInvalid(t *testing.T) {
	for _, s := range []string{"", "manual:abc", "2026-10-13", "2026-13-13T00:00:00Z"} {
		if _, err := ParseOccurrenceKey(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestScheduledSource(t *testing.T) {
	if got := ScheduledSource("job", "occ"); got != "job\x00occ" {
		t.Fatalf("source = %q", got)
	}
}
func TestAgentNameValid(t *testing.T) {
	uid := RunUID(time.Unix(1, 0), "a")
	name := AgentName(uid)
	if len(name) != 29 || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(name) || name != "hk-"+strings.ToLower(uid) {
		t.Fatalf("name = %s", name)
	}
}
func TestNewRunUIDUsesEntropy(t *testing.T) {
	at := time.UnixMilli(0)
	if got := NewRunUID(at, bytes.NewReader(make([]byte, 10))); got != "00000000000000000000000000" {
		t.Fatalf("UID = %s", got)
	}
	if got := NewRunUID(at, bytes.NewReader(bytes.Repeat([]byte{255}, 10))); got != "0000000000ZZZZZZZZZZZZZZZZ" {
		t.Fatalf("UID = %s", got)
	}
}
func TestNewRunUIDEntropyFailure(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("entropy failure silently produced UID")
		}
	}()
	NewRunUID(time.Unix(1, 0), bytes.NewReader(nil))
}
func FuzzOccurrenceKeyRoundTrip(f *testing.F) {
	f.Add(int64(0), int32(0))
	f.Add(int64(1700000000), int32(999999999))
	f.Fuzz(func(t *testing.T, seconds int64, nanos int32) {
		// RFC3339 represents calendar years 0000..9999; materialize arbitrary
		// timestamps into that domain without losing subsecond boundary coverage.
		seconds = max(seconds%253402300800, -62167219200)
		at := time.Unix(seconds, int64(uint32(nanos)%1000000000))
		got, err := ParseOccurrenceKey(OccurrenceKey(at))
		if err != nil || !got.Equal(at) {
			t.Fatalf("roundtrip %v = %v, %v", at, got, err)
		}
	})
}

func FuzzRunUIDBytes(f *testing.F) {
	f.Add(uint64(0), "")
	f.Add(uint64(1<<48-1), "job\x00occ")
	f.Fuzz(func(t *testing.T, millis uint64, source string) {
		millis &= 1<<48 - 1
		uid := RunUID(time.UnixMilli(int64(millis)), source)
		n := new(big.Int)
		for _, c := range uid {
			digit := strings.IndexRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c)
			if digit < 0 {
				t.Fatalf("invalid digit %c", c)
			}
			n.Lsh(n, 5)
			n.Or(n, big.NewInt(int64(digit)))
		}
		raw := n.FillBytes(make([]byte, 16))
		digest := sha256.Sum256([]byte("herdr-kata/run/v1\x00" + source))
		var timestamp uint64
		for _, b := range raw[:6] {
			timestamp = timestamp<<8 | uint64(b)
		}
		if timestamp != millis || !bytes.Equal(raw[6:], digest[:10]) {
			t.Fatalf("identity bytes differ for %d/%q", millis, source)
		}
	})
}
