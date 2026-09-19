package parity

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/runconfig"
)

// contracts/runners-config-contract.json — EXECUTABLE.
//
// Two rules over `.tick/runners.toml`: what `[sandbox].image` may be, and what
// `[orchestration].max_parallel` may be. Both tables ARE read by ticfac —
// since tick wgi, internal/runconfig is the execution-half reader that
// validates the whole table set (the split, and the decision behind it, are
// that package's doc.go) — so the parity here is asserted twice against this
// repository's own side: against the data-driven re-implementation below, and
// against the REAL reader (TestTheRealReaderAgreesWithTheContract). The
// accepted and refused lists are the parity: a pattern relaxed on one side and
// not the other shows up as an accepted string a reader refuses.
//
// The rules stay pinned as data (a pattern string with a maximum length, and a
// minimum) because the file's second reader is TypeScript
// (cloudflare/src/repo-runconfig.ts), which cannot share a Go regexp; the
// in-repo reader adds the behavioural pin on top of the data pin.
//
// The image rule is the one that matters to ticfac: it is what stands between
// a repository's configuration and a container reference, and
// `orchestrator; rm -rf /` is in the refused list because an image reference
// is a name and never a place to hide a shell fragment.

const runnersConfigFile = "runners-config-contract.json"

type runnersConfig struct {
	Image struct {
		Path           string   `json:"path"`
		Pattern        string   `json:"pattern"`
		MaxLength      int      `json:"max_length"`
		BoundaryChar   string   `json:"boundary_char"`
		RefusalMessage string   `json:"refusal_message"`
		Accepted       []string `json:"accepted"`
		Refused        []string `json:"refused"`
	} `json:"image"`
	MaxParallel struct {
		Path              string   `json:"path"`
		Minimum           int      `json:"minimum"`
		RefusalMessage    string   `json:"refusal_message"`
		Accepted          []int    `json:"accepted"`
		Refused           []int    `json:"refused"`
		RefusedTOMLValues []string `json:"refused_toml_values"`
	} `json:"max_parallel"`
	Tables struct {
		VersionKey          string   `json:"version_key"`
		ExecutionTables     []string `json:"execution_tables"`
		TrackerTables       []string `json:"tracker_tables"`
		ToleratedByConsumer []string `json:"tolerated_by_consumer"`
		ForeignRule         string   `json:"foreign_table_rule"`
		Accepted            []struct {
			Name    string   `json:"name"`
			Readers []string `json:"readers"`
			Toml    string   `json:"toml"`
		} `json:"accepted"`
		Refused []struct {
			Name                string   `json:"name"`
			Readers             []string `json:"readers"`
			ExpectErrorContains string   `json:"expect_error_contains"`
			Toml                string   `json:"toml"`
		} `json:"refused"`
	} `json:"tables"`
	Substrate struct {
		Path              string   `json:"path"`
		Values            []string `json:"values"`
		Default           string   `json:"default"`
		RefusalMessage    string   `json:"refusal_message"`
		Accepted          []string `json:"accepted"`
		Refused           []string `json:"refused"`
		RefusedTOMLValues []string `json:"refused_toml_values"`
	} `json:"substrate"`
}

// acceptImage is ticfac's implementation of the image rule.
func acceptImage(c runnersConfig, value string) bool {
	if value == "" || len(value) > c.Image.MaxLength {
		return false
	}
	return regexp.MustCompile(c.Image.Pattern).MatchString(value)
}

func TestImageReferenceAcceptsAndRefusesTheContractsLists(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	if c.Image.Pattern == "" || c.Image.MaxLength <= 0 {
		t.Fatalf("the image rule is not pinned: %+v", c.Image)
	}
	if _, err := regexp.Compile(c.Image.Pattern); err != nil {
		t.Fatalf("the pinned image pattern does not compile in Go: %v", err)
	}
	if len(c.Image.Accepted) == 0 || len(c.Image.Refused) == 0 {
		t.Fatal("a rule with only accepted or only refused cases tests one direction")
	}

	for _, value := range c.Image.Accepted {
		if !acceptImage(c, value) {
			t.Errorf("the contract accepts %q and this reader refuses it", value)
		}
	}
	for _, value := range c.Image.Refused {
		if acceptImage(c, value) {
			t.Errorf("the contract refuses %q and this reader accepts it", value)
		}
	}
}

