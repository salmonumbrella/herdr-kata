package katacli

import "encoding/json"

// MergeObject replaces the fields owned by an editor while retaining unknown
// raw fields for peers and newer releases, including exact numeric values.
func MergeObject(original json.RawMessage, owned any) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(original) > 0 {
		if e := Decode(original, &fields); e != nil {
			return nil, e
		}
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
	}
	raw, e := json.Marshal(owned)
	if e != nil {
		return nil, e
	}
	var updates map[string]json.RawMessage
	if e := Decode(raw, &updates); e != nil {
		return nil, e
	}
	for key, value := range updates {
		fields[key] = value
	}
	return json.Marshal(fields)
}
