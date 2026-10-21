package kata

import (
	"context"
	"fmt"
	"testing"
)

func TestAmbiguousWrite(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{context.DeadlineExceeded, true},
		{fmt.Errorf("wrapped: %w", &Error{Code: "process_error"}), true},
		{&Error{Code: "timeout"}, true},
		{&Error{Code: "conflict", Exit: 5}, false},
		{&Error{Code: "board_route_refused", Exit: 6}, false},
	} {
		if got := Ambiguous(tt.err); got != tt.want {
			t.Fatalf("%v: got %v, want %v", tt.err, got, tt.want)
		}
	}
}