// The length boundary, from both sides: `max_length` characters is accepted
// and one more is not. `boundary_char` exists so the two implementations build
// the same string rather than each choosing a filler.
func TestImageLengthBoundaryIsExact(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	if c.Image.BoundaryChar == "" {
		t.Fatal("the contract names no boundary character, so each reader would invent its own")
	}
	atLimit := strings.Repeat(c.Image.BoundaryChar, c.Image.MaxLength)
	if !acceptImage(c, atLimit) {
		t.Errorf("a reference of exactly max_length (%d) was refused", c.Image.MaxLength)
	}
	if acceptImage(c, atLimit+c.Image.BoundaryChar) {
		t.Errorf("a reference of max_length+1 (%d) was accepted", c.Image.MaxLength+1)
	}
}

func TestMaxParallelAcceptsAndRefusesTheContractsLists(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	if c.MaxParallel.Minimum <= 0 {
		t.Fatalf("max_parallel has no positive minimum: %+v", c.MaxParallel)
	}
	for _, value := range c.MaxParallel.Accepted {
		if value < c.MaxParallel.Minimum {
			t.Errorf("the contract accepts %d, which is under its own minimum %d", value, c.MaxParallel.Minimum)
		}
	}
	for _, value := range c.MaxParallel.Refused {
		if value >= c.MaxParallel.Minimum {
			t.Errorf("the contract refuses %d, which satisfies its own minimum %d", value, c.MaxParallel.Minimum)
		}
	}
	// The typed refusals are what a TOML reader has to catch: a float, a
	// string and a boolean are refusals, never coercions.
	if len(c.MaxParallel.RefusedTOMLValues) == 0 {
		t.Error("no typed value is refused; a config reader that coerces \"3\" to 3 would pass")
	}
}

// Both rules name the config path they govern and the exact words they refuse
// with. The message is contract surface: an operator greps for it, and a
// reworded refusal on one side is a rule nobody can match against the other.
func TestBothRulesNameTheirPathAndRefusal(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	for name, pair := range map[string][2]string{
		"image":        {c.Image.Path, c.Image.RefusalMessage},
		"max_parallel": {c.MaxParallel.Path, c.MaxParallel.RefusalMessage},
	} {
		if pair[0] == "" {
			t.Errorf("%s names no config path", name)
		}
		if pair[1] == "" {
			t.Errorf("%s names no refusal message", name)
		}
	}
	if c.Image.RefusalMessage == c.MaxParallel.RefusalMessage {
		t.Error("two distinct rules share one refusal message; a reader cannot tell which refused")
	}
}

// TestTheRealReaderAgreesWithTheContract is the half of the parity this
// repository could not assert before the split: ticfac's own reader, the one a
// run actually parses the target repository's file with, answering the same
// accept/refuse lists the fixture serves. The data-driven checks above prove
// the fixture's rules are self-consistent; this proves ticfac's reader IS
// that fixture's reader, through the whole Parse path — version gate, decode,
// shape validation — not just through a re-implemented regexp.
func TestTheRealReaderAgreesWithTheContract(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	t.Run("image", func(t *testing.T) {
		for _, value := range c.Image.Accepted {
			if _, err := imageConfig(t, value); err != nil {
				t.Errorf("the contract accepts %q and ticfac's reader refuses it: %v", value, err)
			}
		}
		for _, value := range c.Image.Refused {
			_, err := imageConfig(t, value)
			if err == nil {
				t.Errorf("the contract refuses %q and ticfac's reader accepts it", value)
				continue
			}
			if !strings.Contains(err.Error(), "sandbox.image") {
				t.Errorf("the refusal for %q does not name sandbox.image: %v", value, err)
			}
		}
		// The exact-length boundary, through the reader: max_length
		// characters is a whole document that loads, one character more is a
		// stop.
		atLimit := strings.Repeat(c.Image.BoundaryChar, c.Image.MaxLength)
		if _, err := imageConfig(t, atLimit); err != nil {
			t.Errorf("an image of exactly max_length (%d) was refused: %v", c.Image.MaxLength, err)
		}
		if _, err := imageConfig(t, atLimit+c.Image.BoundaryChar); err == nil {
			t.Errorf("an image of max_length+1 (%d) was accepted", c.Image.MaxLength+1)
		}
	})

	t.Run("max_parallel", func(t *testing.T) {
		for _, value := range c.MaxParallel.Accepted {
			if _, err := maxParallelConfig(t, fmt.Sprintf("%d", value)); err != nil {
				t.Errorf("the contract accepts %d and ticfac's reader refuses it: %v", value, err)
			}
		}
		for _, value := range c.MaxParallel.Refused {
			_, err := maxParallelConfig(t, fmt.Sprintf("%d", value))
			if err == nil {
				t.Errorf("the contract refuses %d and ticfac's reader accepts it", value)
				continue
			}
			if !strings.Contains(err.Error(), c.MaxParallel.RefusalMessage) {
				t.Errorf("the refusal for %d does not carry the pinned message %q: %v",
					value, c.MaxParallel.RefusalMessage, err)
			}
		}
		// The typed refusals: a float, a string and a boolean are refusals,
		// never coercions — and only the raw TOML text carries the difference.
		for _, raw := range c.MaxParallel.RefusedTOMLValues {
			if _, err := maxParallelConfig(t, raw); err == nil {
				t.Errorf("max_parallel = %s is a typed value the contract refuses, and the reader coerced it", raw)
			}
		}
	})
}

