package defs

import "testing"

// Protect the grammar accepted by herdr-kata's validation boundary, matching Kata.
func TestCronParserParity(t *testing.T) {
	for _, tc := range []struct {
		name, expression string
		wantErr          bool
	}{
		{"five fields", "0 9 * * MON", false},
		{"descriptor", "@daily", false},
		{"interval descriptor", "@every 90s", false},
		{"Sunday zero", "0 9 * * 0", false},
		{"Sunday seven", "0 9 * * 7", false},
		{"Sunday range", "0 9 * * 5-7", false},
		{"Sunday range step", "0 9 * * 5-7/2", false},
		{"Sunday open step", "0 9 * * 5/2", false},
		{"wrapping hours", "0 22-2 * * *", false},
		{"hour span step", "0 */24 * * *", true},
		{"minute span step", "*/60 * * * *", true},
		{"seconds", "0 0 9 * * MON", true},
		{"year", "0 9 * * MON 2026", true},
		{"seconds and year", "0 0 9 * * MON 2026", true},
		{"last day", "0 9 L * *", true},
		{"weekday", "0 9 15W * *", true},
		{"nth weekday", "0 9 * * MON#2", true},
		{"hashed minute", "H 9 * * *", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTrigger(Trigger{Kind: "cron", Cron: tc.expression})
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateTrigger(%q) error = %v, want error %v", tc.expression, err, tc.wantErr)
			}
		})
	}

	for _, directive := range []string{"TZ=", "CRON_TZ="} {
		for _, tc := range []struct {
			name, expression, timezone string
			wantErr                    bool
		}{
			{"matching", "America/New_York 0 9 * * MON", "America/New_York", false},
			{"default UTC", "UTC @daily", "", false},
			{"conflicting", "America/New_York 0 9 * * MON", "UTC", true},
			{"empty zone", " 0 9 * * MON", "UTC", true},
			{"empty directive", "", "", true},
			{"missing schedule", "UTC", "UTC", true},
			{"invalid zone", "Invalid/Zone 0 9 * * MON", "Invalid/Zone", true},
			{"invalid embedded zone", "Invalid/Zone 0 9 * * MON", "UTC", true},
		} {
			t.Run(directive+tc.name, func(t *testing.T) {
				// A panic also fails the contract: bad directives must return errors.
				err := ValidateTrigger(Trigger{Kind: "cron", Cron: directive + tc.expression, Timezone: tc.timezone})
				if (err != nil) != tc.wantErr {
					t.Fatalf("ValidateTrigger(%q, timezone %q) error = %v, want error %v", directive+tc.expression, tc.timezone, err, tc.wantErr)
				}
			})
		}
	}
}
