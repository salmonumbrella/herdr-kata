package config

import (
	"encoding/json"
	"fmt"
	"hegel.dev/go/hegel"
	"math"
	"reflect"
	"strings"
	"testing"
)

// A project-local invalid limit cannot stop a different project's work.
func TestProjectCapacityIsolation(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		n := hegel.Draw(ht, hegel.Integers(math.MinInt, math.MaxInt))
		c, err := loadText(t, example+fmt.Sprintf("\n[[projects]]\nname = \"other-project\"\nactor = \"worker\"\nmax_active_runs = %d\n", n))
		if err != nil {
			ht.Fatalf("project-local limit %d stopped config: %v", n, err)
		}
		if len(c.Projects) == 0 || c.Projects[0].Name != "example-project" {
			ht.Fatalf("valid project lost: %+v", c)
		}
		if n < 0 {
			if len(c.Projects) != 1 {
				ht.Fatalf("negative capacity accepted: %+v", c.Projects)
			}
		} else {
			want := n
			if n == 0 {
				want = 1
			}
			if len(c.Projects) != 2 || c.Projects[1].Name != "other-project" || c.Projects[1].MaxActiveRuns != want {
				ht.Fatalf("capacity %d: %+v", n, c.Projects)
			}
		}
	}, hegel.WithTestCases(1000))
}

func TestBadProjectKeepsValidNeighbor(t *testing.T) {
	for _, bad := range []string{
		`name = "broken-project"`,
		`name = "broken-project"
actor = "worker"
default_agnet = "default"`,
		`name = "broken-project"
actor = "worker"
max_active_runs = "two"`,
	} {
		t.Run(bad, func(t *testing.T) {
			c, err := loadText(t, example+"\n[[projects]]\n"+bad)
			if err != nil {
				t.Fatalf("bad neighbor stopped valid project: %v", err)
			}
			if len(c.Projects) != 1 || c.Projects[0].Name != "example-project" {
				t.Fatalf("valid projects: %+v", c.Projects)
			}
		})
	}
}