// imageConfig builds the smallest whole config that carries the image
// reference and runs ticfac's real reader over it.
func imageConfig(t *testing.T, image string) (*runconfig.Config, error) {
	t.Helper()
	doc := "version = 2\n\n[roles.implement]\nkind = \"claude\"\n\n[sandbox]\nimage = " +
		fmt.Sprintf("%q", image) + "\n"
	return runconfig.Parse([]byte(doc))
}

// maxParallelConfig builds the smallest whole config carrying one raw
// max_parallel TOML value.
func maxParallelConfig(t *testing.T, raw string) (*runconfig.Config, error) {
	t.Helper()
	doc := "[roles.implement]\nkind = \"claude\"\n\n[orchestration]\nmax_parallel = " + raw + "\n"
	return runconfig.Parse([]byte(doc))
}

// The table enumeration (bundle 4.0.0): the split this repository's
// internal/runconfig IS one half of, as data. Every accepted case whose
// `readers` names ticfac must load through the REAL reader; every refused one
// must be refused with the words the contract pins. The mirror-direction
// cases (a scalar on [tier_policy], read by ticks' half) run in ticks'
// runners_config_parity_test.go over the same fixture — both halves of the
// split, one file.
func TestTheTableEnumerationMatchesTheRealReader(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	if c.Tables.VersionKey != "version" {
		t.Errorf("the version key is pinned as %q, want \"version\"", c.Tables.VersionKey)
	}
	if c.Tables.ForeignRule == "" {
		t.Fatal("the contract does not state its foreign-table rule")
	}

	// The mirror obligation, behaviourally: every table the contract names as
	// the OTHER reader's half must load here as a foreign table, and a scalar
	// squatting on its name must be refused — the tolerance is keyed on shape,
	// not on the bare name, and that is what separates a legal file on the
	// other side of the split from a typo'd key on this one.
	want := append([]string{}, c.Tables.TrackerTables...)
	sort.Strings(want)
	if !equalSlices(c.Tables.ToleratedByConsumer, want) {
		t.Errorf("the contract says ticfac tolerates %v, which is not its tracker half %v",
			c.Tables.ToleratedByConsumer, want)
	}
	for _, name := range want {
		t.Run("tolerates the foreign table ["+name+"]", func(t *testing.T) {
			doc := "[roles.implement]\nkind = \"claude\"\n\n[" + name + "]\n"
			if _, err := runconfig.Parse([]byte(doc)); err != nil {
				t.Errorf("[%s] is the other reader's half of the split and must load here as a foreign table: %v", name, err)
			}
		})
		t.Run("refuses a scalar on the foreign name "+name, func(t *testing.T) {
			doc := "[roles.implement]\nkind = \"claude\"\n\n" + name + " = true\n"
			_, err := runconfig.Parse([]byte(doc))
			if err == nil {
				t.Fatalf("%s = true is a scalar squatting on a foreign table's name, and the reader accepted it", name)
			}
			if !strings.Contains(err.Error(), name+": unknown key") {
				t.Errorf("the refusal is %q, want %q — a scalar on the name is a typo'd key, not a foreign table", err.Error(), name+": unknown key")
			}
		})
	}

	for _, accepted := range c.Tables.Accepted {
		if !hasReader(accepted.Readers, "ticfac") {
			continue
		}
		t.Run("accepts "+accepted.Name, func(t *testing.T) {
			if _, err := runconfig.Parse([]byte(accepted.Toml)); err != nil {
				t.Errorf("the contract accepts this file and ticfac's reader refuses it:\n%s\n%v", accepted.Toml, err)
			}
		})
	}
	for _, refused := range c.Tables.Refused {
		if !hasReader(refused.Readers, "ticfac") {
			continue
		}
		t.Run("refuses "+refused.Name, func(t *testing.T) {
			_, err := runconfig.Parse([]byte(refused.Toml))
			if err == nil {
				t.Fatalf("the contract refuses this file and ticfac's reader accepted it:\n%s", refused.Toml)
			}
			if !strings.Contains(err.Error(), refused.ExpectErrorContains) {
				t.Errorf("the refusal is %q, the contract pins %q", err.Error(), refused.ExpectErrorContains)
			}
		})
	}
}

