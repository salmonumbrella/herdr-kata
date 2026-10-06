package store

import (
	"encoding/json"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
)

// ValidateNativeWorkflowPolicy checks the portable subset supported by this executor.
func ValidateNativeWorkflowPolicy(raw json.RawMessage) error {
	var body struct {
		Steps []struct {
			Retries int `json:"retries"`
		} `json:"steps"`
	}
	if err := katacli.Decode(raw, &body); err != nil {
		return err
	}
	for _, step := range body.Steps {
		if step.Retries != 0 {
			return errors.New("unsupported workflow step retries: this installation supports zero")
		}
	}
	return nil
}
