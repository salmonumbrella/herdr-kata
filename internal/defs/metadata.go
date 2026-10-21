package defs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

func ValidateJobTags(tags []string) error {
	if len(tags) > 16 {
		return fmt.Errorf("tags: at most 16 tags")
	}
	for _, tag := range tags {
		if !validJobTag(tag) {
			return fmt.Errorf("tags: each tag must match charset [a-z0-9._:-] and length 1..64")
		}
	}

	return nil
}

// Copied from Kata f3a06c1 internal/db/import_policy.go: validImportLabel.
// Kata's internal packages cannot be imported by the plugin.
func validJobTag(label string) bool {
	if len(label) < 1 || len(label) > 64 {
		return false
	}
	for _, character := range label {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '.' || character == '_' || character == ':' || character == '-':
		default:
			return false
		}
	}
	return true
}

func ValidateJobDescription(description string) error {
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > 200 {
		return fmt.Errorf("description: at most 200 runes")
	}
	return nil
}

// JobMetadata omits malformed foreign fields without making a job unsupported.
func JobMetadata(options json.RawMessage) (tags []string, description string, warnings []string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(options, &fields) != nil {
		return
	}
	if raw, ok := fields["tags"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &tags) != nil {
			warnings = append(warnings, "invalid options.tags")
			tags = nil
		} else if err := ValidateJobTags(tags); err != nil {
			warnings = append(warnings, "invalid options.tags: "+err.Error())
			tags = nil
		}
	}
	if raw, ok := fields["description"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &description) != nil {
			warnings = append(warnings, "invalid options.description")
			description = ""
		} else if err := ValidateJobDescription(description); err != nil {
			warnings = append(warnings, "invalid options.description: "+err.Error())
			description = ""
		}
	}
	return
}
