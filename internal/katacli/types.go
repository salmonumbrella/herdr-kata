package katacli

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strings"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func NormalizeUID(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 26 || s[0] > '7' {
		return "", errors.New("identity must be a ULID")
	}
	for _, r := range s {
		if !strings.ContainsRune(alphabet, r) {
			return "", errors.New("identity must be a ULID")
		}
	}
	return s, nil
}
func NewUID() (string, error) {
	var raw [16]byte
	if _, e := rand.Read(raw[6:]); e != nil {
		return "", e
	}
	ms := time.Now().UnixMilli()
	for i := 5; i >= 0; i-- {
		raw[i] = byte(ms)
		ms >>= 8
	}
	n := new(big.Int).SetBytes(raw[:])
	base := big.NewInt(32)
	var result [26]byte
	rem := new(big.Int)
	for i := 25; i >= 0; i-- {
		n.QuoRem(n, base, rem)
		result[i] = alphabet[rem.Int64()]
	}
	return string(result[:]), nil
}
func Decode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return errors.New("JSON must contain exactly one value")
	}
	return nil
}

type Definition struct {
	UID                string          `json:"uid"`
	ProjectID          int64           `json:"project_id"`
	Name               string          `json:"name"`
	DefinitionEventUID string          `json:"definition_event_uid"`
	Revision           int64           `json:"revision"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	DeletedAt          *time.Time      `json:"deleted_at,omitempty"`
	Definition         json.RawMessage `json:"definition"`
}

// Draft retains its create identity and last observed winner across save failure.
type Draft struct {
	Resource, UID, Name, ExpectedEventUID string
	Definition                            json.RawMessage
}

func NewDraft(resource, uid, name string, raw json.RawMessage, expected string) (Draft, error) {
	if resource != "job" && resource != "flow" {
		return Draft{}, errors.New("definition resource must be job or flow")
	}
	var e error
	if uid == "" {
		uid, e = NewUID()
	} else {
		uid, e = NormalizeUID(uid)
	}
	if e != nil {
		return Draft{}, e
	}
	if expected != "" {
		expected, e = NormalizeUID(expected)
		if e != nil {
			return Draft{}, e
		}
	}
	if len(raw) > 256*1024 || !json.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return Draft{}, errors.New("definition must be a JSON object of at most256KiB")
	}
	return Draft{resource, uid, name, expected, append(json.RawMessage(nil), raw...)}, nil
}
func (d Draft) Args() []string {
	if d.ExpectedEventUID == "" {
		return []string{"automation", d.Resource, "create", "--uid", d.UID, "--file", "-"}
	}
	return []string{"automation", d.Resource, "update", d.UID, "--expected-event-uid", d.ExpectedEventUID, "--file", "-"}
}
func (d Draft) Body(actor string) json.RawMessage {
	raw, _ := json.Marshal(struct {
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
		Actor      string          `json:"actor"`
		Expected   string          `json:"expected_event_uid,omitempty"`
	}{d.Name, d.Definition, actor, d.ExpectedEventUID})
	return raw
}