// The substrate enum (bundle 4.0.0): the closed vocabulary that decides which
// dispatch verb is correct, run through the real reader over whole documents —
// the values, the default, the refusal words and the typed-value refusals are
// all shared surface with ticks' reader.
func TestTheSubstrateEnumMatchesTheRealReader(t *testing.T) {
	var c runnersConfig
	readContract(t, runnersConfigFile, &c)

	var want []string
	for _, s := range runconfig.Substrates {
		want = append(want, string(s))
	}
	if !equalSlices(c.Substrate.Values, want) {
		t.Errorf("the contract pins substrate values %v, this reader's vocabulary is %v", c.Substrate.Values, want)
	}
	if c.Substrate.Default != string(runconfig.SubstrateAuto) {
		t.Errorf("the contract pins the default as %q, this reader defaults to %q",
			c.Substrate.Default, runconfig.SubstrateAuto)
	}
	if len(c.Substrate.Accepted) == 0 || len(c.Substrate.Refused) == 0 {
		t.Fatal("a parity guard with nothing on one side of it guards nothing")
	}

	for _, value := range c.Substrate.Accepted {
		t.Run("accepts "+value, func(t *testing.T) {
			doc := substrateDocument(t, fmt.Sprintf("%q", value))
			if _, err := runconfig.Parse([]byte(doc)); err != nil {
				t.Errorf("%s refused %q: %v", c.Substrate.Path, value, err)
			}
		})
	}
	for _, value := range c.Substrate.Refused {
		t.Run("refuses "+value, func(t *testing.T) {
			doc := substrateDocument(t, fmt.Sprintf("%q", value))
			_, err := runconfig.Parse([]byte(doc))
			if err == nil {
				t.Fatalf("%s accepted %q, which the contract calls unusable", c.Substrate.Path, value)
			}
			if !strings.Contains(err.Error(), c.Substrate.RefusalMessage) {
				t.Errorf("the refusal for %q does not carry the pinned message %q: %v",
					value, c.Substrate.RefusalMessage, err)
			}
		})
	}
	for _, raw := range c.Substrate.RefusedTOMLValues {
		t.Run("refuses the value "+raw, func(t *testing.T) {
			doc := substrateDocument(t, raw)
			if _, err := runconfig.Parse([]byte(doc)); err == nil {
				t.Errorf("substrate = %s is a typed value the contract refuses, and the reader coerced it", raw)
			}
		})
	}
}

// substrateDocument builds the smallest whole config carrying one raw
// substrate TOML value, in the shape every other case here uses.
func substrateDocument(t *testing.T, raw string) string {
	t.Helper()
	return "[roles.implement]\nkind = \"claude\"\n\n[orchestration]\nsubstrate = " + raw + "\n"
}

func hasReader(readers []string, want string) bool {
	for _, r := range readers {
		if r == want {
			return true
		}
	}
	return false
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