// -1 is the named regression for Hegel's first shrunk failure.
func TestNegativeProjectLimitQuarantined(t *testing.T) {
	c, err := loadText(t, example+`
[[projects]]
name = "broken-project"
actor = "worker"
max_active_runs = -1
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Quarantine{{2, "broken-project", "max_active_runs must not be negative"}}
	if !reflect.DeepEqual(c.Quarantined, want) {
		t.Fatalf("quarantine = %+v, want %+v", c.Quarantined, want)
	}
}

func TestProjectQuarantineReasons(t *testing.T) {
	for _, tt := range []struct{ name, fields, want string }{
		{"missing name", `actor = "worker"`, "name and actor are required"},
		{"missing actor", `name = "broken-project"`, "name and actor are required"},
		{"checkout", `name = "broken-project"
actor = "worker"
default_checkout = "missing"`, "default_checkout not configured: missing"},
		{"agent", `name = "broken-project"
actor = "worker"
default_agent = "missing"`, "default_agent not configured: missing"},
		{"unknown keys", `name = "broken-project"
actor = "worker"
z_typo = true
a_typo = true`, "unknown keys: a_typo, z_typo"},
		{"multiple", `name = "broken-project"
z_typo = true
max_active_runs = -1
default_checkout = "missing"
default_agent = "missing"`, "unknown keys: z_typo; name and actor are required; max_active_runs must not be negative; default_checkout not configured: missing; default_agent not configured: missing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := loadText(t, example+"\n[[projects]]\n"+tt.fields)
			if err != nil {
				t.Fatal(err)
			}
			name := "broken-project"
			if tt.name == "missing name" {
				name = ""
			}
			want := []Quarantine{{2, name, tt.want}}
			if len(c.Projects) != 1 || c.Projects[0].Name != "example-project" || !reflect.DeepEqual(c.Quarantined, want) {
				t.Fatalf("config = %+v, want quarantine %+v", c, want)
			}
		})
	}
}

func TestAllDuplicateProjectEntriesQuarantined(t *testing.T) {
	for _, invalid := range []string{"", "max_active_runs = -1\n", "max_active_runs = \"two\"\n", "default_agnet = true\n"} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%v", invalid, reverse), func(t *testing.T) {
				first, last := "", invalid
				if reverse {
					first, last = last, first
				}
				c, err := loadText(t, "executor=\"worker\"\nhc_bin=\"/bin/sh\"\n[kata]\nbin=\"/bin/sh\"\ndaemon=\"fixture\"\n[[projects]]\nname=\"duplicate\"\nactor=\"worker\"\n"+first+"[[projects]]\nname=\"example-project\"\nactor=\"worker\"\n[[projects]]\nname=\"duplicate\"\nactor=\"worker\"\n"+last)
				if err != nil {
					t.Fatal(err)
				}
				if len(c.Projects) != 1 || c.Projects[0].Name != "example-project" || len(c.Quarantined) != 2 {
					t.Fatalf("config = %+v", c)
				}
				for i, entry := range []int{1, 3} {
					q := c.Quarantined[i]
					if q.Entry != entry || q.Name != "duplicate" || !strings.HasSuffix(q.Reason, "duplicate project name") {
						t.Fatalf("quarantine = %+v", q)
					}
					if invalid == "max_active_runs = \"two\"\n" && ((i == 0) == reverse) && !strings.HasPrefix(q.Reason, "invalid value: ") {
						t.Fatalf("type reason lost: %+v", q)
					}
				}
			})
		}
	}
}

func TestNestedProjectErrorsStayLocal(t *testing.T) {
	for _, fields := range []string{
		`default_agnet = { nested = true }`,
		`default_agnet.nested = true`,
		`[projects.default_agnet]
nested = true`,
		`max_active_runs = { nested = true }`,
		`name = { nested = true }`,
	} {
		t.Run(fields, func(t *testing.T) {
			identity := "name=\"broken-project\"\n"
			if strings.HasPrefix(fields, "name =") {
				identity = ""
			}
			text := example + "\n[[projects]]\nactor=\"worker\"\n" + identity + fields
			c, err := loadText(t, text)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Projects) != 1 || c.Projects[0].Name != "example-project" || len(c.Quarantined) != 1 {
				t.Fatalf("config = %+v", c)
			}
			if strings.Contains(fields, "default_agnet") && c.Quarantined[0].Reason != "unknown keys: default_agnet" {
				t.Fatalf("nested unknown key not diagnosed: %+v", c.Quarantined)
			}
			c, err = loadText(t, "global_typo = true\n"+text)
			if err == nil || c != nil {
				t.Fatalf("global error hidden: %+v %v", c, err)
			}
		})
	}
}

func TestProjectShapes(t *testing.T) {
	base := "executor=\"worker\"\nhc_bin=\"/bin/sh\"\n[kata]\nbin=\"/bin/sh\"\ndaemon=\"fixture\"\n"
	for _, entry := range []string{`17`, `"bad"`, `false`, `[]`} {
		c, err := loadText(t, "projects=["+entry+", {name=\"example-project\",actor=\"worker\"}]\n"+base)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Projects) != 1 || !reflect.DeepEqual(c.Quarantined, []Quarantine{{1, "", "not a table"}}) {
			t.Fatalf("shape %s: %+v", entry, c)
		}
	}
	for _, text := range []string{base, "projects=[]\n" + base} {
		c, err := loadText(t, text)
		if err != nil || len(c.Projects) != 0 || len(c.Quarantined) != 0 {
			t.Fatalf("empty projects: %+v %v", c, err)
		}
	}
	c, err := loadText(t, "projects=[{}]\n"+base)
	if err != nil || len(c.Projects) != 0 || !reflect.DeepEqual(c.Quarantined, []Quarantine{{1, "", "name and actor are required"}}) {
		t.Fatalf("empty entry: %+v %v", c, err)
	}
}

func TestGlobalErrorsRemainFatal(t *testing.T) {
	for _, text := range []string{
		"typo=true\n" + example,
		`"projects.typo"=true` + "\n" + example,
		strings.Replace(example, "[kata]", "[kata]\ntypo=true", 1),
		example + "\n[limits]\ntypo=true\n",
		example + "\n[limits]\nmax_active_runs=0\n",
		example + "\n[limits]\nmax_active_runs=\"two\"\n",
		example + "\n[limits]\ndefault_timeout=\"-1s\"\n",
		strings.Replace(example, "[profiles.default]", "[profiles.default]\ntypo=true", 1),
		strings.Replace(example, "/srv/projects/example", "relative", 1),
		strings.Replace(example, `executor = "work-runtime"`, "", 1),
		"projects=\"x\"\n" + example,
		example + "\n[[projects]",
		"quarantined=[]\n" + example,
	} {
		c, err := loadText(t, text)
		if err == nil || c != nil {
			t.Fatalf("accepted invalid global: %+v %v\n%s", c, err, text)
		}
	}
}

func TestProjectKeysAndCasePreserved(t *testing.T) {
	text := strings.Replace(example, "commands = false", "commands = true\ndefault_checkout=\"example-project\"\ndefault_agent=\"default\"", 1)
	for _, f := range reflect.VisibleFields(reflect.TypeFor[Project]()) {
		key := f.Tag.Get("toml")
		text = strings.ReplaceAll(text, key+" ", strings.ToUpper(key)+" ")
		text = strings.ReplaceAll(text, key+"=", strings.ToUpper(key)+"=")
	}
	text = strings.ReplaceAll(text, "[[projects]]", "[[PROJECTS]]")
	c, err := loadText(t, text)
	if err != nil {
		t.Fatal(err)
	}
	want := []Project{{Name: "example-project", Actor: "worker", Commands: true, MaxActiveRuns: 1, DefaultCheckout: "example-project", DefaultAgent: "default"}}
	if !reflect.DeepEqual(c.Projects, want) || len(c.Quarantined) != 0 {
		t.Fatalf("case/tags not accepted: %+v", c)
	}
}

func TestQuarantineString(t *testing.T) {
	for _, tt := range []struct {
		q    Quarantine
		want string
	}{
		{Quarantine{2, "example-project", "duplicate project name"}, "project entry 2 (example-project): duplicate project name"},
		{Quarantine{3, "", "name and actor are required"}, "project entry 3: name and actor are required"},
	} {
		if got := tt.q.String(); got != tt.want {
			t.Fatalf("got %q, want %q", got, tt.want)
		}
	}
}

const globalsOnly = `executor="worker"
hc_bin="/bin/sh"
[kata]
bin="/bin/sh"
daemon="fixture"
`

func TestProjectStringAndBooleanPreservation(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		name := hegel.Draw(ht, hegel.Text())
		actor := hegel.Draw(ht, hegel.Text())
		commands := hegel.Draw(ht, hegel.Booleans())
		n := quotedProjectString(name)
		a := quotedProjectString(actor)
		c, err := loadText(t, globalsOnly+fmt.Sprintf("\n[[projects]]\nname=%s\nactor=%s\ncommands=%t\n", n, a, commands))
		if err != nil {
			ht.Fatal(err)
		}
		if name == "" || actor == "" {
			if len(c.Projects) != 0 || !reflect.DeepEqual(c.Quarantined, []Quarantine{{1, name, "name and actor are required"}}) {
				ht.Fatalf("invalid identity: %+v", c)
			}
		} else {
			want := []Project{{Name: name, Actor: actor, Commands: commands, MaxActiveRuns: 1}}
			if !reflect.DeepEqual(c.Projects, want) || len(c.Quarantined) != 0 {
				ht.Fatalf("string/boolean values changed: %+v, want %+v", c, want)
			}
		}
	}, hegel.WithTestCases(1000))
}

func TestRawProjectNameSurvivesTypedFailure(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		name := hegel.Draw(ht, hegel.Text())
		field := hegel.Draw(ht, hegel.SampledFrom([]string{"actor", "commands", "max_active_runs", "default_checkout", "default_agent"}))
		n := quotedProjectString(name)
		c, err := loadText(t, globalsOnly+fmt.Sprintf("\n[[projects]]\nname=%s\n%s=[]\n", n, field))
		if err != nil {
			ht.Fatal(err)
		}
		if len(c.Projects) != 0 || len(c.Quarantined) != 1 || c.Quarantined[0].Name != name || c.Quarantined[0].Entry != 1 || !strings.HasPrefix(c.Quarantined[0].Reason, "invalid value: ") || !strings.Contains(c.Quarantined[0].Reason, "projects."+field) {
			ht.Fatalf("typed failure identity/reason: %+v", c)
		}
	}, hegel.WithTestCases(1000))
}

func TestDuplicateGroupsQuarantined(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		name := hegel.Draw(ht, hegel.Text())
		count := min(hegel.Draw(ht, hegel.Integers(2, math.MaxInt)), 10) // Bound group materialization, not the generator domain.
		n := quotedProjectString(name)
		text := globalsOnly + strings.Repeat(fmt.Sprintf("\n[[projects]]\nname=%s\nactor=\"worker\"\n", n), count)
		c, err := loadText(t, text)
		if err != nil {
			ht.Fatal(err)
		}
		reason := "duplicate project name"
		if name == "" {
			reason = "name and actor are required"
		}
		if len(c.Projects) != 0 || len(c.Quarantined) != count {
			ht.Fatalf("duplicate group escaped: %+v", c)
		}
		for i, q := range c.Quarantined {
			if q != (Quarantine{i + 1, name, reason}) {
				ht.Fatalf("duplicate diagnostic %+v at %d", q, i+1)
			}
		}
	}, hegel.WithTestCases(1000))
}

func TestProjectNumericBounds(t *testing.T) {
	c, err := loadText(t, globalsOnly+"\n[[projects]]\nname=\"example-project\"\nactor=\"worker\"\nmax_active_runs=-9223372036854775808\n")
	if err != nil || len(c.Projects) != 0 || !reflect.DeepEqual(c.Quarantined, []Quarantine{{1, "example-project", "max_active_runs must not be negative"}}) {
		t.Fatalf("minimum integer: %+v %v", c, err)
	}
	c, err = loadText(t, globalsOnly+"\n[[projects]]\nname=\"example-project\"\nactor=\"worker\"\nmax_active_runs=9223372036854775807\n")
	if err != nil || len(c.Projects) != 1 || c.Projects[0].MaxActiveRuns != math.MaxInt || len(c.Quarantined) != 0 {
		t.Fatalf("maximum integer: %+v %v", c, err)
	}
	c, err = loadText(t, globalsOnly+"\n[[projects]]\nmax_active_runs=9223372036854775808\n")
	if err == nil || c != nil {
		t.Fatalf("out-of-range TOML integer accepted: %+v %v", c, err)
	}
}

// JSON escaping needs one extra escape to produce a TOML basic string: DEL
// is legal literal JSON but must be escaped in TOML. Keep the full text domain.
func quotedProjectString(value string) string {
	raw, _ := json.Marshal(value)
	return strings.ReplaceAll(string(raw), "\x7f", `\u007f`)
}

func TestEscapedDeleteCharacterProjectIdentity(t *testing.T) {
	c, err := loadText(t, globalsOnly+`
[[projects]]
name="\u007f"
actor="\u007f"
`)
	want := []Project{{Name: "\x7f", Actor: "\x7f", MaxActiveRuns: 1}}
	if err != nil || !reflect.DeepEqual(c.Projects, want) || len(c.Quarantined) != 0 {
		t.Fatalf("escaped identity: %+v %v", c, err)
	}
}

func TestConflictingNameAliasesQuarantineEveryIdentity(t *testing.T) {
	text := globalsOnly + `
[[projects]]
name="example-project"
NAME="other-project"
actor="worker"
[[projects]]
name="example-project"
actor="worker"
[[projects]]
name="other-project"
actor="worker"
[[projects]]
name="valid-project"
actor="worker"
`
	for range 128 {
		c, err := loadText(t, text)
		if err != nil {
			t.Fatal(err)
		}
		want := []Project{{Name: "valid-project", Actor: "worker", MaxActiveRuns: 1}}
		if !reflect.DeepEqual(c.Projects, want) || len(c.Quarantined) != 3 {
			t.Fatalf("ambiguous identities escaped quarantine: %+v", c)
		}
		if c.Quarantined[0] != (Quarantine{1, "example-project", "invalid value: conflicting name keys; duplicate project name"}) {
			t.Fatalf("unstable alias diagnostic: %+v", c.Quarantined)
		}
	}
}

func TestAgreeingNameAliasesRemainAccepted(t *testing.T) {
	c, err := loadText(t, globalsOnly+"\n[[projects]]\nname=\"example-project\"\nNAME=\"example-project\"\nactor=\"worker\"\n")
	want := []Project{{Name: "example-project", Actor: "worker", MaxActiveRuns: 1}}
	if err != nil || !reflect.DeepEqual(c.Projects, want) || len(c.Quarantined) != 0 {
		t.Fatalf("agreeing aliases rejected: %+v %v", c, err)
	}
}

func TestConflictingProjectContainersRemainFatal(t *testing.T) {
	for _, roots := range [][2]string{{"projects", "PROJECTS"}, {"PROJECTS", "projects"}} {
		for _, second := range []string{"name=\"example-project\"\nactor=\"worker\"", "name=\"other-project\"\nactor=\"worker\"", "name=\"other-project\"\nactor=[]", "name=\"other-project\"\ntypo=true"} {
			c, err := loadText(t, globalsOnly+"\n[["+roots[0]+"]]\nname=\"example-project\"\nactor=\"worker\"\n[["+roots[1]+"]]\n"+second)
			if err == nil || c != nil || !strings.Contains(err.Error(), "conflicting projects keys") {
				t.Fatalf("ambiguous global container accepted: %+v %v", c, err)
			}
		}
	}
}

func TestTypedInvalidNameAliasRetainsEveryStringIdentity(t *testing.T) {
	for _, aliases := range []string{"NAME=7\nname=\"example-project\"", "NAME=\"example-project\"\nname=7", "NAME=\"other-project\"\nname=\"example-project\"\nactor=7"} {
		for _, reverse := range []bool{false, true} {
			bad := "\n[[projects]]\n" + aliases + "\n"
			good := "\n[[projects]]\nname=\"example-project\"\nactor=\"worker\"\n"
			first, last := bad, good
			if reverse {
				first, last = last, first
			}
			for range 32 {
				c, err := loadText(t, globalsOnly+first+last)
				if err != nil || len(c.Projects) != 0 || len(c.Quarantined) != 2 {
					t.Fatalf("invalid alias hid duplicate: %+v %v", c, err)
				}
				for _, q := range c.Quarantined {
					if q.Name != "example-project" || !strings.HasSuffix(q.Reason, "duplicate project name") {
						t.Fatalf("alias identity lost: %+v", q)
					}
				}
			}
		}
	}
}
