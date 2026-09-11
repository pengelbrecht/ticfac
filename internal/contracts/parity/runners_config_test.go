package parity

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/pengelbrecht/ticfac/internal/herd/config"
)

// contracts/runners-config-contract.json — EXECUTABLE.
//
// Two rules over `.tick/runners.toml`: what `[sandbox].image` may be, and what
// `[orchestration].max_parallel` may be. Both tables ARE read by ticfac —
// since tick wgi, internal/herd/config is the execution-half reader that
// validates the whole table set (the split, and the decision behind it, are
// that package's doc.go) — so the parity here is asserted twice against this
// repository's own side: against the data-driven re-implementation below, and
// against the REAL reader (TestTheRealReaderAgreesWithTheContract). The
// accepted and refused lists are the parity: a pattern relaxed on one side and
// not the other shows up as an accepted string a reader refuses.
//
// The rules stay pinned as data (a pattern string with a maximum length, and a
// minimum) because the file's second reader is TypeScript
// (cloud/factory/src/repo-config.ts), which cannot share a Go regexp; the
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
func imageConfig(t *testing.T, image string) (*config.Config, error) {
	t.Helper()
	doc := "version = 2\n\n[roles.implement]\nkind = \"claude\"\n\n[sandbox]\nimage = " +
		fmt.Sprintf("%q", image) + "\n"
	return config.Parse([]byte(doc))
}

// maxParallelConfig builds the smallest whole config carrying one raw
// max_parallel TOML value.
func maxParallelConfig(t *testing.T, raw string) (*config.Config, error) {
	t.Helper()
	doc := "[roles.implement]\nkind = \"claude\"\n\n[orchestration]\nmax_parallel = " + raw + "\n"
	return config.Parse([]byte(doc))
}
